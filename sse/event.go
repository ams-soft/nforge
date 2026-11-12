package sse

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"
	"unicode/utf8"
)

// Event is one Server-Sent Event for one user.
type Event struct {
	// ID is the SSE "id:" field. When empty the manager generates one per
	// event; an ID does not imply replay support.
	ID string
	// Name is the SSE "event:" field (letters, digits and . _ : -). Empty
	// leaves the default "message" event.
	Name string
	// Data is the payload. A string or []byte is sent as text; anything else
	// is encoded as JSON with the application's codec.
	Data any
}

// PublishResult reports the outcome of a publication.
type PublishResult struct {
	// Accepted means the distribution driver took the event: for memory, local
	// dispatch ran; for rabbitmq, the broker confirmed the publication. It does
	// NOT mean any browser received it, nor that a user was connected.
	Accepted bool
}

// wire is the transport form of an event, identical for both drivers: the data
// is already text, so every instance writes the same frame.
type wire struct {
	Version int    `json:"v"`
	User    string `json:"user" codec:"required"`
	ID      string `json:"id,omitempty"`
	Name    string `json:"name,omitempty"`
	Data    string `json:"data"`
}

const wireVersion = 1

// validControl reports whether s has no control characters, NUL or invalid UTF-8.
func validText(s string) bool {
	if !utf8.ValidString(s) {
		return false
	}
	for _, r := range s {
		if r < 0x20 || r == 0x7f || (r >= 0x80 && r < 0xa0) || r == 0x2028 || r == 0x2029 {
			return false
		}
	}
	return true
}

func validEventName(s string, max int) bool {
	if s == "" || len(s) > max {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c < 'a' || c > 'z') && (c < 'A' || c > 'Z') && (c < '0' || c > '9') && c != '.' && c != '_' && c != ':' && c != '-' {
			return false
		}
	}
	return true
}

func (m *Manager) validUser(id string) error {
	if id == "" || len(id) > m.cfg.Limits.MaxUserIDBytes || !validText(id) {
		return ErrInvalidUser
	}
	return nil
}

// validate checks the fields that reach the frame.
func (m *Manager) validateWire(w wire) error {
	if err := m.validUser(w.User); err != nil {
		return err
	}
	if len(w.ID) > m.cfg.Limits.MaxEventIDBytes || !validText(w.ID) {
		return fmt.Errorf("%w: id", ErrInvalidEvent)
	}
	if w.Name != "" && !validEventName(w.Name, m.cfg.Limits.MaxEventNameSize) {
		return fmt.Errorf("%w: name", ErrInvalidEvent)
	}
	if int64(len(w.Data)) > int64(m.cfg.Limits.MaxPayloadBytes) {
		return ErrPayloadTooLarge
	}
	if !utf8.ValidString(w.Data) {
		return fmt.Errorf("%w: data is not valid UTF-8", ErrInvalidEvent)
	}
	return nil
}

// newWire validates and normalizes an event of a publisher.
func (m *Manager) newWire(user string, e Event) (wire, error) {
	w := wire{Version: wireVersion, User: user, ID: e.ID, Name: e.Name}
	switch d := e.Data.(type) {
	case nil:
	case string:
		w.Data = d
	case []byte:
		w.Data = string(d)
	default:
		b, err := m.codec.EncodeAs("json", d)
		if err != nil {
			return wire{}, fmt.Errorf("%w: data cannot be encoded", ErrInvalidEvent)
		}
		w.Data = string(b)
	}
	if err := m.validateWire(w); err != nil {
		return wire{}, err
	}
	if w.ID == "" {
		var b [12]byte
		if _, err := rand.Read(b[:]); err != nil {
			return wire{}, fmt.Errorf("%w: id generation failed", ErrInvalidEvent)
		}
		w.ID = hex.EncodeToString(b[:])
	}
	return w, nil
}

// frame renders w as one SSE frame: id, event, one data line per payload line
// (CR, LF and CRLF all end a line for a browser), and the blank line.
func (m *Manager) frame(w wire) ([]byte, error) {
	var b strings.Builder
	b.Grow(len(w.Data) + len(w.ID) + len(w.Name) + 32)
	b.WriteString("id: " + w.ID + "\n")
	if w.Name != "" {
		b.WriteString("event: " + w.Name + "\n")
	}
	data := strings.NewReplacer("\r\n", "\n", "\r", "\n").Replace(w.Data)
	for line := range strings.SplitSeq(data, "\n") {
		b.WriteString("data: " + line + "\n")
	}
	b.WriteString("\n")
	if int64(b.Len()) > int64(m.cfg.Limits.MaxPayloadBytes)+1024 {
		return nil, ErrPayloadTooLarge
	}
	return []byte(b.String()), nil
}
