package api

import (
	"context"
	"net/http"
	"testing"

	"discodrive/internal/db"
)

// Two people open the same task, event or contact. The second to save used to write
// their form over whatever the first had saved. A form now carries the etag it was
// loaded with, and a save over something that changed since is refused (412).

func TestTaskSaveOverSomebodyElsesChangeIsRefused(t *testing.T) {
	h := newCalHarness(t)
	rec, m := h.do(t, h.tokA, "POST", "/me/tasks", map[string]any{"summary": "buy milk"})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", rec.Code, rec.Body)
	}
	path := "/me/tasks/" + m["uid"].(string)

	_, mine := h.do(t, h.tokA, "GET", path, nil)
	_, theirs := h.do(t, h.tokA, "GET", path, nil)
	etag, _ := mine["etag"].(string)
	if etag == "" {
		t.Fatalf("a loaded task has no etag: %v", mine)
	}

	// The other tab marks it done and sets a due date.
	rec, saved := h.do(t, h.tokA, "PUT", path, map[string]any{
		"summary": "buy milk", "due": "2026-08-05T10:00:00Z", "completed": true, "etag": theirs["etag"]})
	if rec.Code != http.StatusOK {
		t.Fatalf("first save: %d %s", rec.Code, rec.Body)
	}
	if saved["etag"] == "" || saved["etag"] == etag {
		t.Fatalf("a save does not answer with the new etag: %v", saved)
	}
	// This tab renames it from what it loaded before that.
	rec, _ = h.do(t, h.tokA, "PUT", path, map[string]any{"summary": "buy oat milk", "etag": etag})
	if rec.Code != http.StatusPreconditionFailed {
		t.Fatalf("save over a newer version: %d, want 412", rec.Code)
	}
	_, now := h.do(t, h.tokA, "GET", path, nil)
	if now["summary"] != "buy milk" || now["completed"] != true || now["due"] == "" {
		t.Fatalf("the refused save changed the task: %v", now)
	}
	// Loaded again, the rename goes through and keeps the other tab's changes it shows.
	rec, _ = h.do(t, h.tokA, "PUT", path, map[string]any{
		"summary": "buy oat milk", "due": now["due"], "completed": true, "etag": now["etag"]})
	if rec.Code != http.StatusOK {
		t.Fatalf("save from a fresh form: %d %s", rec.Code, rec.Body)
	}
	// A client that sends no etag (the MCP tools) saves as before.
	if rec, _ = h.do(t, h.tokA, "PUT", path, map[string]any{"summary": "milk"}); rec.Code != http.StatusOK {
		t.Fatalf("save without an etag: %d", rec.Code)
	}
	// Ticking the box changes one field on the server's own copy: it is never stale.
	if rec, _ = h.do(t, h.tokA, "PUT", path+"/done", map[string]any{"completed": true}); rec.Code != http.StatusOK {
		t.Fatalf("toggle: %d", rec.Code)
	}
	if _, now = h.do(t, h.tokA, "GET", path, nil); now["summary"] != "milk" || now["completed"] != true {
		t.Fatalf("after the toggle: %v", now)
	}
}

func TestEventSaveOverSomebodyElsesChangeIsRefused(t *testing.T) {
	h := newCalHarness(t)
	home := h.calendarOf(t, h.uidA)
	body := func(summary string, etag any) map[string]any {
		return map[string]any{"summary": summary, "start": "2026-08-05T10:00:00Z", "end": "2026-08-05T11:00:00Z",
			"alarm": "keep", "calendar_id": home, "etag": etag}
	}
	rec, m := h.do(t, h.tokA, "POST", "/me/calendar/events", body("standup", nil))
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: %d", rec.Code)
	}
	path := "/me/calendar/events/" + m["uid"].(string) + "?calendar_id=" + home
	_, loaded := h.do(t, h.tokA, "GET", path, nil)
	etag, _ := loaded["etag"].(string)
	if etag == "" {
		t.Fatalf("a loaded event has no etag: %v", loaded)
	}
	if rec, _ = h.do(t, h.tokA, "PUT", path, body("standup (moved)", etag)); rec.Code != http.StatusOK {
		t.Fatalf("first save: %d %s", rec.Code, rec.Body)
	}
	if rec, _ = h.do(t, h.tokA, "PUT", path, body("daily", etag)); rec.Code != http.StatusPreconditionFailed {
		t.Fatalf("save over a newer version: %d, want 412", rec.Code)
	}
	if _, now := h.do(t, h.tokA, "GET", path, nil); now["summary"] != "standup (moved)" {
		t.Fatalf("the refused save changed the event: %v", now)
	}
}

func TestContactSaveOverSomebodyElsesChangeIsRefused(t *testing.T) {
	h := newCalHarness(t)
	rec, m := h.do(t, h.tokA, "POST", "/me/contacts", map[string]any{"full_name": "Ann Lee"})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", rec.Code, rec.Body)
	}
	path := "/me/contacts/" + m["uid"].(string)
	_, loaded := h.do(t, h.tokA, "GET", path, nil)
	etag, _ := loaded["etag"].(string)
	if etag == "" {
		t.Fatalf("a loaded contact has no etag: %v", loaded)
	}
	if rec, _ = h.do(t, h.tokA, "PUT", path, map[string]any{"full_name": "Ann Lee", "org": "Acme", "etag": etag}); rec.Code != http.StatusOK {
		t.Fatalf("first save: %d %s", rec.Code, rec.Body)
	}
	if rec, _ = h.do(t, h.tokA, "PUT", path, map[string]any{"full_name": "Anna Lee", "etag": etag}); rec.Code != http.StatusPreconditionFailed {
		t.Fatalf("save over a newer version: %d, want 412", rec.Code)
	}
	if _, now := h.do(t, h.tokA, "GET", path, nil); now["full_name"] != "Ann Lee" || now["org"] != "Acme" {
		t.Fatalf("the refused save changed the contact: %v", now)
	}
}

// Moving a task to another list wrote it there and then deleted it from the first as two
// separate steps: a failure between them left the task in both.
func TestMoveCalendarObjectIsOneStep(t *testing.T) {
	h := newCalHarness(t)
	ctx := context.Background()
	src, err := h.dav.CreateCalendar(ctx, h.uidA, "Inbox", "")
	if err != nil {
		t.Fatal(err)
	}
	dst, err := h.dav.CreateCalendar(ctx, h.uidA, "Work", "")
	if err != nil {
		t.Fatal(err)
	}
	srcID, dstID := db.UUIDString(src.ID), db.UUIDString(dst.ID)
	ics := func(uid, summary string) string {
		return "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//t//EN\r\nBEGIN:VTODO\r\nUID:" + uid +
			"\r\nDTSTAMP:20260101T000000Z\r\nSUMMARY:" + summary + "\r\nEND:VTODO\r\nEND:VCALENDAR\r\n"
	}
	etag, err := h.dav.PutCalendarObject(ctx, srcID, "t1", ics("t1", "one"))
	if err != nil {
		t.Fatal(err)
	}
	// The destination already has another object with this iCalendar UID: the write there
	// is refused, and the original must still be where it was.
	if _, err := h.dav.PutCalendarObject(ctx, dstID, "other", ics("t1", "clash")); err != nil {
		t.Fatal(err)
	}
	if _, err := h.dav.MoveCalendarObject(ctx, srcID, dstID, "t1", ics("t1", "one"), etag); err == nil {
		t.Fatal("a move into a UID clash succeeded")
	}
	if _, _, err := h.dav.GetCalendarObject(ctx, srcID, "t1"); err != nil {
		t.Fatalf("a failed move lost the original: %v", err)
	}
	if err := h.dav.DeleteCalendarObject(ctx, dstID, "other"); err != nil {
		t.Fatal(err)
	}
	// A stale etag: somebody changed the task since it was read.
	if _, err := h.dav.MoveCalendarObject(ctx, srcID, dstID, "t1", ics("t1", "one"), "stale"); err == nil {
		t.Fatal("a move of a changed object succeeded")
	}
	if _, _, err := h.dav.GetCalendarObject(ctx, dstID, "t1"); err == nil {
		t.Fatal("a refused move wrote the copy")
	}
	if _, err := h.dav.MoveCalendarObject(ctx, srcID, dstID, "t1", ics("t1", "one"), etag); err != nil {
		t.Fatalf("move: %v", err)
	}
	if _, _, err := h.dav.GetCalendarObject(ctx, srcID, "t1"); err == nil {
		t.Fatal("the moved object is still in the first list")
	}
	if _, _, err := h.dav.GetCalendarObject(ctx, dstID, "t1"); err != nil {
		t.Fatalf("the moved object is not in the second list: %v", err)
	}
}
