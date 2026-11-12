package message

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"
)

// pubIDHeader carries a per-publish identifier through the broker so a
// basic.return can be attributed to exactly the publish that caused it, even
// when callers reuse message IDs. It is an x- header, so consumers never see it.
const pubIDHeader = "x-nforge-pubid"

// returnsHub collects basic.return frames of the current publisher channel.
// AMQP guarantees a broker sends basic.return before the basic.ack of the same
// message and amqp091 dispatches frames in order, so by the time a publish's
// confirmation resolves its return (if any) is already in the channel buffer:
// take drains it synchronously, no goroutine races the answer.
type returnsHub struct {
	mu  sync.Mutex
	ch  <-chan amqp.Return
	ids map[string]struct{}
	seq atomic.Uint64
}

const maxStaleReturns = 4096

func (h *returnsHub) attach(ch <-chan amqp.Return) {
	h.mu.Lock()
	h.ch = ch
	h.ids = map[string]struct{}{}
	h.mu.Unlock()
}

func (h *returnsHub) nextID() string {
	return fmt.Sprintf("%x-%x", time.Now().UnixNano(), h.seq.Add(1))
}

// drainLocked moves buffered returns into the id set.
func (h *returnsHub) drainLocked() {
	for h.ch != nil {
		select {
		case r, ok := <-h.ch:
			if !ok {
				h.ch = nil
				return
			}
			if id, _ := r.Headers[pubIDHeader].(string); id != "" {
				if len(h.ids) >= maxStaleReturns {
					h.ids = map[string]struct{}{} // returns of abandoned publishes: bounded, never unbounded
				}
				h.ids[id] = struct{}{}
			}
		default:
			return
		}
	}
}

// drain keeps the buffer from filling with returns nobody will claim.
func (h *returnsHub) drain() {
	h.mu.Lock()
	h.drainLocked()
	h.mu.Unlock()
}

// take reports (and forgets) whether publish id was returned as unroutable.
func (h *returnsHub) take(id string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.drainLocked()
	_, ok := h.ids[id]
	delete(h.ids, id)
	return ok
}

// publishConfirmed publishes one message and waits for ITS confirmation only.
// It holds the client mutex just long enough to read the channel, never across
// the network wait, so publishers pipeline and Status stays responsive.
//
// Outcomes: nil (confirmed and, when mandatory, routed), ErrUnroutable,
// ErrNacked (the broker refused it), ErrPublishUncertain (the answer was lost:
// timeout, caller gone, channel closed while waiting), ErrTransportUnavailable
// (nothing was sent), or the caller's context error before anything was sent.
func (c *Client) publishConfirmed(ctx context.Context, exchange, key string, mandatory bool, p amqp.Publishing) error {
	err := c.publishConfirmedRaw(ctx, exchange, key, mandatory, p)
	c.countPublish(err)
	return err
}

func (c *Client) publishConfirmedRaw(ctx context.Context, exchange, key string, mandatory bool, p amqp.Publishing) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	c.mu.Lock()
	if err := c.usableLocked(); err != nil {
		c.mu.Unlock()
		return err
	}
	pub := c.pub
	c.mu.Unlock()

	var id string
	if mandatory {
		c.returns.drain()
		id = c.returns.nextID()
		h := make(amqp.Table, len(p.Headers)+1)
		for k, v := range p.Headers {
			h[k] = v
		}
		h[pubIDHeader] = id
		p.Headers = h
	}
	dc, err := pub.PublishWithDeferredConfirmWithContext(ctx, exchange, key, mandatory, false, p)
	if err != nil {
		c.recoverPublisher(pub)
		if pub.IsClosed() {
			return fmt.Errorf("%w: publisher channel closed", ErrTransportUnavailable)
		}
		return fmt.Errorf("%w: publish interrupted", ErrPublishUncertain)
	}
	wctx, cancel := context.WithTimeout(ctx, c.cfg.Publisher.ConfirmTimeout)
	defer cancel()
	acked, werr := dc.WaitContext(wctx)
	if werr != nil {
		c.recoverPublisher(pub)
		return fmt.Errorf("%w: no confirmation in time", ErrPublishUncertain)
	}
	if !acked {
		// amqp091 resolves pending confirmations as nacks when the channel dies:
		// that is not a broker refusal, the outcome is unknown.
		if pub.IsClosed() {
			c.recoverPublisher(pub)
			return fmt.Errorf("%w: channel closed before the confirmation", ErrPublishUncertain)
		}
		return ErrNacked
	}
	if mandatory && c.returns.take(id) {
		return ErrUnroutable
	}
	return nil
}

// recoverPublisher handles a publisher channel the broker closed (a channel
// level error such as a deleted exchange): the supervisor only watches the
// connection, so without this the client would stay unusable until it dropped.
// Closing the connection makes the supervisor rebuild channel and topology.
func (c *Client) recoverPublisher(pub *amqp.Channel) {
	if pub == nil || !pub.IsClosed() {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.pub == pub && c.conn != nil && !c.conn.IsClosed() {
		c.fanoutMu.Lock()
		c.fanoutDeclared = nil // the exchange may be the cause: declare it again
		c.fanoutMu.Unlock()
		go func(conn *amqp.Connection) { _ = conn.Close() }(c.conn)
	}
}
