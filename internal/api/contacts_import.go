package api

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/emersion/go-vcard"

	"discodrive/internal/dav"
	"discodrive/internal/db"
	"discodrive/internal/quota"
)

const maxImportBytes = 32 << 20 // 32 MB

// splitVCards splits a vCard stream into raw BEGIN:VCARD…END:VCARD blocks (normalizing
// line endings to CRLF). Preserves original lines within each block (e.g., photo folding).
func splitVCards(raw string) []string {
	// A UTF-8 byte order mark (Windows exports, Outlook) would hide the first BEGIN:VCARD
	// and drop that card without a trace.
	raw = strings.TrimPrefix(raw, "\uFEFF")
	var out []string
	var cur []string
	in := false
	for _, line := range strings.Split(raw, "\n") {
		l := strings.TrimRight(line, "\r")
		u := strings.ToUpper(strings.TrimSpace(l))
		if strings.HasPrefix(u, "BEGIN:VCARD") {
			in = true
			cur = nil
		}
		if in {
			cur = append(cur, l)
		}
		if strings.HasPrefix(u, "END:VCARD") {
			if in && len(cur) > 0 {
				out = append(out, strings.Join(cur, "\r\n")+"\r\n")
			}
			in = false
			cur = nil
		}
	}
	return out
}

// importVCards saves cards from the stream into the address book (upsert by UID). Returns
// the number of imported and skipped cards (failed decode or save error).
func importVCards(ctx context.Context, svc *dav.Service, abID, raw string) (imported, skipped int, err error) {
	return importCards(raw, func(uid, data string) error {
		_, err := svc.PutAddressbookObject(ctx, abID, uid, data)
		return err
	})
}

// importCards stores each card of raw with put. A card that does not parse or is refused
// is skipped; running out of storage stops the import with quota.ErrExceeded, since every
// card after it would be refused too.
func importCards(raw string, put func(uid, data string) error) (imported, skipped int, err error) {
	for i, block := range splitVCards(raw) {
		// Every card is a transaction of its own. A file is a few thousand contacts at
		// most; whatever comes after maxImportCards, or is larger than a card with a
		// photo can be, is counted as skipped rather than worked through.
		if i >= maxImportCards || len(block) > maxImportCard {
			skipped++
			continue
		}
		card, err := vcard.NewDecoder(strings.NewReader(block)).Decode()
		if err != nil {
			skipped++
			continue
		}
		uid := card.Value(vcard.FieldUID)
		data := block
		// The UID becomes the object's name in CardDAV URLs. One that cannot stand in a
		// path ("foo/bar") would import and then be unreachable for every client.
		if uid == "" || !safeObjectUID(uid) {
			uid = newContactUID()
			card.SetValue(vcard.FieldUID, uid)
			var b strings.Builder
			if err := vcard.NewEncoder(&b).Encode(card); err != nil {
				skipped++
				continue
			}
			data = b.String()
		}
		if err := put(uid, data); err != nil {
			if errors.Is(err, quota.ErrExceeded) {
				return imported, skipped, err
			}
			skipped++
			continue
		}
		imported++
	}
	return imported, skipped, nil
}

const (
	maxImportCards = 50000
	maxImportCard  = 2 << 20
)

// safeObjectUID reports whether uid can be a path segment as it is.
func safeObjectUID(uid string) bool {
	if len(uid) > 255 || uid == "." || uid == ".." {
		return false
	}
	for _, c := range uid {
		if c <= ' ' || c == 0x7f || strings.ContainsRune(`/\?#%`, c) {
			return false
		}
	}
	return true
}

// exportVCards concatenates raw cards into a single vCard stream.
func exportVCards(objs []db.AddressbookObject) string {
	var b strings.Builder
	for _, o := range objs {
		b.WriteString(strings.TrimRight(o.Data, "\r\n"))
		b.WriteString("\r\n")
	}
	return b.String()
}

// POST /me/contacts/import — multipart (file) or raw text/vcard body.
func (s *Server) handleImportContacts(w http.ResponseWriter, r *http.Request) {
	abID, ok := s.book(r)
	if !ok {
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	// The route is exempt from the API body limit, so cap it here: a file of up to
	// maxImportBytes plus room for the multipart framing.
	r.Body = http.MaxBytesReader(w, r.Body, maxImportBytes+1<<20)
	var raw string
	if strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/") {
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			var tooBig *http.MaxBytesError
			if errors.As(err, &tooBig) {
				writeError(w, http.StatusRequestEntityTooLarge, "request body too large")
				return
			}
			writeError(w, http.StatusBadRequest, "expected multipart/form-data")
			return
		}
		// Runs on a request clone: net/http would not remove these spool files.
		defer r.MultipartForm.RemoveAll()
		f, _, err := r.FormFile("file")
		if err != nil {
			writeError(w, http.StatusBadRequest, "file is required (field: file)")
			return
		}
		defer f.Close()
		b, _ := io.ReadAll(io.LimitReader(f, maxImportBytes))
		raw = string(b)
	} else {
		b, _ := io.ReadAll(io.LimitReader(r.Body, maxImportBytes))
		raw = string(b)
	}
	imported, skipped, err := importVCards(r.Context(), s.dav, abID, raw)
	if errors.Is(err, quota.ErrExceeded) {
		writeJSON(w, http.StatusInsufficientStorage, map[string]any{
			"error": "insufficient storage", "imported": imported, "skipped": skipped})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"imported": imported, "skipped": skipped})
}

// GET /me/contacts/export — all contacts in the address book as a single .vcf file.
func (s *Server) handleExportContacts(w http.ResponseWriter, r *http.Request) {
	abID, ok := s.book(r)
	if !ok {
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	objs, err := s.dav.ListAddressbookObjects(r.Context(), abID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	w.Header().Set("Content-Type", "text/vcard; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="contacts.vcf"`)
	_, _ = w.Write([]byte(exportVCards(objs)))
}
