package notify

import (
	"bytes"
	"context"
	_ "embed"
	"errors"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/wneessen/go-mail"

	"discodrive/internal/db"
	"discodrive/internal/secret"
)

// logoPNG is the brand logo embedded in every HTML email via a CID reference (cid:logo),
// so it renders without depending on an externally hosted URL.
//
//go:embed logo.png
var logoPNG []byte

// emailStore is a narrow slice of db.Queries for reading SMTP settings.
type emailStore interface {
	GetSetting(ctx context.Context, key string) (db.Setting, error)
}

// EmailChannel sends email over SMTP. Config is read from settings on every send.
type EmailChannel struct {
	q      emailStore
	cipher *secret.Cipher
}

func NewEmailChannel(q *db.Queries, cipher *secret.Cipher) *EmailChannel {
	return &EmailChannel{q: q, cipher: cipher}
}

func (e *EmailChannel) Name() string { return "email" }

func (e *EmailChannel) get(ctx context.Context, key string) string {
	row, err := e.q.GetSetting(ctx, key)
	if errors.Is(err, pgx.ErrNoRows) || err != nil {
		return ""
	}
	return row.Value
}

func (e *EmailChannel) Send(ctx context.Context, m Message) error {
	host := e.get(ctx, "smtp.host")
	if host == "" {
		return errors.New("SMTP not configured")
	}
	port := 587
	if p := e.get(ctx, "smtp.port"); p != "" {
		if v, err := strconv.Atoi(p); err == nil && v > 0 {
			port = v
		}
	}
	from := e.get(ctx, "smtp.from")
	if from == "" {
		from = e.get(ctx, "smtp.username")
	}
	username := e.get(ctx, "smtp.username")

	var password string
	if row, err := e.q.GetSetting(ctx, "smtp.password"); err == nil && row.Value != "" {
		p, derr := e.cipher.Decrypt(row.Value)
		if derr != nil {
			return derr
		}
		password = p
	}

	opts := []mail.Option{mail.WithPort(port), mail.WithTimeout(15 * time.Second)}
	auth := mail.SMTPAuthPlain
	switch e.get(ctx, "smtp.security") {
	case "tls":
		// Implicit TLS (port 465 style) — WithSSL() sets useSSL=true.
		opts = append(opts, mail.WithSSL())
	case "none":
		opts = append(opts, mail.WithTLSPolicy(mail.NoTLS))
		// Plain PLAIN refuses to send credentials without TLS (except to localhost),
		// so a login on an unencrypted relay never worked. The admin chose "none"
		// explicitly: send them in the clear, as asked.
		auth = mail.SMTPAuthPlainNoEnc
	default: // "starttls" or unset
		opts = append(opts, mail.WithTLSPolicy(mail.TLSMandatory))
	}
	if username != "" {
		opts = append(opts, mail.WithSMTPAuth(auth), mail.WithUsername(username), mail.WithPassword(password))
	}
	client, err := mail.NewClient(host, opts...)
	if err != nil {
		return err
	}

	msg := mail.NewMsg()
	if err := msg.From(from); err != nil {
		return err
	}
	if err := msg.To(m.To); err != nil {
		return err
	}
	msg.Subject(m.Subject)
	msg.SetBodyString(mail.TypeTextPlain, m.Text)
	msg.AddAlternativeString(mail.TypeTextHTML, m.HTML)
	// Embed the brand logo so the HTML template's <img src="cid:logo"> renders inline.
	if err := msg.EmbedReader("logo.png", bytes.NewReader(logoPNG), mail.WithFileContentID("logo")); err != nil {
		return err
	}
	return client.DialAndSendWithContext(ctx, msg)
}
