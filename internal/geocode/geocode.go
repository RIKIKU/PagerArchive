// Package geocode places messages on the map (R24-R26, ADR 0005): the
// address against the local address dataset first, then the grid
// reference, and flags the two disagreeing by more than 1 km. Page text is
// never logged (P1).
package geocode

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/RIKIKU/PagerArchive/internal/geo"
	"github.com/RIKIKU/PagerArchive/internal/store"
	"github.com/RIKIKU/PagerArchive/internal/store/db"
	"github.com/RIKIKU/PagerArchive/internal/streets"
)

// MismatchMetres is the R26 distance (owner, 2026-10-13).
const MismatchMetres = 1000

// Sources of a location.
const (
	FromAddress = "address" // the house number was found
	FromStreet  = "street"  // no house number, or not in the dataset: approximate
	FromGrid    = "grid"    // the grid reference (R25)
)

// Input is what a message offers for placing it: its parsed fields.
type Input struct {
	Address, Locality, Description, GridRef string
}

// Result is where a message goes. Point is nil when it can't be placed.
type Result struct {
	Point    *geo.Point
	Source   string
	Mismatch bool // R26, admins only
}

// Geocoder places messages. Index is the street index built from the same
// dataset that was loaded into the database. GridSystem is the network's
// grid reference system ("" means grid references aren't used).
type Geocoder struct {
	Index      *streets.Index
	GridSystem string
}

// Locate places one message. q runs the lookups (a transaction or the pool).
func (g *Geocoder) Locate(ctx context.Context, q *db.Queries, in Input) (Result, error) {
	var res Result
	place, placed := streets.Place{}, false
	var addr *geo.Point
	if in.Address != "" {
		if place, placed = g.Index.ParseAddress(in.Address, in.Locality); placed {
			var err error
			if addr, res.Source, err = g.addressPoint(ctx, q, place); err != nil {
				return Result{}, err
			}
		}
	}

	var grid *geo.Point
	if in.GridRef != "" && g.GridSystem == geo.GridSystem6InLocalitySquare {
		// The locality comes from the parsed address, else the locality
		// field, else the end of the description when no address was split
		// from it (ADR 0005).
		locality := place.Locality
		if locality == "" {
			locality = streets.NormaliseLocality(in.Locality)
		}
		if locality == "" && in.Address == "" {
			locality, _ = g.Index.LocalityAtEnd(in.Description)
		}
		if locality != "" {
			ext, err := q.LocalityExtent(ctx, locality)
			switch {
			case errors.Is(err, pgx.ErrNoRows):
			case err != nil:
				return Result{}, err
			default:
				if p, ok := geo.CompleteGrid(in.GridRef, geo.Extent{MinLon: ext.MinLon, MinLat: ext.MinLat, MaxLon: ext.MaxLon, MaxLat: ext.MaxLat}); ok {
					grid = &p
				}
			}
		}
	}

	switch {
	case addr != nil: // the address first (R24), the grid only to check it (R26)
		res.Point = addr
		if grid != nil {
			d := geo.Distance(*addr, *grid)
			if res.Source == FromStreet {
				m, err := q.DistanceToStreet(ctx, db.DistanceToStreetParams{Locality: place.Locality, Street: place.Street, Lon: grid.Lon, Lat: grid.Lat})
				if err != nil {
					return Result{}, err
				}
				if m >= 0 {
					d = m
				}
			}
			res.Mismatch = d > MismatchMetres
		}
	case grid != nil:
		res.Point, res.Source = grid, FromGrid
	default:
		res.Source = ""
	}
	return res, nil
}

// addressPoint finds the house number (as written, then simpler forms),
// else the street's point, marked approximate (section 14).
func (g *Geocoder) addressPoint(ctx context.Context, q *db.Queries, p streets.Place) (*geo.Point, string, error) {
	for _, n := range streets.NumberCandidates(p.Number) {
		row, err := q.AddressPoint(ctx, db.AddressPointParams{Locality: p.Locality, Street: p.Street, Number: n})
		if err == nil {
			return &geo.Point{Lon: row.Lon, Lat: row.Lat}, FromAddress, nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return nil, "", err
		}
	}
	row, err := q.StreetPoint(ctx, db.StreetPointParams{Locality: p.Locality, Street: p.Street})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, "", nil // listed street with no coordinates (a street fixture)
	}
	if err != nil {
		return nil, "", err
	}
	return &geo.Point{Lon: row.Lon, Lat: row.Lat}, FromStreet, nil
}

// SetLocationParams turns a result into the update for message id.
func SetLocationParams(id int64, r Result) db.SetLocationParams {
	p := db.SetLocationParams{ID: id, Mismatch: r.Mismatch}
	if r.Point != nil {
		lon, lat, src := r.Point.Lon, r.Point.Lat, r.Source
		p.Lon, p.Lat, p.Source = &lon, &lat, &src
	}
	return p
}

const datasetHashKey = "dataset_hash"

// DatasetHash identifies a dataset file together with the street types that
// normalise it, streaming the file so a large one isn't held in memory.
func DatasetHash(path string, streetTypes map[string]string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	types, _ := json.Marshal(streetTypes) // map keys are sorted
	fmt.Fprintf(h, "\nstreet types %s\n", types)
	return hex.EncodeToString(h.Sum(nil)), nil
}

// LoadStats counts what Load stored.
type LoadStats struct {
	Skipped                        bool // the dataset was already loaded
	Addresses, Streets, Localities int64
}

// Load puts the dataset's points into the database if the dataset (or its
// street types) changed since the last load. It runs in one transaction, so
// a failed load leaves the previous dataset in place.
func Load(ctx context.Context, st *store.Store, path string, ix *streets.Index, hash string) (LoadStats, error) {
	if old, err := st.GetSetting(ctx, datasetHashKey); err == nil && old == hash {
		return LoadStats{Skipped: true}, nil
	}
	var stats LoadStats
	err := pgx.BeginFunc(ctx, st.Pool, func(tx pgx.Tx) error {
		for _, sql := range []string{
			`TRUNCATE address_points, street_points, localities`,
			`CREATE TEMP TABLE load_points (locality text, street text, number text, lon float8, lat float8) ON COMMIT DROP`,
		} {
			if _, err := tx.Exec(ctx, sql); err != nil {
				return err
			}
		}
		rows := make(chan []any, 1024)
		scanErr := make(chan error, 1)
		go func() {
			defer close(rows)
			_, err := streets.Scan(path, func(a streets.Address) error {
				select {
				case rows <- []any{streets.NormaliseLocality(a.Locality), ix.NormaliseStreet(a.Street), strings.ToUpper(a.Number), a.Lon, a.Lat}:
					return nil
				case <-ctx.Done():
					return ctx.Err()
				}
			})
			scanErr <- err
		}()
		_, copyErr := tx.CopyFrom(ctx, pgx.Identifier{"load_points"}, []string{"locality", "street", "number", "lon", "lat"}, &chanSource{c: rows})
		for range rows { // drain, so the scanner can finish
		}
		if err := <-scanErr; err != nil {
			return err
		}
		if copyErr != nil {
			return copyErr
		}
		steps := []struct {
			sql   string
			count *int64
		}{
			{`INSERT INTO address_points (locality, street, number, geom)
			  SELECT locality, street, number, ST_SetSRID(ST_MakePoint(lon, lat), 4326)::geography
			  FROM load_points WHERE number <> ''`, &stats.Addresses},
			// The dataset's own street points win.
			{`INSERT INTO street_points (locality, street, geom)
			  SELECT DISTINCT ON (locality, street) locality, street, ST_SetSRID(ST_MakePoint(lon, lat), 4326)::geography
			  FROM load_points WHERE number = '' ORDER BY locality, street`, nil},
			// Otherwise the address point nearest the centre of the street's
			// addresses, so the point is on the street even if it curves.
			{`INSERT INTO street_points (locality, street, geom)
			  SELECT DISTINCT ON (a.locality, a.street) a.locality, a.street, a.geom
			  FROM address_points a
			  JOIN (SELECT locality, street, ST_Centroid(ST_Collect(geom::geometry)) AS c
			        FROM address_points GROUP BY locality, street) s USING (locality, street)
			  WHERE NOT EXISTS (SELECT 1 FROM street_points p WHERE p.locality = a.locality AND p.street = a.street)
			  ORDER BY a.locality, a.street, a.geom::geometry <-> s.c`, nil},
			{`INSERT INTO localities (name, min_lon, min_lat, max_lon, max_lat)
			  SELECT locality, ST_XMin(e), ST_YMin(e), ST_XMax(e), ST_YMax(e)
			  FROM (SELECT locality, ST_Extent(geom::geometry)::box3d AS e
			        FROM (SELECT locality, geom FROM address_points UNION ALL SELECT locality, geom FROM street_points) u
			        GROUP BY locality) x`, &stats.Localities},
		}
		for _, s := range steps {
			tag, err := tx.Exec(ctx, s.sql)
			if err != nil {
				return err
			}
			if s.count != nil {
				*s.count = tag.RowsAffected()
			}
		}
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM street_points`).Scan(&stats.Streets); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `INSERT INTO settings (key, value) VALUES ($1, $2)
			ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value`, datasetHashKey, hash)
		return err
	})
	if err != nil {
		return LoadStats{}, fmt.Errorf("load address dataset: %s", safe(err))
	}
	if _, err := st.Pool.Exec(ctx, `ANALYZE address_points, street_points, localities`); err != nil {
		return stats, fmt.Errorf("analyze address dataset: %s", safe(err))
	}
	return stats, nil
}

// safe describes an error without database message text (P1). Dataset
// errors (file, line, column) carry no page text and are kept.
func safe(err error) string {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return store.SafeError(err)
	}
	return err.Error()
}

// chanSource feeds CopyFrom from a channel.
type chanSource struct {
	c   <-chan []any
	row []any
}

func (s *chanSource) Next() bool {
	r, ok := <-s.c
	s.row = r
	return ok
}
func (s *chanSource) Values() ([]any, error) { return s.row, nil }
func (s *chanSource) Err() error             { return nil }
