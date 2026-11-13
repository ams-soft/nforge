// Package email is nForge's email infrastructure: a synchronous SMTP client
// published as a module capability, and an independent MJML renderer.
//
// # Sending
//
//	client := module.Require(scope, email.Capability) // in a module that declared Requires
//	err := client.Send(ctx, email.Message{To: "customer@example.com", Subject: "Welcome", HTML: html})
//
// Send returns nil only when the SMTP server accepted the message (a positive
// answer to the end of DATA). That is not delivery: bounces, greylisting and spam
// filtering happen later and are not observable. It is synchronous, there is no
// queue, retry, attachment or tracking. A failed or ambiguous send is never
// repeated by the framework: ErrOutcomeUnknown means the message may have been
// accepted, and the caller owns the decision to resend.
//
// Sends are allowed only once the application started (not in Init); before
// that, and after the shutdown began, Send returns ErrClosed.
//
// # Message
//
// To is exactly one bare ASCII mailbox (no display name; SMTPUTF8 is not
// implemented, so a non-ASCII address is refused, use punycode for the domain).
// Subject is required and may be any Unicode text without control characters
// (RFC 2047). Text and HTML: at least one; both give multipart/alternative with
// the text first. Bodies are UTF-8, quoted-printable, with CRLF line ends.
// Limits: MaxSubjectBytes, MaxPartBytes per body and MaxMessageBytes for the
// encoded message. The sender comes from email.from; it is never per message.
//
// # SMTP transport
//
// One connection per send, bounded as a whole by email.timeout and the caller's
// context (the shorter wins), including dial, TLS, AUTH, the envelope and DATA.
// security: starttls requires the server to offer STARTTLS and upgrades with a
// verified certificate before AUTH or any message data; security: tls is
// implicit TLS. The minimum is TLS 1.2, the certificate is always verified
// against the configured host, and there is no plaintext or skip-verify mode.
// AUTH PLAIN is used only when a username and password are configured, and only
// over TLS; with none, no AUTH is attempted (a relay trusted by network).
//
// # Errors
//
// Errors wrap ErrInvalidMessage, ErrClosed, ErrUnavailable, ErrTransient,
// ErrRejected or ErrOutcomeUnknown, and the context error when it ended the
// operation. *Error carries the phase and the SMTP code. They never contain the
// message, the recipient, a credential or the server's text.
//
// # MJML
//
// RenderMJML turns an MJML document into HTML with no module, SMTP or network. It
// does not substitute variables: the caller builds the MJML, and escaping an
// untrusted value placed into it is the caller's responsibility. mj-include is
// not resolved. See RenderMJML for the engine, limits and caveats.
//
// # Drivers
//
// The transport sits behind an unexported driver interface selected by
// email.driver; only "smtp" exists. A future provider adds its own section
// (email.ses, email.sendpulse) and a case in newDriver without changing Message.
package email
