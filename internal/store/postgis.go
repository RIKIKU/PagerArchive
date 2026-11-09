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
FROM pg_extension e JOIN pg_available_extensions a ON a.name = e.extname
WHERE e.extname = ANY($1)`, PostGISExtensions)
	if err != nil {
		log.Warn("couldn't check the PostGIS extensions' versions", "error", SafeError(err))
		return
	}
	type ext struct{ from, to string }
	installed := map[string]ext{}
	for rows.Next() {
		var name, from string
		var to *string
		if err := rows.Scan(&name, &from, &to); err != nil {
			rows.Close()
			log.Warn("couldn't check the PostGIS extensions' versions", "error", SafeError(err))
			return
		}
		if to != nil {
			installed[name] = ext{from, *to}
		}
	}
	if err := rows.Err(); err != nil {
		log.Warn("couldn't check the PostGIS extensions' versions", "error", SafeError(err))
		return
	}
	for _, name := range PostGISExtensions {
		e, ok := installed[name]
		if !ok || e.from == e.to {
			continue
		}
		if _, err := conn.Exec(ctx, "ALTER EXTENSION "+pgx.Identifier{name}.Sanitize()+" UPDATE"); err != nil {
			msg := "couldn't update a PostGIS extension: run ALTER EXTENSION " + name + " UPDATE as its owner (docs/deploy.md)"
			var pgErr *pgconn.PgError
			if errors.As(err, &pgErr) && pgErr.Code == "42501" {
				msg = "couldn't update a PostGIS extension, as the API's database role doesn't own it: run ALTER EXTENSION " + name + " UPDATE as its owner (docs/deploy.md)"
			}
			log.Warn(msg, "extension", name, "from", e.from, "to", e.to, "error", SafeError(err))
			continue
		}
		log.Info("PostGIS extension updated", "extension", name, "from", e.from, "to", e.to)
	}
}
