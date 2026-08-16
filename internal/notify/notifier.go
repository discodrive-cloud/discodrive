package notify

import (
	"bytes"
	"context"
	htmltmpl "html/template"
	"log"
	texttmpl "text/template"

	"github.com/jackc/pgx/v5/pgtype"

	"discodrive/internal/db"
)

// Message is a fully rendered message ready to send via a channel.
type Message struct {
	To      string
	Subject string
	HTML    string
	Text    string
}

// Channel is a delivery transport (email now; push/telegram later).
type Channel interface {
	Name() string
	Send(ctx context.Context, m Message) error
}

// store is a narrow slice of db.Queries used by Notifier (simplifies unit tests with a fake).
type store interface {
	GetUserByID(ctx context.Context, id pgtype.UUID) (db.User, error)
	GetSetting(ctx context.Context, key string) (db.Setting, error)
	NotificationPrefsForEvent(ctx context.Context, arg db.NotificationPrefsForEventParams) ([]db.NotificationPrefsForEventRow, error)
}

// Notifier resolves event → recipient/channels/prefs, assembles the message, and sends asynchronously.
type Notifier struct {
	q        store
	channels map[string]Channel
}

// New constructs a Notifier with an email channel.
func New(q *db.Queries, email Channel) *Notifier {
	return &Notifier{q: q, channels: map[string]Channel{email.Name(): email}}
}

// projectURL is the public project link shown in the footer of every email.
const projectURL = "https://discodrive.app"

// settingEnabled reads a boolean setting (defaults to def if missing; "false" → false).
func (n *Notifier) settingEnabled(ctx context.Context, key string, def bool) bool {
	row, err := n.q.GetSetting(ctx, key)
	if err != nil {
		return def
	}
	return row.Value != "false"
}

// Emit sends a notification for eventKey to userID. Best-effort: errors are logged, not returned.
// A nil Notifier is a no-op (notifications simply not configured).
func (n *Notifier) Emit(ctx context.Context, userID, eventKey string, data map[string]any) {
	if n == nil {
		return
	}
	ev, ok := Catalog[eventKey]
	if !ok {
		log.Printf("notify: unknown event %q", eventKey)
		return
	}
	if !ev.Mandatory && !n.settingEnabled(ctx, "notifications.enabled", true) {
		return
	}
	uid, err := db.ParseUUID(userID)
	if err != nil {
		log.Printf("notify: bad userID %q: %v", userID, err)
		return
	}
	user, err := n.q.GetUserByID(ctx, uid)
	if err != nil {
		log.Printf("notify: user %s not found: %v", userID, err)
		return
	}

	channels := n.resolveChannels(ctx, uid, ev)
	if len(channels) == 0 {
		return
	}

	// Render in the recipient's language (users.language), falling back to the default.
	tpl, ok := ev.Templates[user.Language]
	if !ok {
		tpl, ok = ev.Templates[DefaultLang]
	}
	if !ok {
		log.Printf("notify: no template for %q/%s", eventKey, user.Language)
		return
	}
	subject := renderText(tpl.Subject, data)
	contentHTML := renderHTML(tpl.HTML, data)
	contentText := renderText(tpl.Text, data)
	fullHTML, err := renderLayout(contentHTML)
	if err != nil {
		log.Printf("notify: email layout: %v", err)
		return
	}
	fullText := contentText + "\n\n" + projectURL

	msg := Message{To: user.Email, Subject: subject, HTML: fullHTML, Text: fullText}
	// Delivery is asynchronous and must outlive the HTTP request: callers pass a
	// request-scoped ctx that is cancelled when the response is sent.
	// Detach cancellation (preserving values); the channel owns its own deadline.
	sendCtx := context.WithoutCancel(ctx)
	for _, ch := range channels {
		ch := ch
		go func() {
			defer func() {
				if rec := recover(); rec != nil {
					log.Printf("notify: panic in channel %s: %v", ch.Name(), rec)
				}
			}()
			if err := ch.Send(sendCtx, msg); err != nil {
				log.Printf("notify: channel %s, event %s: %v", ch.Name(), eventKey, err)
			}
		}()
	}
}

// resolveChannels: for mandatory events uses all default channels; otherwise uses channels enabled in prefs (no row = enabled).
func (n *Notifier) resolveChannels(ctx context.Context, uid pgtype.UUID, ev Event) []Channel {
	var out []Channel
	var prefs map[string]bool
	if !ev.Mandatory {
		prefs = map[string]bool{}
		rows, err := n.q.NotificationPrefsForEvent(ctx, db.NotificationPrefsForEventParams{UserID: uid, EventKey: ev.Key})
		if err == nil {
			for _, r := range rows {
				prefs[r.Channel] = r.Enabled
			}
		}
	}
	for _, name := range ev.DefaultChannels {
		ch, ok := n.channels[name]
		if !ok {
			continue
		}
		if !ev.Mandatory {
			if en, has := prefs[name]; has && !en {
				continue // user disabled this channel
			}
		}
		out = append(out, ch)
	}
	return out
}

// renderText renders subject/body text via text/template (no escaping).
func renderText(tmpl string, data map[string]any) string {
	t, err := texttmpl.New("t").Parse(tmpl)
	if err != nil {
		return tmpl
	}
	var buf bytes.Buffer
	if err := t.Execute(&buf, data); err != nil {
		return tmpl
	}
	return buf.String()
}

// renderHTML renders an HTML block via html/template (event data is escaped).
func renderHTML(tmpl string, data map[string]any) string {
	t, err := htmltmpl.New("t").Parse(tmpl)
	if err != nil {
		return tmpl
	}
	var buf bytes.Buffer
	if err := t.Execute(&buf, data); err != nil {
		return tmpl
	}
	return buf.String()
}
