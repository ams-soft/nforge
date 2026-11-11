package message

import "errors"

var (
	ErrDisabled             = errors.New("message: disabled")
	ErrNotStarted           = errors.New("message: not started")
	ErrStarted              = errors.New("message: already started")
	ErrClosed               = errors.New("message: closed")
	ErrQueueNotRegistered   = errors.New("message: queue not registered")
	ErrInvalidQueue         = errors.New("message: invalid queue")
	ErrQueueConflict        = errors.New("message: queue definition conflict")
	ErrUnsupportedFeature   = errors.New("message: unsupported feature")
	ErrInvalidPriority      = errors.New("message: invalid priority")
	ErrInvalidHeader        = errors.New("message: invalid header")
	ErrPayloadTooLarge      = errors.New("message: payload too large")
	ErrTransportUnavailable = errors.New("message: transport unavailable")
	ErrPublishUncertain     = errors.New("message: publish outcome uncertain")
	ErrUnroutable           = errors.New("message: message is unroutable")
	ErrNacked               = errors.New("message: broker negatively acknowledged publish")
	ErrAlreadySettled       = errors.New("message: delivery already settled")
	ErrStaleDelivery        = errors.New("message: delivery belongs to an old connection")
	ErrStatsUnavailable     = errors.New("message: stats unavailable")
	ErrInvalidLimit         = errors.New("message: invalid limit")
	ErrReprocessRunning     = errors.New("message: reprocessing already running")
)

// Error contains sanitized operation context. It never includes broker text or connection credentials.
type Error struct {
	Op, Queue, Reason string
	Permanent         bool
}

func (e *Error) Error() string {
	if e.Queue != "" {
		return "message: " + e.Op + " queue " + e.Queue + ": " + e.Reason
	}
	return "message: " + e.Op + ": " + e.Reason
}
