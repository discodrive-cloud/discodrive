package api

import (
	"encoding/json"
	"net/http"

	"discodrive/internal/auth"
	"discodrive/internal/dav"
	"discodrive/internal/db"
)

// POST /me/contacts/share {email} — share the default address book with another user.
func (s *Server) handleShareContacts(w http.ResponseWriter, r *http.Request) {
	abID, ok := s.book(r)
	if !ok {
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	var body struct {
		Email string `json:"email"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	owner := auth.UserID(r.Context())
	share, err := s.dav.ShareAddressbook(r.Context(), owner, abID, body.Email)
	if !writeShareResult(w, err, "address book not found") {
		return
	}
	abName := "Contacts"
	if ab, e := s.dav.GetAddressbook(r.Context(), abID); e == nil && ab.Name != "" {
		abName = ab.Name
	}
	sharerEmail := ""
	if su, e := s.q.GetUserByID(r.Context(), mustUUID(owner)); e == nil {
		sharerEmail = su.Email
	}
	s.notify.Emit(r.Context(), db.UUIDString(share.SharedWithUser), "share.received",
		map[string]any{"NodeName": abName, "SharerEmail": sharerEmail, "ResourceLabel": "address book"})
	writeShareOK(w)
}

// GET /me/contacts/shares
func (s *Server) handleListContactsShares(w http.ResponseWriter, r *http.Request) {
	abID, ok := s.book(r)
	if !ok {
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	infos, err := s.dav.ListAddressbookShares(r.Context(), auth.UserID(r.Context()), abID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	type dto struct {
		ID    string `json:"id"`
		Email string `json:"email"`
	}
	out := make([]dto, 0, len(infos))
	for _, i := range infos {
		out = append(out, dto{ID: i.ID, Email: i.Email})
	}
	writeJSON(w, http.StatusOK, out)
}

// DELETE /me/contacts/shares/{shareId} — the owner revokes a share, or the recipient leaves
// the address book.
func (s *Server) handleDeleteContactsShare(w http.ResponseWriter, r *http.Request) {
	err := s.dav.DeleteAddressbookShare(r.Context(), auth.UserID(r.Context()), r.PathValue("shareId"))
	switch err {
	case nil:
		w.WriteHeader(http.StatusNoContent)
	case dav.ErrNotOwner:
		writeError(w, http.StatusForbidden, "owner only")
	case dav.ErrNotFound:
		writeError(w, http.StatusNotFound, "share not found")
	default:
		writeError(w, http.StatusInternalServerError, "internal error")
	}
}
