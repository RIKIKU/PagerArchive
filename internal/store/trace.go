package store

import (
	"context"
	_ "embed"
	"regexp"
	"strings"

	"github.com/jackc/pgx/v5"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	"github.com/RIKIKU/PagerArchive/internal/telemetry"
)

//go:embed queries.sql
var queriesSQL string

var (
	sqlcName = regexp.MustCompile(`(?m)^-- name: ([A-Za-z0-9_]+) :`)
	// Statements outside sqlc are named by their first word.
	rawStatements = []string{"SELECT", "INSERT", "UPDATE", "DELETE", "VACUUM", "ANALYZE", "LISTEN", "CREATE", "DROP", "TRUNCATE", "BEGIN", "COMMIT", "ROLLBACK"}
)

func init() { telemetry.AllowSpanNames(QueryNames()...) }

// QueryNames are the span names database calls can have: each sqlc
// query's name, and "SQL <first word>" for the few statements outside sqlc.
func QueryNames() []string {
	var out []string
	for _, m := range sqlcName.FindAllStringSubmatch(queriesSQL, -1) {
		out = append(out, m[1])
	}
	for _, w := range rawStatements {
		out = append(out, "SQL "+w)
	}
	return append(out, "SQL")
}

// queryName names a statement without any of its values (R70): sqlc's
// name for its queries, which start "-- name: InsertMessage :one".
func queryName(sql string) string {
	if rest, ok := strings.CutPrefix(sql, "-- name: "); ok {
		if name, _, ok := strings.Cut(rest, " "); ok {
			return name
		}
	}
	word, _, _ := strings.Cut(strings.TrimSpace(sql), " ")
	word = strings.ToUpper(word)
	for _, w := range rawStatements {
		if word == w {
			return "SQL " + w
		}
	}
	return "SQL"
}

// Tracer makes a span for each database call made inside a traced piece
// of work (a request, ingest, a nightly run), named by its query and
// never carrying the SQL text, its arguments or the error's message
// (R70, R72). Calls outside one (idle polling) make none.
type Tracer struct{ T trace.Tracer }

// TraceQueryStart is pgx.QueryTracer's.
func (t Tracer) TraceQueryStart(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryStartData) context.Context {
	if !trace.SpanFromContext(ctx).IsRecording() {
		return ctx
	}
	name := queryName(data.SQL)
	ctx, _ = telemetry.Or(t.T).Start(ctx, name, trace.WithSpanKind(trace.SpanKindClient),
		trace.WithAttributes(attribute.String("db.system.name", "postgresql"), attribute.String("db.operation.name", name)))
	return ctx
}

// TraceQueryEnd is pgx.QueryTracer's.
func (t Tracer) TraceQueryEnd(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryEndData) {
	span := trace.SpanFromContext(ctx)
	if !span.IsRecording() {
		return
	}
	if data.Err != nil && data.Err != pgx.ErrNoRows {
		span.SetAttributes(attribute.String("error.type", SafeError(data.Err)))
		span.SetStatus(codes.Error, "")
	}
	span.End()
}
