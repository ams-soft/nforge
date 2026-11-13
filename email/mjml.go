package email

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/julez-dev/mjmlgo/v2"
)

// Limits of RenderMJML.
const (
	MaxMJMLSourceBytes = 1 << 20 // the MJML document
	MaxMJMLOutputBytes = 4 << 20 // the rendered HTML
)

// Errors of RenderMJML.
var (
	// ErrMJMLTooLarge is returned for a source or a result over the limits.
	ErrMJMLTooLarge = errors.New("email: mjml document too large")
	// ErrMJML wraps a document the renderer rejects (malformed markup, an
	// unknown element, a bad attribute, an mj-include).
	ErrMJML = errors.New("email: invalid mjml")
)

var (
	rendererOnce sync.Once
	renderer     *mjmlgo.Renderer
)

// RenderMJML renders one MJML document to HTML. It is independent of the email
// module: no configuration, SMTP connection or network is involved, and it is
// safe for concurrent use.
//
// The source is the whole document. mj-include is not resolved (a document with
// one is an error): nothing reads the filesystem or the network. There is no
// variable substitution or template storage: the caller builds the MJML, and
// escaping an untrusted value placed into it is the caller's responsibility.
//
// The renderer is mjmlgo, a pure-Go implementation of MJML 5 (not the
// reference JavaScript compiler; parity is not guaranteed for every feature).
// The source is limited to MaxMJMLSourceBytes and the HTML to MaxMJMLOutputBytes.
// Rendering is CPU work that cannot be interrupted midway: when ctx ends the
// call returns its error at once and the already running render finishes in the
// background, bounded by the source limit.
func RenderMJML(ctx context.Context, source string) (string, error) {
	if len(source) > MaxMJMLSourceBytes {
		return "", fmt.Errorf("%w: source is larger than %d bytes", ErrMJMLTooLarge, MaxMJMLSourceBytes)
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	rendererOnce.Do(func() { renderer = mjmlgo.NewRenderer(mjmlgo.WithComments(false)) })
	type result struct {
		html string
		err  error
	}
	ch := make(chan result, 1)
	go func() {
		defer func() {
			if r := recover(); r != nil {
				ch <- result{err: fmt.Errorf("%w: renderer failure", ErrMJML)}
			}
		}()
		var out bytes.Buffer
		if err := renderer.Render(ctx, &limitWriter{w: &out, left: MaxMJMLOutputBytes}, []byte(source)); err != nil {
			ch <- result{err: err}
			return
		}
		ch <- result{html: out.String()}
	}()
	select {
	case <-ctx.Done():
		return "", ctx.Err()
	case r := <-ch:
		switch {
		case r.err == nil:
			return r.html, nil
		case errors.Is(r.err, errMJMLOutput):
			return "", fmt.Errorf("%w: result is larger than %d bytes", ErrMJMLTooLarge, MaxMJMLOutputBytes)
		case errors.Is(r.err, context.Canceled), errors.Is(r.err, context.DeadlineExceeded):
			return "", r.err
		default:
			return "", fmt.Errorf("%w: %w", ErrMJML, r.err)
		}
	}
}

var errMJMLOutput = errors.New("output limit")

// limitWriter refuses output past its allowance.
type limitWriter struct {
	w    *bytes.Buffer
	left int
}

func (l *limitWriter) Write(p []byte) (int, error) {
	if len(p) > l.left {
		return 0, errMJMLOutput
	}
	l.left -= len(p)
	return l.w.Write(p)
}
