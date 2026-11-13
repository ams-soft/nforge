package email

import (
	"errors"
	"io"
	"mime"
	"mime/multipart"
	"mime/quotedprintable"
	"net/mail"
	"strings"
	"testing"
	"time"
)

var testFrom = FromConfig{Address: "no-reply@example.com", Name: "Exemplo São Paulo"}

func mustBuild(t *testing.T, m Message) (envelope, *mail.Message) {
	t.Helper()
	e, err := build(testFrom, m, time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC))
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if strings.Contains(strings.ReplaceAll(string(e.data), "\r\n", ""), "\n") || strings.Contains(strings.ReplaceAll(string(e.data), "\r\n", ""), "\r") {
		t.Fatal("bare CR or LF in the wire message")
	}
	msg, err := mail.ReadMessage(strings.NewReader(string(e.data)))
	if err != nil {
		t.Fatalf("ReadMessage: %v", err)
	}
	return e, msg
}

func decodeQP(t *testing.T, r io.Reader) string {
	t.Helper()
	b, err := io.ReadAll(quotedprintable.NewReader(r))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestTextOnly(t *testing.T) {
	e, msg := mustBuild(t, Message{To: "a@example.org", Subject: "Hi", Text: "line1\nline2\r\n.dot"})
	if e.from != "no-reply@example.com" || e.to != "a@example.org" {
		t.Fatalf("envelope = %+v", e)
	}
	if ct := msg.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/plain") {
		t.Fatalf("content type %q", ct)
	}
	if got := decodeQP(t, msg.Body); got != "line1\r\nline2\r\n.dot\r\n" {
		t.Fatalf("body %q", got)
	}
	if msg.Header.Get("MIME-Version") != "1.0" || msg.Header.Get("Message-Id") == "" || msg.Header.Get("Date") == "" {
		t.Fatalf("headers %v", msg.Header)
	}
}

func TestHTMLOnly(t *testing.T) {
	_, msg := mustBuild(t, Message{To: "a@example.org", Subject: "Hi", HTML: "<p>olá</p>"})
	if ct := msg.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Fatalf("content type %q", ct)
	}
	if got := decodeQP(t, msg.Body); got != "<p>olá</p>\r\n" {
		t.Fatalf("body %q", got)
	}
}

func TestMultipartAlternativeOrder(t *testing.T) {
	e, msg := mustBuild(t, Message{To: "a@example.org", Subject: "Hi", Text: "plain ✓", HTML: "<b>html ✓</b>"})
	if strings.Count(string(e.data), "Content-Transfer-Encoding: quoted-printable\r\n") != 2 {
		t.Fatal("both parts must declare quoted-printable")
	}
	mt, params, err := mime.ParseMediaType(msg.Header.Get("Content-Type"))
	if err != nil || mt != "multipart/alternative" || params["boundary"] == "" {
		t.Fatalf("content type %q %v %v", mt, params, err)
	}
	mr := multipart.NewReader(msg.Body, params["boundary"])
	var types, bodies []string
	for {
		p, err := mr.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		types = append(types, p.Header.Get("Content-Type"))
		b, _ := io.ReadAll(p) // multipart.Reader already decodes quoted-printable
		bodies = append(bodies, string(b))
	}
	if len(types) != 2 || !strings.HasPrefix(types[0], "text/plain") || !strings.HasPrefix(types[1], "text/html") {
		t.Fatalf("parts %v", types)
	}
	if strings.TrimRight(bodies[0], "\r\n") != "plain ✓" || strings.TrimRight(bodies[1], "\r\n") != "<b>html ✓</b>" {
		t.Fatalf("bodies %q", bodies)
	}
}

func TestBoundaryIsUnique(t *testing.T) {
	a, _ := mustBuild(t, Message{To: "a@example.org", Subject: "s", Text: "t", HTML: "h"})
	b, _ := mustBuild(t, Message{To: "a@example.org", Subject: "s", Text: "t", HTML: "h"})
	if string(a.data) == string(b.data) {
		t.Fatal("two builds are identical (boundary or message id repeated)")
	}
}

func TestUnicodeHeaders(t *testing.T) {
	_, msg := mustBuild(t, Message{To: "a@example.org", Subject: "Olá, mundo ✓ " + strings.Repeat("ação ", 30), Text: "x"})
	dec := new(mime.WordDecoder)
	subj, err := dec.DecodeHeader(msg.Header.Get("Subject"))
	if err != nil || !strings.HasPrefix(subj, "Olá, mundo ✓ ação") {
		t.Fatalf("subject %q %v", subj, err)
	}
	from, err := msg.Header.AddressList("From")
	if err != nil || from[0].Name != "Exemplo São Paulo" || from[0].Address != "no-reply@example.com" {
		t.Fatalf("from %+v %v", from, err)
	}
}

func TestInvalidMessages(t *testing.T) {
	ok := Message{To: "a@example.org", Subject: "s", Text: "t"}
	long := func(n int) string { return strings.Repeat("a", n) }
	cases := map[string]Message{
		"empty to":         {Subject: "s", Text: "t"},
		"display name":     {To: "Ann <a@example.org>", Subject: "s", Text: "t"},
		"two recipients":   {To: "a@example.org,b@example.org", Subject: "s", Text: "t"},
		"header injection": {To: "a@example.org\r\nBcc: x@example.org", Subject: "s", Text: "t"},
		"space":            {To: "a@example.org ", Subject: "s", Text: "t"},
		"unicode local":    {To: "josé@example.org", Subject: "s", Text: "t"},
		"no domain":        {To: "a", Subject: "s", Text: "t"},
		"empty subject":    {To: ok.To, Text: "t"},
		"subject CRLF":     {To: ok.To, Subject: "a\r\nBcc: x@example.org", Text: "t"},
		"subject LF":       {To: ok.To, Subject: "a\nb", Text: "t"},
		"subject NUL":      {To: ok.To, Subject: "a\x00b", Text: "t"},
		"subject long":     {To: ok.To, Subject: long(MaxSubjectBytes + 1), Text: "t"},
		"subject utf8":     {To: ok.To, Subject: "a\xffb", Text: "t"},
		"no body":          {To: ok.To, Subject: "s"},
		"text NUL":         {To: ok.To, Subject: "s", Text: "a\x00"},
		"text big":         {To: ok.To, Subject: "s", Text: long(MaxPartBytes + 1)},
		"html big":         {To: ok.To, Subject: "s", HTML: long(MaxPartBytes + 1)},
		"html utf8":        {To: ok.To, Subject: "s", HTML: "\xff"},
	}
	for name, m := range cases {
		if _, err := build(testFrom, m, time.Now()); !errors.Is(err, ErrInvalidMessage) {
			t.Errorf("%s: err = %v, want ErrInvalidMessage", name, err)
		}
	}
}

func TestEncodedSizeLimit(t *testing.T) {
	// Non-ASCII text grows 3x under quoted-printable: two parts under the part
	// limit can still exceed the message limit.
	big := strings.Repeat("é", MaxPartBytes/2-1)
	_, err := build(testFrom, Message{To: "a@example.org", Subject: "s", Text: big, HTML: big}, time.Now())
	if !errors.Is(err, ErrInvalidMessage) {
		t.Fatalf("err = %v", err)
	}
}

func TestSenderNameIsEncodedNotInjected(t *testing.T) {
	from := FromConfig{Address: "a@example.com", Name: `Evil" <x@y>`}
	e, err := build(from, Message{To: "b@example.org", Subject: "s", Text: "t"}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	msg, _ := mail.ReadMessage(strings.NewReader(string(e.data)))
	l, err := msg.Header.AddressList("From")
	if err != nil || len(l) != 1 || l[0].Address != "a@example.com" {
		t.Fatalf("from %v %v", l, err)
	}
}

func FuzzBuild(f *testing.F) {
	f.Add("a@example.org", "subject", "text", "<p>html</p>")
	f.Add("a@example.org\r\n", "x\r\nBcc: y", "\r.\r\n.", "")
	f.Fuzz(func(t *testing.T, to, subject, text, html string) {
		e, err := build(testFrom, Message{To: to, Subject: subject, Text: text, HTML: html}, time.Unix(0, 0))
		if err != nil {
			return
		}
		msg, err := mail.ReadMessage(strings.NewReader(string(e.data)))
		if err != nil {
			t.Fatalf("unparseable message: %v", err)
		}
		if len(msg.Header["Bcc"]) > 0 || len(msg.Header["From"]) != 1 || len(msg.Header["To"]) != 1 || msg.Header.Get("To") != to {
			t.Fatalf("header injection or mangled headers: %v", msg.Header)
		}
	})
}
