package streets

import (
	"compress/gzip"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"github.com/RIKIKU/PagerArchive/schemas"
)

// Address is one located point from the dataset (R24). Number is empty for
// a street's own point, which a dataset may give instead of (or as well as)
// numbered addresses; it overrides the street centroid (ADR 0005).
type Address struct {
	Number   string  `json:"number"`
	Street   string  `json:"street"`
	Locality string  `json:"locality"`
	Lon      float64 `json:"lon"`
	Lat      float64 `json:"lat"`
}

// Dataset is an address dataset or a street fixture: its street list, with
// each street once per locality.
type Dataset struct {
	Attribution string
	Streets     []Entry
}

// CSVColumns are the columns a CSV dataset must have, in any order. Other
// columns are ignored.
var CSVColumns = []string{"number", "street", "locality", "lon", "lat"}

// LoadFile reads a dataset or street fixture for its street list.
func LoadFile(path string) (*Dataset, error) { return Scan(path, nil) }

// Scan reads a dataset and calls fn for each located address, if fn isn't
// nil. A path ending .csv or .csv.gz is read as CSV (for large datasets,
// streamed); anything else as JSON validated against
// schemas/address-dataset.schema.json.
func Scan(path string, fn func(Address) error) (*Dataset, error) {
	lower := strings.ToLower(path)
	var (
		d   *Dataset
		err error
	)
	if strings.HasSuffix(lower, ".csv") || strings.HasSuffix(lower, ".csv.gz") {
		d, err = scanCSV(path, strings.HasSuffix(lower, ".gz"), fn)
	} else {
		d, err = scanJSON(path, fn)
	}
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return d, nil
}

type jsonDataset struct {
	Attribution string `json:"attribution"`
	Streets     []struct {
		Street   string   `json:"street"`
		Locality string   `json:"locality"`
		Lon      *float64 `json:"lon"`
		Lat      *float64 `json:"lat"`
	} `json:"streets"`
	Addresses []Address `json:"addresses"`
}

func scanJSON(path string, fn func(Address) error) (*Dataset, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if probs := schemas.Validate(schemas.AddressDataset, data); len(probs) > 0 {
		return nil, fmt.Errorf("%s", probs[0])
	}
	var j jsonDataset
	if err := json.Unmarshal(data, &j); err != nil {
		return nil, err
	}
	b := newBuilder(j.Attribution)
	for _, s := range j.Streets {
		b.add(s.Street, s.Locality)
		if s.Lon != nil && s.Lat != nil && fn != nil {
			if err := fn(Address{Street: s.Street, Locality: s.Locality, Lon: *s.Lon, Lat: *s.Lat}); err != nil {
				return nil, err
			}
		}
	}
	for _, a := range j.Addresses {
		b.add(a.Street, a.Locality)
		if fn != nil {
			if err := fn(a); err != nil {
				return nil, err
			}
		}
	}
	return b.d, nil
}

func scanCSV(path string, gz bool, fn func(Address) error) (*Dataset, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var r io.Reader = f
	if gz {
		zr, err := gzip.NewReader(f)
		if err != nil {
			return nil, err
		}
		defer zr.Close()
		r = zr
	}
	cr := csv.NewReader(r)
	cr.ReuseRecord = true
	header, err := cr.Read()
	if err != nil {
		return nil, fmt.Errorf("header: %w", err)
	}
	col := map[string]int{}
	for i, h := range header {
		col[strings.ToLower(strings.TrimSpace(strings.TrimPrefix(h, string(rune(0xFEFF)))))] = i
	}
	for _, c := range CSVColumns {
		if _, ok := col[c]; !ok {
			return nil, fmt.Errorf("missing column %q (needs %s)", c, strings.Join(CSVColumns, ", "))
		}
	}
	b := newBuilder("")
	for line := 2; ; line++ {
		rec, err := cr.Read()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("line %d: %w", line, err)
		}
		a := Address{
			Number:   strings.TrimSpace(rec[col["number"]]),
			Street:   strings.TrimSpace(rec[col["street"]]),
			Locality: strings.TrimSpace(rec[col["locality"]]),
		}
		if a.Street == "" || a.Locality == "" {
			return nil, fmt.Errorf("line %d: street and locality are required", line)
		}
		var e1, e2 error
		a.Lon, e1 = strconv.ParseFloat(strings.TrimSpace(rec[col["lon"]]), 64)
		a.Lat, e2 = strconv.ParseFloat(strings.TrimSpace(rec[col["lat"]]), 64)
		if e1 != nil || e2 != nil || a.Lon < -180 || a.Lon > 180 || a.Lat < -90 || a.Lat > 90 {
			return nil, fmt.Errorf("line %d: lon and lat must be decimal degrees", line)
		}
		b.add(a.Street, a.Locality)
		if fn != nil {
			if err := fn(a); err != nil {
				return nil, err
			}
		}
	}
	return b.d, nil
}

// builder collects each street once per locality.
type builder struct {
	d    *Dataset
	seen map[Entry]bool
}

func newBuilder(attribution string) *builder {
	return &builder{d: &Dataset{Attribution: attribution, Streets: []Entry{}}, seen: map[Entry]bool{}}
}

func (b *builder) add(street, locality string) {
	e := Entry{Street: street, Locality: locality}
	if !b.seen[e] {
		b.seen[e] = true
		b.d.Streets = append(b.d.Streets, e)
	}
}
