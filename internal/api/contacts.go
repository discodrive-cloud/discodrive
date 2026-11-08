package api

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/emersion/go-vcard"
	"github.com/google/uuid"

	"discodrive/internal/auth"
	"discodrive/internal/dav"
	"discodrive/internal/db"
)

type contactForm struct {
	UID      string       `json:"uid"`
	FullName string       `json:"full_name"`
	Family   string       `json:"family"`
	Given    string       `json:"given"`
	Emails   []typedValue `json:"emails"`
	Phones   []typedValue `json:"phones"`
	Org      string       `json:"org"`
	Title    string       `json:"title"`
	Adr      contactAddr  `json:"adr"`
	Note     string       `json:"note"`
	Bday     string       `json:"bday"`
	HasPhoto bool         `json:"has_photo"`
	PhotoURI string       `json:"photo_uri,omitempty"`
}

type typedValue struct {
	Type  string `json:"type"`
	Value string `json:"value"`
}

type contactAddr struct {
	Street  string `json:"street"`
	City    string `json:"city"`
	Region  string `json:"region"`
	Postal  string `json:"postal"`
	Country string `json:"country"`
}

func (s *Server) book(r *http.Request) (string, bool) {
	ab, err := s.dav.EnsureDefaultAddressbook(r.Context(), auth.UserID(r.Context()))
	if err != nil {
		return "", false
	}
	return db.UUIDString(ab.ID), true
}

func (s *Server) handleListContacts(w http.ResponseWriter, r *http.Request) {
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
	type item struct {
		UID      string   `json:"uid"`
		FullName string   `json:"full_name"`
		Emails   []string `json:"emails"`
		Phones   []string `json:"phones"`
	}
	out := make([]item, 0, len(objs))
	for _, o := range objs {
		var p struct {
			FullName string   `json:"full_name"`
			Emails   []string `json:"emails"`
			Phones   []string `json:"phones"`
		}
		_ = json.Unmarshal(o.Parsed, &p)
		out = append(out, item{UID: o.Uid, FullName: p.FullName, Emails: p.Emails, Phones: p.Phones})
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleGetContact(w http.ResponseWriter, r *http.Request) {
	abID, ok := s.book(r)
	if !ok {
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	data, _, err := s.dav.GetAddressbookObject(r.Context(), abID, r.PathValue("uid"))
	if err == dav.ErrNotFound {
		writeError(w, http.StatusNotFound, "contact not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	card, err := vcard.NewDecoder(strings.NewReader(data)).Decode()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to parse vCard")
		return
	}
	writeJSON(w, http.StatusOK, cardToForm(r.PathValue("uid"), card))
}

func cardToForm(uid string, card vcard.Card) contactForm {
	f := contactForm{UID: uid}
	f.FullName = card.Value(vcard.FieldFormattedName)
	if n := card.Name(); n != nil {
		f.Family, f.Given = n.FamilyName, n.GivenName
	}
	for _, e := range card[vcard.FieldEmail] {
		f.Emails = append(f.Emails, typedValue{Type: e.Params.Get(vcard.ParamType), Value: e.Value})
	}
	for _, t := range card[vcard.FieldTelephone] {
		f.Phones = append(f.Phones, typedValue{Type: t.Params.Get(vcard.ParamType), Value: t.Value})
	}
	f.Org = card.Value(vcard.FieldOrganization)
	f.Title = card.Value(vcard.FieldTitle)
	if a := card.Address(); a != nil {
		f.Adr = contactAddr{Street: a.StreetAddress, City: a.Locality, Region: a.Region, Postal: a.PostalCode, Country: a.Country}
	}
	f.Note = card.Value(vcard.FieldNote)
	f.Bday = card.Value(vcard.FieldBirthday)
	f.HasPhoto = card.Get(vcard.FieldPhoto) != nil
	f.PhotoURI = photoDataURI(card)
	return f
}

// photoDataURI extracts the contact photo as a URI suitable for use in <img src>.
// Handles all vCard variants: data: URI (4.0), inline base64 (3.0 ENCODING=b/BASE64), and external URLs.
func photoDataURI(card vcard.Card) string {
	p := card.Get(vcard.FieldPhoto)
	if p == nil || p.Value == "" {
		return ""
	}
	v := strings.TrimSpace(p.Value)
	if strings.HasPrefix(v, "data:") || strings.HasPrefix(v, "http://") || strings.HasPrefix(v, "https://") {
		return v
	}
	enc := strings.ToLower(p.Params.Get("ENCODING"))
	if enc == "b" || enc == "base64" {
		mime := "image/jpeg"
		if t := strings.ToLower(p.Params.Get(vcard.ParamType)); t != "" {
			mime = "image/" + t
		}
		// strip any whitespace/line breaks from the base64 data
		v = strings.NewReplacer(" ", "", "\r", "", "\n", "", "\t", "").Replace(v)
		return "data:" + mime + ";base64," + v
	}
	return ""
}

func newContactUID() string { return uuid.NewString() }

func (s *Server) handleCreateContact(w http.ResponseWriter, r *http.Request) {
	abID, ok := s.book(r)
	if !ok {
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	var form contactForm
	if err := json.NewDecoder(r.Body).Decode(&form); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	uid := newContactUID()
	card := vcard.Card{}
	card.SetValue(vcard.FieldVersion, "3.0")
	card.SetValue(vcard.FieldUID, uid)
	applyForm(card, form)
	if err := s.putCard(r, abID, uid, card); err != nil {
		writePutCalError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"uid": uid})
}

func (s *Server) handleUpdateContact(w http.ResponseWriter, r *http.Request) {
	abID, ok := s.book(r)
	if !ok {
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	uid := r.PathValue("uid")
	data, _, err := s.dav.GetAddressbookObject(r.Context(), abID, uid)
	if err == dav.ErrNotFound {
		writeError(w, http.StatusNotFound, "contact not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	var form contactForm
	if err := json.NewDecoder(r.Body).Decode(&form); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	card, err := vcard.NewDecoder(strings.NewReader(data)).Decode()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to parse vCard")
		return
	}
	applyForm(card, form) // modify-existing: PHOTO/X-* fields are preserved
	if err := s.putCard(r, abID, uid, card); err != nil {
		writePutCalError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"uid": uid})
}

func (s *Server) handleDeleteContact(w http.ResponseWriter, r *http.Request) {
	abID, ok := s.book(r)
	if !ok {
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	if err := s.dav.DeleteAddressbookObject(r.Context(), abID, r.PathValue("uid")); err != nil {
		if err == dav.ErrNotFound {
			writeError(w, http.StatusNotFound, "contact not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) putCard(r *http.Request, abID, uid string, card vcard.Card) error {
	var b strings.Builder
	if err := vcard.NewEncoder(&b).Encode(card); err != nil {
		return err
	}
	_, err := s.dav.PutAddressbookObject(r.Context(), abID, uid, b.String())
	return err
}

// applyForm overwrites the editable fields; other fields (PHOTO, X-*) are left untouched.
func applyForm(card vcard.Card, f contactForm) {
	card.SetValue(vcard.FieldFormattedName, f.FullName)
	card.SetName(&vcard.Name{FamilyName: f.Family, GivenName: f.Given})
	delete(card, vcard.FieldEmail)
	for _, e := range f.Emails {
		if e.Value == "" {
			continue
		}
		fld := &vcard.Field{Value: e.Value, Params: vcard.Params{}}
		if e.Type != "" {
			fld.Params[vcard.ParamType] = []string{e.Type}
		}
		card.Add(vcard.FieldEmail, fld)
	}
	delete(card, vcard.FieldTelephone)
	for _, t := range f.Phones {
		if t.Value == "" {
			continue
		}
		fld := &vcard.Field{Value: t.Value, Params: vcard.Params{}}
		if t.Type != "" {
			fld.Params[vcard.ParamType] = []string{t.Type}
		}
		card.Add(vcard.FieldTelephone, fld)
	}
	setOrDelete(card, vcard.FieldOrganization, f.Org)
	setOrDelete(card, vcard.FieldTitle, f.Title)
	setOrDelete(card, vcard.FieldNote, f.Note)
	setOrDelete(card, vcard.FieldBirthday, f.Bday)
	if f.Adr.Street == "" && f.Adr.City == "" && f.Adr.Region == "" && f.Adr.Postal == "" && f.Adr.Country == "" {
		delete(card, vcard.FieldAddress)
	} else {
		card.SetAddress(&vcard.Address{StreetAddress: f.Adr.Street, Locality: f.Adr.City, Region: f.Adr.Region, PostalCode: f.Adr.Postal, Country: f.Adr.Country})
	}
}

func setOrDelete(card vcard.Card, key, val string) {
	if val == "" {
		delete(card, key)
		return
	}
	card.SetValue(key, val)
}
