-- +goose Up
-- R24-R26 (ADR 0005). The address dataset, loaded by the API whenever the
-- dataset file (or the street types that normalise it) changes. Streets and
-- localities are stored normalised, as the street index writes them.
CREATE TABLE address_points (
    locality text NOT NULL,
    street   text NOT NULL,
    number   text NOT NULL,
    geom     geography(Point, 4326) NOT NULL
);
CREATE INDEX address_points_key_idx ON address_points (locality, street, number);

-- One point per street per locality: the dataset's own point for the street
-- if it gives one, otherwise the address point nearest the centre of the
-- street's addresses (section 14: "the street's centroid").
CREATE TABLE street_points (
    locality text NOT NULL,
    street   text NOT NULL,
    geom     geography(Point, 4326) NOT NULL,
    PRIMARY KEY (locality, street)
);

-- Each locality's extent, from its points: R25 completes grid references
-- from the 100 km square(s) it overlaps.
CREATE TABLE localities (
    name    text PRIMARY KEY,
    min_lon double precision NOT NULL,
    min_lat double precision NOT NULL,
    max_lon double precision NOT NULL,
    max_lat double precision NOT NULL
);

-- Where a message is placed, and from what: its address, its street (no
-- house number, or the number isn't in the dataset: approximate) or its
-- grid reference.
ALTER TABLE messages
    ADD COLUMN location geography(Point, 4326),
    ADD COLUMN location_source text CHECK (location_source IN ('address', 'street', 'grid')),
    ADD CONSTRAINT messages_location_has_source CHECK ((location IS NULL) = (location_source IS NULL)),
    -- For reading: the same point as plain degrees.
    ADD COLUMN lon double precision GENERATED ALWAYS AS (ST_X(location::geometry)) STORED,
    ADD COLUMN lat double precision GENERATED ALWAYS AS (ST_Y(location::geometry)) STORED;
CREATE INDEX messages_located_idx ON messages (sent_at DESC) WHERE location IS NOT NULL;

-- +goose Down
DROP INDEX messages_located_idx;
ALTER TABLE messages DROP COLUMN lat;
ALTER TABLE messages DROP COLUMN lon;
ALTER TABLE messages DROP CONSTRAINT messages_location_has_source;
ALTER TABLE messages DROP COLUMN location_source;
ALTER TABLE messages DROP COLUMN location;
DROP TABLE localities;
DROP TABLE street_points;
DROP TABLE address_points;
