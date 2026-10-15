package streets

import (
	"compress/gzip"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestParseAddress(t *testing.T) {
	ix := NewIndex([]Entry{
		{"FICTION ST", "MADEUPTON"},
		{"SAMPLE RD", "MADEUPTON"},
		{"Fiction Street", "North Testville"},
	}, types)
	tests := []struct {
		address, locality string
		want              Place
		ok                bool
	}{
		// Locality as its own field (the example profile).
		{"12 FICTION ST", "MADEUPTON", Place{"12", "FICTION ST", "MADEUPTON"}, true},
		{"sample road", "Madeupton", Place{"", "SAMPLE RD", "MADEUPTON"}, true},
		// Locality at the end of the address (R24).
		{"3/5 FICTION STREET NORTH TESTVILLE", "", Place{"3/5", "FICTION ST", "NORTH TESTVILLE"}, true},
		// Both: the address repeats the locality.
		{"12A FICTION ST MADEUPTON", "MADEUPTON", Place{"12A", "FICTION ST", "MADEUPTON"}, true},
		// Street not listed in that locality.
		{"12 SAMPLE RD", "NORTH TESTVILLE", Place{}, false},
		{"12 SAMPLE RD NORTH TESTVILLE", "", Place{}, false},
		// A number alone is no address.
		{"12", "MADEUPTON", Place{}, false},
	}
	for _, tt := range tests {
		got, ok := ix.ParseAddress(tt.address, tt.locality)
		if got != tt.want || ok != tt.ok {
			t.Errorf("ParseAddress(%q, %q) = %+v, %v; want %+v, %v", tt.address, tt.locality, got, ok, tt.want, tt.ok)
		}
	}
}

func TestLocalityAtEnd(t *testing.T) {
	ix := NewIndex([]Entry{{"FICTION ST", "TESTVILLE"}, {"FICTION ST", "NORTH TESTVILLE"}}, types)
	for text, want := range map[string]string{
		"SMOKE 9 NEW ESTATE CT NORTH TESTVILLE": "NORTH TESTVILLE", // longest wins
		"SMOKE 9 NEW ESTATE CT TESTVILLE":       "TESTVILLE",
		"SMOKE NEAR TESTVILLE OVAL":             "",
	} {
		if got, ok := ix.LocalityAtEnd(text); got != want || ok != (want != "") {
			t.Errorf("LocalityAtEnd(%q) = %q, %v; want %q", text, got, ok, want)
		}
	}
}

func TestNumberCandidates(t *testing.T) {
	for in, want := range map[string][]string{
		"12":    {"12"},
		"12a":   {"12A", "12"},
		"2/12":  {"2/12", "12"},
		"12-14": {"12-14", "12"},
		"3/5B":  {"3/5B", "5B", "5"},
		"":      {},
	} {
		if got := NumberCandidates(in); !reflect.DeepEqual(got, want) {
			t.Errorf("NumberCandidates(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestScanCSV(t *testing.T) {
	dir := t.TempDir()
	body := string(rune(0xFEFF)) + "lat,lon,number,street,locality,extra\n" +
		"-34.83,-140.86,12,FICTION ST,MADEUPTON,x\n" +
		"-34.84,-140.87,14,FICTION ST,MADEUPTON,x\n" +
		"-34.85,-140.88,,SAMPLE RD,MADEUPTON,x\n"
	plain := filepath.Join(dir, "a.csv")
	if err := os.WriteFile(plain, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	gz := filepath.Join(dir, "a.csv.gz")
	f, _ := os.Create(gz)
	zw := gzip.NewWriter(f)
	zw.Write([]byte(body))
	zw.Close()
	f.Close()
	for _, path := range []string{plain, gz} {
		var got []Address
		d, err := Scan(path, func(a Address) error { got = append(got, a); return nil })
		if err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		want := []Entry{{"FICTION ST", "MADEUPTON"}, {"SAMPLE RD", "MADEUPTON"}}
		if !reflect.DeepEqual(d.Streets, want) {
			t.Errorf("%s: streets %v", path, d.Streets)
		}
		if len(got) != 3 || got[0] != (Address{"12", "FICTION ST", "MADEUPTON", -140.86, -34.83}) || got[2].Number != "" {
			t.Errorf("%s: addresses %+v", path, got)
		}
	}

	bad := filepath.Join(dir, "bad.csv")
	os.WriteFile(bad, []byte("number,street,locality,lon\n1,A ST,B,1\n"), 0o644)
	if _, err := Scan(bad, nil); err == nil || !strings.Contains(err.Error(), `missing column "lat"`) {
		t.Errorf("missing column: %v", err)
	}
	os.WriteFile(bad, []byte("number,street,locality,lon,lat\n1,A ST,B,east,1\n"), 0o644)
	if _, err := Scan(bad, nil); err == nil || !strings.Contains(err.Error(), "line 2") {
		t.Errorf("bad lon: %v", err)
	}
}

func TestScanExampleDataset(t *testing.T) {
	n := 0
	d, err := Scan("../../profiles/example/example-addresses.json", func(Address) error { n++; return nil })
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Streets) != 5 || n == 0 {
		t.Errorf("%d streets, %d addresses", len(d.Streets), n)
	}
}
