package api

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"

	"discodrive/internal/auth"
	"discodrive/internal/music"
	"discodrive/internal/music/tagwrite"
	"discodrive/internal/safecontent"
)

type tagsDTO struct {
	Title       *string `json:"title"`
	Artist      *string `json:"artist"`
	Album       *string `json:"album"`
	AlbumArtist *string `json:"albumArtist"`
	Genre       *string `json:"genre"`
	Year        *int    `json:"year"`
	Track       *int    `json:"track"`
	Disc        *int    `json:"disc"`
}

type tagInfoDTO struct {
	Tags     tagsDTO `json:"tags"`
	HasCover bool    `json:"hasCover"`
	Writable bool    `json:"writable"`
	Suffix   string  `json:"suffix"`
}

// tagEditRequest: `fields` lists which keys to apply; `values` carries the new
// values; `cover` is "keep" (omitted), "remove" (null), or base64 data.
type tagEditRequest struct {
	Fields []string        `json:"fields"`
	Values map[string]any  `json:"values"`
	Cover  json.RawMessage `json:"cover"` // absent=keep, null=remove, {"data","mime"}=replace
}

func (req tagEditRequest) has(field string) bool {
	for _, f := range req.Fields {
		if f == field {
			return true
		}
	}
	return false
}

func (req tagEditRequest) toTags() tagwrite.Tags {
	var t tagwrite.Tags
	str := func(k string) *string {
		if !req.has(k) {
			return nil
		}
		s, _ := req.Values[k].(string)
		return &s
	}
	num := func(k string) *int {
		if !req.has(k) {
			return nil
		}
		f, _ := req.Values[k].(float64)
		n := int(f)
		return &n
	}
	t.Title, t.Artist, t.Album = str("title"), str("artist"), str("album")
	t.AlbumArtist, t.Genre = str("albumArtist"), str("genre")
	t.Year, t.Track, t.Disc = num("year"), num("track"), num("disc")
	return t
}

func (req tagEditRequest) coverChange() (tagwrite.CoverChange, *tagwrite.Cover, error) {
	if len(req.Cover) == 0 {
		return tagwrite.CoverKeep, nil, nil
	}
	if string(req.Cover) == "null" {
		return tagwrite.CoverRemove, nil, nil
	}
	var c struct {
		Data string `json:"data"`
		Mime string `json:"mime"`
	}
	if err := json.Unmarshal(req.Cover, &c); err != nil {
		return tagwrite.CoverKeep, nil, err
	}
	raw, err := base64.StdEncoding.DecodeString(c.Data)
	if err != nil {
		return tagwrite.CoverKeep, nil, err
	}
	return tagwrite.CoverReplace, &tagwrite.Cover{Data: raw, Mime: c.Mime}, nil
}

func tagsToDTO(t tagwrite.Tags) tagsDTO {
	return tagsDTO{
		Title: t.Title, Artist: t.Artist, Album: t.Album, AlbumArtist: t.AlbumArtist,
		Genre: t.Genre, Year: t.Year, Track: t.Track, Disc: t.Disc,
	}
}

func mapTagErr(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, music.ErrReadOnlyFormat):
		writeError(w, http.StatusUnprocessableEntity, "format is read-only")
	case errors.Is(err, music.ErrNotInMusicFolder), errors.Is(err, music.ErrNotAudio):
		writeError(w, http.StatusNotFound, "not found")
	default:
		writeError(w, http.StatusInternalServerError, "internal error")
	}
}

func (s *Server) handleGetMusicTags(w http.ResponseWriter, r *http.Request) {
	info, err := s.tagEditor.Read(r.Context(), auth.UserID(r.Context()), r.PathValue("id"))
	if err != nil {
		mapTagErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, tagInfoDTO{
		Tags: tagsToDTO(info.Tags), HasCover: info.HasCover, Writable: info.Writable, Suffix: info.Suffix,
	})
}

func (s *Server) handleGetMusicTagsCover(w http.ResponseWriter, r *http.Request) {
	data, _, ok, err := s.tagEditor.Cover(r.Context(), auth.UserID(r.Context()), r.PathValue("id"))
	if err != nil {
		mapTagErr(w, err)
		return
	}
	ct, raster := safecontent.Raster(data)
	if !ok || !raster {
		writeError(w, http.StatusNotFound, "no cover")
		return
	}
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Type", ct)
	w.Write(data)
}

func (s *Server) handlePutMusicTags(w http.ResponseWriter, r *http.Request) {
	var req tagEditRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	cc, cover, err := req.coverChange()
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid cover")
		return
	}
	if err := s.tagEditor.Write(r.Context(), auth.UserID(r.Context()), r.PathValue("id"), req.toTags(), cc, cover); err != nil {
		mapTagErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleMusicTagsFolderCount(w http.ResponseWriter, r *http.Request) {
	n, err := s.tagEditor.CountFolderAudio(r.Context(), auth.UserID(r.Context()), r.PathValue("id"))
	if err != nil {
		mapTagErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]int{"affected": n})
}

func (s *Server) handlePostMusicTagsFolder(w http.ResponseWriter, r *http.Request) {
	var req tagEditRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	cc, cover, err := req.coverChange()
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid cover")
		return
	}
	res, err := s.tagEditor.WriteFolder(r.Context(), auth.UserID(r.Context()), r.PathValue("id"), req.toTags(), cc, cover)
	if err != nil {
		mapTagErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}
