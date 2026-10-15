#!/bin/sh
# Builds deploy/example-map/example-map.pmtiles, the tiny made-up test map
# (ADR 0005), from the GeoJSON that scripts/example-map/main.go writes.
# Layer names and properties follow the basemap schema the map style reads
# (earth, roads, places). Needs tippecanoe 2.x.
set -eu
cd "$(dirname "$0")/../../deploy/example-map"
tippecanoe --quiet --force -o example-map.pmtiles -Z 6 -z 15 --no-tile-compression \
	-n "PagerArchive example map" -A "Example map, made up for PagerArchive" \
	-L earth:earth.geojson -L roads:roads.geojson -L places:places.geojson
ls -l example-map.pmtiles
