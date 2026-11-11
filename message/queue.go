package message

import (
	"fmt"
	"strings"
	"time"
)

type queueType string

const (
	classic queueType = "classic"
	quorum  queueType = "quorum"
)

// QueueSpec declares an immutable RabbitMQ queue and its optional failure policy.
type QueueSpec struct {
	name        string
	kind        queueType
	priority    uint8
	prioritySet bool
	deadLetter  bool
	retryDelay  time.Duration
	maxAttempts uint32
}

// Queue creates a durable Classic queue.
func Queue(name string) QueueSpec { return QueueSpec{name: name, kind: classic} }

// QuorumQueue selects RabbitMQ's durable Quorum queue type.
func (q QueueSpec) QuorumQueue() QueueSpec { q.kind = quorum; return q }

// MaxPriority enables native Classic priority from zero through max.
func (q QueueSpec) MaxPriority(max uint8) QueueSpec { q.priority = max; q.prioritySet = true; return q }

// DeadLetter enables a managed dead-letter queue.
func (q QueueSpec) DeadLetter() QueueSpec { q.deadLetter = true; return q }

// WithRetry enables bounded transport retry and requires DeadLetter.
func (q QueueSpec) WithRetry(delay time.Duration, maxAttempts uint32) QueueSpec {
	q.retryDelay = delay
	q.maxAttempts = maxAttempts
	return q
}
func (q QueueSpec) validate() error {
	if len(q.name) < 1 || len(q.name) > 255 || strings.HasPrefix(q.name, "amq.") || strings.HasSuffix(q.name, "_dlq") || strings.HasSuffix(q.name, "_retry") {
		return fmt.Errorf("%w: invalid or reserved name", ErrInvalidQueue)
	}
	if q.kind == "" {
		q.kind = classic
	}
	if q.kind != classic && q.kind != quorum {
		return fmt.Errorf("%w: unknown queue type", ErrInvalidQueue)
	}
	if q.prioritySet && (q.kind == quorum || q.priority < 1 || q.priority > 10) {
		return fmt.Errorf("%w: max_priority must be 1..10 on Classic queues", ErrInvalidPriority)
	}
	if (q.retryDelay != 0 || q.maxAttempts != 0) && (!q.deadLetter || q.retryDelay < time.Millisecond || q.maxAttempts < 1 || q.maxAttempts > 1<<31-1) {
		return fmt.Errorf("%w: retry requires dead_letter, positive delay and max_attempts", ErrInvalidQueue)
	}
	return nil
}
func equalQueue(a, b QueueSpec) bool {
	return a.name == b.name && a.kind == b.kind && a.priority == b.priority && a.prioritySet == b.prioritySet && a.deadLetter == b.deadLetter && a.retryDelay == b.retryDelay && a.maxAttempts == b.maxAttempts
}
