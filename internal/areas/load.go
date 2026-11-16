package areas

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/RIKIKU/PagerArchive/internal/store"
)

const (
	// hashPrefix starts each layer's settings row holding the hash of the
	// areas loaded ("areas_hash:districts").
	hashPrefix = "areas_hash:"
	// backfillPrefix starts each layer's settings row holding how far
	// Backfill has got: the last message id it worked out, then "done".
	backfillPrefix = "message_areas_backfill:"
	// partPoints is the most points a piece of an area has (ST_Subdivide).
	partPoints = 256
	// MapBudget is the most one layer's outlines may weigh as GeoJSON, in
	// bytes (R88c): the smallest tolerance from firstTolerance, doubling,
	// that brings them under it is the one used.
	MapBudget = 400_000
	// firstTolerance is the first simplification tried, in degrees (about
	// 10 m), and maxTries how many doublings are tried after it.
	firstTolerance = 0.0001
	maxTries       = 12
	// mapDigits is how many decimals the map's coordinates have (about 10 m).
	mapDigits = 4
)

// LoadStats says what Load did.
type LoadStats struct {
	Skipped bool // the same areas were loaded already
	Areas   int
	Parts   int64
	// Tolerance is the outlines' simplification in degrees, and MapBytes
	// what they weigh as GeoJSON.
	Tolerance float64
	MapBytes  int64
}

// Load puts the file's areas into the database as the layer named layer, if
// they changed since that layer's last load (R88d), in one transaction, so a
// failed load leaves the previous ones in place. Other layers aren't
// touched. Changed areas start the layer's Backfill again from the first
// page: every located page's area of this layer is worked out again, in the
// background. Rows already there stay until then.
func Load(ctx context.Context, st *store.Store, layer string, f *File) (LoadStats, error) {
	if old, err := st.GetSetting(ctx, hashPrefix+layer); err == nil && old == f.Hash {
		return LoadStats{Skipped: true}, nil
	}
	stats := LoadStats{Areas: len(f.Areas)}
	err := pgx.BeginFunc(ctx, st.Pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `DELETE FROM areas WHERE layer = $1`, layer); err != nil {
			return err
		}
		for _, a := range f.Areas {
			geoms := make([]string, len(a.Geometries))
			for i, g := range a.Geometries {
				geoms[i] = string(g)
			}
			// The file's polygons, made valid and merged by name. The outline
			// and its label point are set below.
			if _, err := tx.Exec(ctx, `INSERT INTO areas (layer, name, geom, outline, label_point)
				SELECT $1, $2, g, g, ST_PointOnSurface(g) FROM (
					SELECT ST_Multi(ST_CollectionExtract(ST_MakeValid(ST_Union(ST_SetSRID(ST_GeomFromGeoJSON(x), 4326))), 3)) AS g
					FROM unnest($3::text[]) AS x
				) s`, layer, a.Name, geoms); err != nil {
				return fmt.Errorf("area %q: %w", a.Name, err)
			}
		}
		tag, err := tx.Exec(ctx, `INSERT INTO area_parts (layer, name, geom) SELECT layer, name, ST_Subdivide(geom, $2) FROM areas WHERE layer = $1`, layer, partPoints)
		if err != nil {
			return err
		}
		stats.Parts = tag.RowsAffected()
		if stats.Tolerance, stats.MapBytes, err = outline(ctx, tx, layer); err != nil {
			return err
		}
		// Where each label goes: the middle of the outline's widest part.
		if _, err := tx.Exec(ctx, `UPDATE areas SET label_point = COALESCE(
				CASE WHEN NOT ST_IsEmpty(outline) THEN (ST_MaximumInscribedCircle(outline)).center END, ST_PointOnSurface(geom))
				WHERE layer = $1`, layer); err != nil {
			return err
		}
		for k, v := range map[string]string{hashPrefix + layer: f.Hash, backfillPrefix + layer: "0"} {
			if _, err := tx.Exec(ctx, `INSERT INTO settings (key, value) VALUES ($1, $2) ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value`, k, v); err != nil {
				return err
			}
		}
		return nil
	})
	return stats, err
}

// outline simplifies a layer's areas for the map: each one's outline is its
// polygons simplified together with the layer's others, so neighbours keep
// sharing their edges (ST_CoverageSimplify), or one at a time where the
// areas overlap and so aren't a coverage. The tolerance is the smallest that
// brings them under MapBudget.
func outline(ctx context.Context, tx pgx.Tx, layer string) (float64, int64, error) {
	coverage := true
	tol := firstTolerance
	var size int64
	for try := 0; ; try++ {
		simplify := `UPDATE areas a SET outline = s.g FROM (
			SELECT name, ST_CoverageSimplify(geom, $1) OVER () AS g FROM areas WHERE layer = $2) s
			WHERE a.layer = $2 AND s.name = a.name`
		if !coverage {
			simplify = `UPDATE areas SET outline = ST_SimplifyPreserveTopology(geom, $1) WHERE layer = $2`
		}
		err := pgx.BeginFunc(ctx, tx, func(sp pgx.Tx) error { // a savepoint: a failed try leaves the outlines as they were
			if _, err := sp.Exec(ctx, simplify, tol, layer); err != nil {
				return err
			}
			return sp.QueryRow(ctx, `SELECT COALESCE(sum(length(ST_AsGeoJSON(outline, $1))), 0) FROM areas WHERE layer = $2`, mapDigits, layer).Scan(&size)
		})
		if err != nil && coverage {
			// Not a coverage (overlapping areas), or GEOS without coverage
			// functions: one area at a time instead.
			coverage = false
			try--
			continue
		}
		if err != nil {
			return 0, 0, err
		}
		if size <= MapBudget || try >= maxTries {
			return tol, size, nil
		}
		tol *= 2
	}
}

// Loaded is the layers loaded before, by their settings rows.
func Loaded(ctx context.Context, st *store.Store) ([]string, error) {
	rows, err := st.Pool.Query(ctx, `SELECT substr(key, length($1) + 1) FROM settings WHERE starts_with(key, $1) ORDER BY key`, hashPrefix)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowTo[string])
}

// Clear removes every layer loaded but not in keep, with its areas and every
// page's area of it (R88d: the profile names it no longer). Other layers
// aren't touched. It does nothing if none is to go.
func Clear(ctx context.Context, st *store.Store, keep []string) error {
	loaded, err := Loaded(ctx, st)
	if err != nil {
		return err
	}
	kept := map[string]bool{}
	for _, k := range keep {
		kept[k] = true
	}
	for _, layer := range loaded {
		if kept[layer] {
			continue
		}
		if err := pgx.BeginFunc(ctx, st.Pool, func(tx pgx.Tx) error {
			for _, q := range []string{`DELETE FROM message_areas WHERE layer = $1`, `DELETE FROM areas WHERE layer = $1`} {
				if _, err := tx.Exec(ctx, q, layer); err != nil {
					return err
				}
			}
			_, err := tx.Exec(ctx, `DELETE FROM settings WHERE key = ANY($1)`, []string{hashPrefix + layer, backfillPrefix + layer})
			return err
		}); err != nil {
			return fmt.Errorf("removing areas %q: %w", layer, err)
		}
	}
	return nil
}

// loadedHash is the hash of a layer's areas loaded before, or "" if none.
func loadedHash(ctx context.Context, st *store.Store, layer string) (string, error) {
	h, err := st.GetSetting(ctx, hashPrefix+layer)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	return strings.TrimSpace(h), err
}
