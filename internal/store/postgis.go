package store

import (
	"context"
	"errors"
	"log/slog"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// PostGISExtensions are the PostGIS extensions UpdatePostGIS keeps up to
// date, in the order it updates them: postgis first, as the others depend on
// it.
var PostGISExtensions = []string{"postgis", "postgis_raster", "postgis_topology", "postgis_sfcgal", "postgis_tiger_geocoder"}

// Conn is what UpdatePostGIS needs of a connection: a *pgx.Conn or a
// *pgxpool.Pool.
type Conn interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}

// UpdatePostGIS brings each installed PostGIS extension up to the version the
// database image provides (R84c). A new image updates PostGIS's library, but
// a database keeps the extension version it registered until ALTER EXTENSION
// ... UPDATE runs, which the image does only for a new database. Extensions
// that aren't installed are left alone, and one that's already current runs
// nothing. An update that fails is logged as a warning, not returned: it
// mustn't stop the API starting.
func UpdatePostGIS(ctx context.Context, conn Conn, log *slog.Logger) {
	rows, err := conn.Query(ctx, `
SELECT e.extname, e.extversion, a.default_version
FROM pg_extension e LEFT JOIN pg_available_extensions a ON a.name = e.extname
WHERE e.extname = ANY($1)`, PostGISExtensions)
	if err != nil {
		log.Warn("couldn't check the PostGIS extensions' versions", "error", SafeError(err))
		return
	}
	type ext struct {
		from string
		to   *string // nil: the image doesn't provide it
	}
	installed := map[string]ext{}
	for rows.Next() {
		var e ext
		var name string
		if err := rows.Scan(&name, &e.from, &e.to); err != nil {
			rows.Close()
			log.Warn("couldn't check the PostGIS extensions' versions", "error", SafeError(err))
			return
		}
		installed[name] = e
	}
	if err := rows.Err(); err != nil {
		log.Warn("couldn't check the PostGIS extensions' versions", "error", SafeError(err))
		return
	}
	for _, name := range PostGISExtensions {
		e, ok := installed[name]
		if !ok {
			continue
		}
		if e.to == nil {
			// PostGIS 3.7 no longer ships postgis_tiger_geocoder, for one.
			log.Warn("a PostGIS extension is installed that the database image no longer provides, so it can't be updated; PagerArchive doesn't use it (docs/deploy.md)", "extension", name, "version", e.from)
			continue
		}
		if e.from == *e.to {
			continue
		}
		if _, err := conn.Exec(ctx, "ALTER EXTENSION "+pgx.Identifier{name}.Sanitize()+" UPDATE"); err != nil {
			// Messages are literals (R73): the extension and the fix are attributes.
			fix := "ALTER EXTENSION " + name + " UPDATE"
			var pgErr *pgconn.PgError
			if errors.As(err, &pgErr) && pgErr.Code == "42501" {
				log.Warn("couldn't update a PostGIS extension, as the API's database role doesn't own it: run the fix as the extension's owner (docs/deploy.md)",
					"extension", name, "from", e.from, "to", *e.to, "fix", fix, "error", SafeError(err))
			} else {
				log.Warn("couldn't update a PostGIS extension: run the fix as the extension's owner (docs/deploy.md)",
					"extension", name, "from", e.from, "to", *e.to, "fix", fix, "error", SafeError(err))
			}
			continue
		}
		log.Info("PostGIS extension updated", "extension", name, "from", e.from, "to", *e.to)
	}
}
