package kosync

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/jackc/pgx/v5"

	"discodrive/internal/db"
)

// usersAuth handles GET /users/auth.
// Returns 200 {"username": email} on success, 401 {} on failure.
func (h *Handler) usersAuth(w http.ResponseWriter, r *http.Request) {
	_, email, ok := h.authUser(r)
	if !ok {
		writeJSON(w, http.StatusUnauthorized, struct{}{})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"username": email})
}

// usersCreate handles POST /users/create.
// KOReader sends this on first connect; we reuse auth to check if the user exists.
// Returns 201 on success, 402 on failure (KOReader treats 402 as non-fatal "user exists/invalid").
func (h *Handler) usersCreate(w http.ResponseWriter, r *http.Request) {
	_, email, ok := h.authUser(r)
	if !ok {
		writeJSON(w, http.StatusPaymentRequired, struct{}{})
		return
	}
	writeJSON(w, http.StatusCreated, map[string]string{"username": email})
}

// The KOSync routes are exempt from the API body limit (limitAPIBodies), so the handler
// bounds its own input. KOReader sends a document hash, an xpointer or page number and a
// device name: all short. The table is not counted toward the storage quota, so the
// number of documents per user is capped too.
const (
	maxProgressBody = 64 << 10
	maxDocumentLen  = 512
	maxProgressLen  = 4096
	maxDeviceLen    = 256
	maxDocsPerUser  = 10000
)

// progressBody is the JSON body for PUT /syncs/progress.
type progressBody struct {
	Document   string  `json:"document"`
	Progress   string  `json:"progress"`
	Percentage float64 `json:"percentage"`
	Device     string  `json:"device"`
	DeviceID   string  `json:"device_id"`
}

// syncsProgressPut handles PUT /syncs/progress.
// Upserts the reading position for the authenticated user.
func (h *Handler) syncsProgressPut(w http.ResponseWriter, r *http.Request) {
	userID, _, ok := h.authUser(r)
	if !ok {
		writeJSON(w, http.StatusUnauthorized, struct{}{})
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxProgressBody)
	var body progressBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeJSON(w, http.StatusRequestEntityTooLarge, struct{}{})
			return
		}
		writeJSON(w, http.StatusBadRequest, struct{}{})
		return
	}
	if len(body.Document) > maxDocumentLen || len(body.Progress) > maxProgressLen ||
		len(body.Device) > maxDeviceLen || len(body.DeviceID) > maxDeviceLen {
		writeJSON(w, http.StatusBadRequest, struct{}{})
		return
	}

	uid, err := db.ParseUUID(userID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, struct{}{})
		return
	}

	if _, err := h.q.GetReadingProgress(r.Context(), db.GetReadingProgressParams{UserID: uid, Document: body.Document}); errors.Is(err, pgx.ErrNoRows) {
		n, cerr := h.q.CountReadingProgress(r.Context(), uid)
		if cerr != nil {
			writeJSON(w, http.StatusInternalServerError, struct{}{})
			return
		}
		if n >= maxDocsPerUser {
			writeJSON(w, http.StatusInsufficientStorage, struct{}{})
			return
		}
	} else if err != nil {
		writeJSON(w, http.StatusInternalServerError, struct{}{})
		return
	}

	row, err := h.q.UpsertReadingProgress(r.Context(), db.UpsertReadingProgressParams{
		UserID:     uid,
		Document:   body.Document,
		Progress:   body.Progress,
		Percentage: float32(body.Percentage),
		Device:     body.Device,
		DeviceID:   body.DeviceID,
	})
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, struct{}{})
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"document":  row.Document,
		"timestamp": row.UpdatedAt.Time.Unix(),
	})
}

// syncsProgressGet handles GET /syncs/progress/{document}.
// Returns the stored reading position for the authenticated user, or {} if not found.
func (h *Handler) syncsProgressGet(w http.ResponseWriter, r *http.Request) {
	userID, _, ok := h.authUser(r)
	if !ok {
		writeJSON(w, http.StatusUnauthorized, struct{}{})
		return
	}

	document := r.PathValue("document")

	uid, err := db.ParseUUID(userID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, struct{}{})
		return
	}

	row, err := h.q.GetReadingProgress(r.Context(), db.GetReadingProgressParams{
		UserID:   uid,
		Document: document,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeJSON(w, http.StatusOK, struct{}{})
			return
		}
		writeJSON(w, http.StatusInternalServerError, struct{}{})
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"document":   row.Document,
		"progress":   row.Progress,
		"percentage": row.Percentage,
		"device":     row.Device,
		"device_id":  row.DeviceID,
		"timestamp":  row.UpdatedAt.Time.Unix(),
	})
}
