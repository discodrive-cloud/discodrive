package appleprofile

import (
	"encoding/xml"
	"io"
	"strings"
	"testing"
	"time"
)

func TestProfile(t *testing.T) {
	o := Options{ServerURL: "https://dav.example:8443", UserID: "user-id", Email: "a&b@example.org", InstallationID: "phone-id", Calendars: true, Contacts: true}
	data, err := Build(o)
	if err != nil {
		t.Fatal(err)
	}
	decoder := xml.NewDecoder(strings.NewReader(string(data)))
	for {
		_, err = decoder.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	s := string(data)
	for _, want := range []string{"com.apple.caldav.account", "com.apple.carddav.account", "<integer>8443</integer>", "a&amp;b@example.org", "https://dav.example:8443/caldav/user-id/"} {
		if !strings.Contains(s, want) {
			t.Fatalf("missing %s", want)
		}
	}
	if strings.Contains(s, "Password") {
		t.Fatal("unencrypted profile must not contain passwords")
	}
	again, _ := Build(o)
	if string(again) != s {
		t.Fatal("profile identifiers are unstable")
	}
	o.Contacts = false
	one, _ := Build(o)
	if strings.Contains(string(one), "com.apple.carddav.account") {
		t.Fatal("unselected contacts included")
	}
	o.ServerURL = "http://dav.example"
	if _, err = Build(o); err == nil {
		t.Fatal("HTTP accepted")
	}
}
func TestTicketBoundExpiryAndRetry(t *testing.T) {
	var tickets Tickets
	key, err := tickets.Put("owner", []byte("profile"))
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		data, ok := tickets.Get(key)
		if !ok || string(data) != "profile" {
			t.Fatal("retry failed")
		}
		data[0] = 'x'
	}
	for i := 0; i < 4; i++ {
		if _, err := tickets.Put("owner", nil); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := tickets.Put("owner", nil); err == nil {
		t.Fatal("owner limit ignored")
	}
	entry := tickets.entries[key]
	entry.until = time.Now().Add(-time.Second)
	tickets.entries[key] = entry
	if _, ok := tickets.Get(key); ok {
		t.Fatal("expired ticket served")
	}
}
