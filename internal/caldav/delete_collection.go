package caldav

import (
	"errors"
	"net/http"
	"strings"

	"discodrive/internal/dav"
	"discodrive/internal/db"
)

// HandleDelete serves DELETE. go-webdav passes every DELETE to DeleteCalendarObject, so
// deleting a whole collection — what Apple Reminders and Calendar send when a list is
// removed — answered 404 and the list came back on the next sync. A collection path is
// handled here; anything else goes to next.
//
// Only the owner deletes a calendar: for someone it is shared with the answer is 403,
// and the home set and the principal are never deletable.
func (b *Backend) HandleDelete(w http.ResponseWriter, r *http.Request, next http.Handler) {
	segments := strings.Split(strings.Trim(strings.TrimPrefix(r.URL.Path, prefix), "/"), "/")
	if len(segments) > 3 {
		next.ServeHTTP(w, r)
		return
	}
	if len(segments) < 3 {
		http.Error(w, "caldav: this collection cannot be deleted", http.StatusForbidden)
		return
	}
	uid := userID(r.Context())
	cal, err := b.resolveCalendar(r.Context(), segments[2])
	if err != nil {
		http.Error(w, "caldav: not found", http.StatusNotFound)
		return
	}
	if db.UUIDString(cal.UserID) != uid {
		http.Error(w, "caldav: only the owner can delete a calendar", http.StatusForbidden)
		return
	}
	switch err := b.svc.DeleteCalendar(r.Context(), uid, db.UUIDString(cal.ID)); {
	case err == nil:
		w.WriteHeader(http.StatusNoContent)
	case errors.Is(err, dav.ErrNotFound):
		http.Error(w, "caldav: not found", http.StatusNotFound)
	default:
		http.Error(w, "caldav: internal error", http.StatusInternalServerError)
	}
}
