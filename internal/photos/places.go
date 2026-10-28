package photos

import (
	"context"
	"sync"
	"sync/atomic"

	"github.com/jackc/pgx/v5/pgtype"

	"discodrive/internal/db"
)

// SwappableGeocoder is a Geocoder whose data can arrive after the indexer was built (the
// GeoNames files are downloaded on an admin's request). Until Swap is called it reports
// !Ready and Lookup misses.
type SwappableGeocoder struct{ cur atomic.Pointer[Geocoder] }

// Swap installs the geocoder (nil removes it).
func (s *SwappableGeocoder) Swap(g Geocoder) {
	if g == nil {
		s.cur.Store(nil)
		return
	}
	s.cur.Store(&g)
}

// Ready reports whether a geocoder is installed.
func (s *SwappableGeocoder) Ready() bool { return s.cur.Load() != nil }

// Lookup implements Geocoder.
func (s *SwappableGeocoder) Lookup(lat, lon float64) (int32, string, bool) {
	if g := s.cur.Load(); g != nil {
		return (*g).Lookup(lat, lon)
	}
	return 0, "", false
}

// Name implements Geocoder.
func (s *SwappableGeocoder) Name(id int32, lang string) string {
	if g := s.cur.Load(); g != nil {
		return (*g).Name(id, lang)
	}
	return ""
}

var backfillMu sync.Mutex

// BackfillPlaces fills place and country of the assets that have coordinates but were
// indexed before a geocoder existed (place_id IS NULL), in batches of 500. A miss is
// stored as 0 like the indexer does. It returns the number of rows filled.
func BackfillPlaces(ctx context.Context, q *db.Queries, g Geocoder) (int, error) {
	backfillMu.Lock()
	defer backfillMu.Unlock()
	n := 0
	for {
		rows, err := q.ListPhotoAssetsWithoutPlace(ctx, 500)
		if err != nil || len(rows) == 0 {
			return n, err
		}
		for _, a := range rows {
			if !a.Lat.Valid || !a.Lon.Valid {
				continue
			}
			id, country, ok := g.Lookup(a.Lat.Float64, a.Lon.Float64)
			if !ok {
				id, country = 0, ""
			}
			if err := q.SetPhotoPlace(ctx, db.SetPhotoPlaceParams{ID: a.ID,
				PlaceID: pgtype.Int4{Int32: id, Valid: true}, CountryCode: pgtype.Text{String: country, Valid: true}}); err != nil {
				return n, err
			}
			n++
		}
	}
}
