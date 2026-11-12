package sse

import "errors"

var (
	// ErrInvalidEvent is returned for an event whose ID or name has control
	// characters or an invalid shape, or whose data cannot be encoded.
	ErrInvalidEvent = errors.New("sse: invalid event")
	// ErrInvalidUser is returned for an empty, oversized or malformed user ID.
	ErrInvalidUser = errors.New("sse: invalid user id")
	// ErrPayloadTooLarge is returned when the event data or frame exceeds the
	// configured limits.
	ErrPayloadTooLarge = errors.New("sse: payload too large")
	// ErrClosed is returned after the manager started shutting down, and for a
	// publication before the module started.
	ErrClosed = errors.New("sse: closed")
	// ErrTransportUnavailable is returned when the distribution driver cannot
	// accept the event now (for rabbitmq: the broker is unreachable). It is not
	// the same as "nobody is connected", which is a successful publication.
	ErrTransportUnavailable = errors.New("sse: transport unavailable")
	// ErrPublishUncertain is returned when the broker did not confirm the
	// publication in time: the event may or may not have been distributed.
	ErrPublishUncertain = errors.New("sse: publish outcome uncertain")
	// ErrUnauthorized is what an IdentityResolver returns (or wraps) when the
	// request has no valid identity: the stream answers 401.
	ErrUnauthorized = errors.New("sse: unauthorized")
	// ErrForbidden is what an IdentityResolver returns (or wraps) for an
	// identity that must not open a stream: the stream answers 403.
	ErrForbidden = errors.New("sse: forbidden")
)
