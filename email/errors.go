package email

import (
	"errors"
	"fmt"
)

// Sentinels for errors.Is. A Send error also wraps context.Canceled or
// context.DeadlineExceeded when the context ended the operation.
var (
	// ErrClosed means the client is shut down (or shutting down) and refuses sends.
	ErrClosed = errors.New("email: client is closed")
	// ErrUnavailable means the server could not be reached or trusted (dial, TLS,
	// STARTTLS, AUTH); no message transaction was accepted. Nothing was delivered.
	ErrUnavailable = errors.New("email: server unavailable")
	// ErrTransient is a temporary (4xx) refusal. The message was not accepted;
	// the caller decides whether to try again later (nForge never retries).
	ErrTransient = errors.New("email: temporary rejection")
	// ErrRejected is a permanent (5xx) refusal. Repeating it will not help.
	ErrRejected = errors.New("email: permanent rejection")
	// ErrOutcomeUnknown means the connection or deadline failed after the message
	// body was sent and before the server answered: the message may or may not
	// have been accepted. Do not blindly resend; it can duplicate the mail.
	ErrOutcomeUnknown = errors.New("email: outcome unknown")
)

// Error describes one failed Send without echoing anything the server said, the
// message, the recipient or a credential.
type Error struct {
	// Phase is where it failed: connect, tls, starttls, auth, mail, rcpt, data,
	// body, quit is never reported (it follows acceptance).
	Phase string
	// Code is the SMTP reply code of a refusal, 0 otherwise.
	Code int
	kind error
	err  error
}

func (e *Error) Error() string {
	s := "email: send failed at " + e.Phase
	if e.Code != 0 {
		s += fmt.Sprintf(" (smtp %d)", e.Code)
	}
	if e.kind != nil {
		s += ": " + e.kind.Error()
	}
	return s
}

// Unwrap exposes the category sentinel and the context error, if any.
func (e *Error) Unwrap() []error {
	var out []error
	if e.kind != nil {
		out = append(out, e.kind)
	}
	if e.err != nil {
		out = append(out, e.err)
	}
	return out
}
