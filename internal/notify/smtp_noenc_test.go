package notify

import (
	"bufio"
	"context"
	"net"
	"strconv"
	"strings"
	"sync"
	"testing"

	"discodrive/internal/secret"
)

// fakeSMTP is a minimal plaintext SMTP server that offers AUTH PLAIN without
// STARTTLS and records what it received.
type fakeSMTP struct {
	ln   net.Listener
	mu   sync.Mutex
	auth string
	data string
}

func newFakeSMTP(t *testing.T) *fakeSMTP {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeSMTP{ln: ln}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go f.serve(c)
		}
	}()
	return f
}

func (f *fakeSMTP) serve(c net.Conn) {
	defer c.Close()
	r := bufio.NewReader(c)
	say := func(s string) { _, _ = c.Write([]byte(s + "\r\n")) }
	say("220 fake ESMTP")
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return
		}
		cmd := strings.ToUpper(strings.TrimSpace(line))
		switch {
		case strings.HasPrefix(cmd, "EHLO"), strings.HasPrefix(cmd, "HELO"):
			say("250-fake")
			say("250 AUTH PLAIN LOGIN")
		case strings.HasPrefix(cmd, "AUTH"):
			f.mu.Lock()
			f.auth = strings.TrimSpace(line)
			f.mu.Unlock()
			say("235 ok")
		case strings.HasPrefix(cmd, "MAIL"), strings.HasPrefix(cmd, "RCPT"), strings.HasPrefix(cmd, "RSET"), strings.HasPrefix(cmd, "NOOP"):
			say("250 ok")
		case cmd == "DATA":
			say("354 go")
			var b strings.Builder
			for {
				l, err := r.ReadString('\n')
				if err != nil {
					return
				}
				if l == ".\r\n" {
					break
				}
				b.WriteString(l)
			}
			f.mu.Lock()
			f.data = b.String()
			f.mu.Unlock()
			say("250 queued")
		case cmd == "QUIT":
			say("221 bye")
			return
		default:
			say("502 ?")
		}
	}
}

// security=none plus a username never sent anything: go-mail's PLAIN auth refuses
// to send credentials over an unencrypted connection to anything but localhost, so
// every notification to a LAN relay failed. With "none" chosen explicitly, the
// unencrypted variant must be used.
func TestEmailSecurityNoneWithLoginSends(t *testing.T) {
	if _, err := net.LookupHost("localhost."); err != nil {
		t.Skipf("resolver has no localhost.: %v", err)
	}
	srv := newFakeSMTP(t)
	port := strconv.Itoa(srv.ln.Addr().(*net.TCPAddr).Port)
	c, _ := secret.New("0123456789abcdef0123456789abcdef")
	encPass, _ := c.Encrypt("relay-pass")
	ch := &EmailChannel{cipher: c, q: stubSettings{values: map[string]string{
		// "localhost." reaches the fake server but is not the literal name go-mail
		// exempts, so it stands in for a relay elsewhere on the LAN.
		"smtp.host": "localhost.", "smtp.port": port, "smtp.security": "none",
		"smtp.username": "relay-user", "smtp.password": encPass, "smtp.from": "dd@example.test",
	}}}
	err := ch.Send(context.Background(), Message{To: "a@example.test", Subject: "s", Text: "hello", HTML: "<p>hello</p>"})
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	srv.mu.Lock()
	defer srv.mu.Unlock()
	if !strings.HasPrefix(strings.ToUpper(srv.auth), "AUTH PLAIN") || srv.data == "" {
		t.Fatalf("auth=%q data=%d bytes: login or message missing", srv.auth, len(srv.data))
	}
}
