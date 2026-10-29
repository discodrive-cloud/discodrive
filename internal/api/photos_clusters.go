package api

import (
	"errors"
	"fmt"
	"math"
	"net/http"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"discodrive/internal/db"
)

// The gallery's map: clusters of the feed's cards by a grid in the Web Mercator projection
// (photo_map_cell, migration 28), and the area filter that narrows the feed to some cells.

// maxMapZoom bounds z: past it a cell is a few metres wide and splits nothing further.
const maxMapZoom = 20

// maxAreaCells bounds the cells of one area: the client merges a few overlapping
// clusters, never more than fit on a screen.
const maxAreaCells = 16

type photoClusterDTO struct {
	Lat     float64     `json:"lat"`
	Lon     float64     `json:"lon"`
	Count   int64       `json:"count"`
	Cell    string      `json:"cell"` // "x.y" at the request's z
	Place   *string     `json:"place"`
	Country *string     `json:"country"`
	Cover   photoRowDTO `json:"cover"`
}

func parseMapZoom(v string) (int32, bool) {
	z, err := strconv.Atoi(v)
	if err != nil || z < 0 || z > maxMapZoom {
		return 0, false
	}
	return int32(z), true
}

// parseBBox reads "west,south,east,north" in degrees. West may lie east of east: the box
// then crosses the 180th meridian.
func parseBBox(v string) (w, s, e, n float64, ok bool) {
	parts := strings.Split(v, ",")
	if len(parts) != 4 {
		return 0, 0, 0, 0, false
	}
	var f [4]float64
	for i, p := range parts {
		x, err := strconv.ParseFloat(strings.TrimSpace(p), 64)
		if err != nil || math.IsNaN(x) || math.IsInf(x, 0) {
			return 0, 0, 0, 0, false
		}
		f[i] = x
	}
	w, s, e, n = f[0], f[1], f[2], f[3]
	if s > n || s < -90 || n > 90 || w < -180 || w > 180 || e < -180 || e > 180 {
		return 0, 0, 0, 0, false
	}
	return w, s, e, n, true
}

// parseArea reads "z/x.y,x.y,…": cells of photo_map_cell at one zoom, as the
// photo_map_key numbers (x·2^(z+1) + y) the feed queries compare.
func parseArea(v string) (z int32, keys []int64, ok bool) {
	zs, cells, found := strings.Cut(v, "/")
	if !found {
		return 0, nil, false
	}
	if z, ok = parseMapZoom(zs); !ok {
		return 0, nil, false
	}
	list := strings.Split(cells, ",")
	if len(list) > maxAreaCells {
		return 0, nil, false
	}
	side := int64(2) << z
	for _, c := range list {
		xv, yv, found := strings.Cut(c, ".")
		x, errX := strconv.ParseInt(xv, 10, 64)
		y, errY := strconv.ParseInt(yv, 10, 64)
		if !found || errX != nil || errY != nil || x < 0 || y < 0 || x >= side || y >= side {
			return 0, nil, false
		}
		keys = append(keys, x*side+y)
	}
	return z, keys, true
}

// GET /me/photos/v1/map/clusters?z=&bbox=w,s,e,n — the clusters of the feed's cards with a
// position inside the box, biggest first. A switched-off gallery has none.
func (s *Server) handlePhotoMapClusters(w http.ResponseWriter, r *http.Request) {
	if !s.photosReady(w) {
		return
	}
	uid, ok := photosUser(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	z, ok := parseMapZoom(q.Get("z"))
	if !ok {
		writeError(w, http.StatusBadRequest, "invalid z")
		return
	}
	west, south, east, north, ok := parseBBox(q.Get("bbox"))
	if !ok {
		writeError(w, http.StatusBadRequest, "invalid bbox")
		return
	}
	out := []photoClusterDTO{}
	if !s.photosOn(r, uid) {
		writeJSON(w, http.StatusOK, out)
		return
	}
	cs, err := s.q.ListPhotoMapClusters(r.Context(), db.ListPhotoMapClustersParams{
		UserID: uid, Z: z, West: west, South: south, East: east, North: north,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	ids := make([]pgtype.UUID, len(cs))
	for i, c := range cs {
		ids[i] = c.CoverID
	}
	covers, err := s.q.ListOwnPhotoAssets(r.Context(), db.ListOwnPhotoAssetsParams{UserID: uid, Ids: ids})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	rows, err := s.photoRows(r, covers)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	byID := make(map[string]photoRowDTO, len(rows))
	for _, row := range rows {
		byID[row.ID] = row
	}
	for _, c := range cs {
		cover, found := byID[db.UUIDString(c.CoverID)]
		if !found {
			continue // gone between the two reads
		}
		d := photoClusterDTO{Lat: c.Lat, Lon: c.Lon, Count: c.Count, Cell: fmt.Sprintf("%d.%d", c.X, c.Y), Cover: cover}
		if c.PlaceID != 0 {
			if n := s.photos.PlaceName(pgtype.Int4{Int32: c.PlaceID, Valid: true}, photoLang(r)); n != "" {
				d.Place = &n
			}
		}
		if c.CountryCode != "" {
			cc := c.CountryCode
			d.Country = &cc
		}
		out = append(out, d)
	}
	writeJSON(w, http.StatusOK, out)
}

// GET /me/photos/v1/map/bounds — {w,s,e,n} around every card with a position (the map's
// first view), or null when there is none.
func (s *Server) handlePhotoMapBounds(w http.ResponseWriter, r *http.Request) {
	if !s.photosReady(w) {
		return
	}
	uid, ok := photosUser(w, r)
	if !ok {
		return
	}
	if !s.photosOn(r, uid) {
		writeJSON(w, http.StatusOK, nil)
		return
	}
	b, err := s.q.GetPhotoMapBounds(r.Context(), uid)
	if errors.Is(err, pgx.ErrNoRows) {
		writeJSON(w, http.StatusOK, nil)
		return
	} else if err != nil {
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	writeJSON(w, http.StatusOK, map[string]float64{"w": b.West, "s": b.South, "e": b.East, "n": b.North})
}
