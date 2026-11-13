package email

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/quotedprintable"
	"net/mail"
	"strings"
	"time"
	"unicode/utf8"
)

// Message is one email. Text and HTML are the alternative bodies of the same
// content: at least one is required, and with both the message is
// multipart/alternative (text first, HTML second). Attachments do not exist.
type Message struct {
	// To is exactly one bare ASCII mailbox such as "customer@example.com".
	To string
	// Subject is required; any Unicode text without control characters.
	Subject string
	// Text is the text/plain body.
	Text string
	// HTML is the text/html body. Escaping untrusted values in it is the caller's job.
	HTML string
}

// Input limits. They bound memory before anything is encoded or sent.
const (
	MaxSubjectBytes = 512
	MaxPartBytes    = 4 << 20  // Text and HTML, each
	MaxMessageBytes = 10 << 20 // the whole encoded MIME message
)

// ErrInvalidMessage is returned (wrapped) for a message that fails validation;
// nothing was sent.
var ErrInvalidMessage = errors.New("email: invalid message")

func invalid(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalidMessage, fmt.Sprintf(format, args...))
}

// envelope is a validated message ready for a driver: the SMTP envelope and
// the complete RFC 5322 message with CRLF line ends (not yet dot-stuffed).
type envelope struct {
	from string
	to   string
	data []byte
}

func (m Message) validate() error {
	if err := checkAddress(m.To); err != nil {
		return invalid("to: %v", err)
	}
	if m.Subject == "" {
		return invalid("subject is required")
	}
	if len(m.Subject) > MaxSubjectBytes {
		return invalid("subject is longer than %d bytes", MaxSubjectBytes)
	}
	if !utf8.ValidString(m.Subject) || hasControl(m.Subject) {
		return invalid("subject must be valid UTF-8 without control characters")
	}
	if m.Text == "" && m.HTML == "" {
		return invalid("text or html is required")
	}
	for name, body := range map[string]string{"text": m.Text, "html": m.HTML} {
		if len(body) > MaxPartBytes {
			return invalid("%s is longer than %d bytes", name, MaxPartBytes)
		}
		if !utf8.ValidString(body) || strings.ContainsRune(body, 0) {
			return invalid("%s must be valid UTF-8 without NUL", name)
		}
	}
	return nil
}

// build validates the message and encodes it from the configured sender.
func build(from FromConfig, m Message, now time.Time) (envelope, error) {
	if err := m.validate(); err != nil {
		return envelope{}, err
	}
	boundary := ""
	var b bytes.Buffer
	hdr := func(k, v string) { b.WriteString(k + ": " + v + "\r\n") }

	sender := mail.Address{Name: from.Name, Address: from.Address}
	hdr("From", sender.String())
	hdr("To", m.To)
	hdr("Subject", mime.QEncoding.Encode("utf-8", m.Subject))
	hdr("Date", now.UTC().Format(time.RFC1123Z))
	id, err := token(16)
	if err != nil {
		return envelope{}, fmt.Errorf("email: generate message id: %w", err)
	}
	_, domain, _ := strings.Cut(from.Address, "@")
	hdr("Message-ID", "<"+id+"@"+domain+">")
	hdr("MIME-Version", "1.0")

	switch {
	case m.Text != "" && m.HTML != "":
		bt, err := token(24)
		if err != nil {
			return envelope{}, fmt.Errorf("email: generate boundary: %w", err)
		}
		boundary = "nforge-" + bt
		hdr("Content-Type", `multipart/alternative; boundary="`+boundary+`"`)
		b.WriteString("\r\n")
		for _, p := range []struct{ typ, body string }{{"text/plain", m.Text}, {"text/html", m.HTML}} {
			b.WriteString("--" + boundary + "\r\n")
			if err := writePart(&b, p.typ, p.body); err != nil {
				return envelope{}, err
			}
			if strings.Contains(p.body, boundary) {
				return envelope{}, errors.New("email: boundary collision") // practically impossible: 192 random bits
			}
		}
		b.WriteString("--" + boundary + "--\r\n")
	case m.HTML != "":
		if err := writePart(&b, "text/html", m.HTML); err != nil {
			return envelope{}, err
		}
	default:
		if err := writePart(&b, "text/plain", m.Text); err != nil {
			return envelope{}, err
		}
	}
	if b.Len() > MaxMessageBytes {
		return envelope{}, invalid("encoded message is larger than %d bytes", MaxMessageBytes)
	}
	return envelope{from: from.Address, to: m.To, data: b.Bytes()}, nil
}

// writePart writes the part headers, a blank line and the body encoded as
// quoted-printable with CRLF line ends.
func writePart(b *bytes.Buffer, typ, body string) error {
	b.WriteString("Content-Type: " + typ + "; charset=utf-8\r\n")
	b.WriteString("Content-Transfer-Encoding: quoted-printable\r\n\r\n")
	body = strings.ReplaceAll(strings.ReplaceAll(body, "\r\n", "\n"), "\r", "\n")
	body = strings.ReplaceAll(body, "\n", "\r\n")
	w := quotedprintable.NewWriter(b)
	if _, err := io.WriteString(w, body); err != nil {
		return fmt.Errorf("email: encode body: %w", err)
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("email: encode body: %w", err)
	}
	if !bytes.HasSuffix(b.Bytes(), []byte("\r\n")) {
		b.WriteString("\r\n")
	}
	return nil
}

func token(n int) (string, error) {
	raw := make([]byte, n)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return hex.EncodeToString(raw), nil
}
