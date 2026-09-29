package api

import (
	"context"
	"io"
	"net/http"
	"strings"

	"github.com/emersion/go-vcard"

	"discodrive/internal/dav"
	"discodrive/internal/db"
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
func importVCards(ctx context.Context, svc *dav.Service, abID, raw string) (imported, skipped int) {
	for _, block := range splitVCards(raw) {
		card, err := vcard.NewDecoder(strings.NewReader(block)).Decode()
		if err != nil {
			skipped++
			continue
		}
		uid := card.Value(vcard.FieldUID)
		data := block
		if uid == "" {
			uid = newContactUID()
			card.SetValue(vcard.FieldUID, uid)
			var b strings.Builder
			if err := vcard.NewEncoder(&b).Encode(card); err != nil {
				skipped++
				continue
			}
			data = b.String()
		}
		if _, err := svc.PutAddressbookObject(ctx, abID, uid, data); err != nil {
			skipped++
			continue
		}
		imported++
	}
	return imported, skipped
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
	var raw string
	if strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/") {
		if err := r.ParseMultipartForm(maxImportBytes); err != nil {
			writeError(w, http.StatusBadRequest, "expected multipart/form-data")
			return
		}
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
	imported, skipped := importVCards(r.Context(), s.dav, abID, raw)
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
