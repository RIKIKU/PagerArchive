-- +goose Up
-- R88 (ADR 0035): the network profile's areas, layers of named polygons such
-- as the districts a network's agencies are organised in, and the area of
-- each layer each located page is in. The API loads a layer whenever its
-- file changes (areas.Load): geom is the file's polygons, merged by name;
-- parts are geom cut into pieces of at most 256 points, so finding the area
-- a point is in reads a few small pieces through the index rather than a
-- whole outline; outline is geom simplified for the map, and label_point
-- where its label goes.
CREATE TABLE areas (
    layer       text NOT NULL,
    name        text NOT NULL,
    geom        geometry(MultiPolygon, 4326) NOT NULL,
    outline     geometry(Geometry, 4326) NOT NULL,
    label_point geometry(Point, 4326) NOT NULL,
    PRIMARY KEY (layer, name)
);

CREATE TABLE area_parts (
    layer text NOT NULL,
    name  text NOT NULL,
    geom  geometry(Geometry, 4326) NOT NULL,
    FOREIGN KEY (layer, name) REFERENCES areas (layer, name) ON DELETE CASCADE
);
CREATE INDEX area_parts_geom_idx ON area_parts USING gist (geom);

-- The area of each layer a page is in, derived from where it's placed
-- (R88a): beside the page rather than a column of messages, which holds what
-- the page said (P3), and so that filling it for the pages stored before
-- writes no page again (ADR 0034 §2). One row per page and layer it's in an
-- area of; it goes with the page (retention, R45). Rows are kept when a
-- layer's areas change, until they're worked out again (areas.Backfill): no
-- foreign key to areas.
CREATE TABLE message_areas (
    message_id bigint NOT NULL REFERENCES messages (id) ON DELETE CASCADE,
    layer      text NOT NULL,
    area       text NOT NULL,
    PRIMARY KEY (message_id, layer)
);

-- The area of a layer a point is in, or NULL: in none of its areas, on an
-- area's edge (between two areas, or at the edge of one), or in two at once
-- (R88a). Pieces the point touches give the candidates; inside one piece is
-- inside its area, and a point only on a piece's own edge, where an area was
-- cut, is checked against the whole area.
-- +goose StatementBegin
CREATE FUNCTION area_at(l text, p geography) RETURNS text LANGUAGE sql STABLE STRICT PARALLEL SAFE AS $$
    WITH hit AS (
        SELECT ap.name, ST_Contains(ap.geom, p::geometry) AS inside
        FROM area_parts ap WHERE ap.layer = l AND ST_Intersects(ap.geom, p::geometry)
    ), one AS (
        SELECT min(hit.name) AS name, bool_or(hit.inside) AS inside FROM hit HAVING count(DISTINCT hit.name) = 1
    )
    SELECT one.name FROM one
    WHERE one.inside OR EXISTS (SELECT 1 FROM areas a WHERE a.layer = l AND a.name = one.name AND ST_Contains(a.geom, p::geometry))
$$;
-- +goose StatementEnd

-- +goose Down
DROP FUNCTION area_at(text, geography);
DROP TABLE message_areas;
DROP TABLE area_parts;
DROP TABLE areas;
DELETE FROM settings WHERE key LIKE 'areas_hash:%' OR key LIKE 'message_areas_backfill:%';
