//go:build ignore

// Writes a made-up address dataset of real size for the A15 resource-budget
// run (ADR 0005): by default 3,000 localities, about 200,000 streets and
// 3.8 million addresses, as a gzipped CSV in the documented columns, plus
// the example network's own addresses so its pages still parse and place.
// Everything is invented and sits in open ocean. It also writes a sample of
// addresses for the load tool.
//
// usage: go run scripts/a15/dataset.go -o big.csv.gz -sample sample.txt [-localities 3000]
package main

import (
	"compress/gzip"
	"encoding/csv"
	"flag"
	"fmt"
	"math/rand/v2"
	"os"
	"strconv"

	"github.com/RIKIKU/PagerArchive/internal/streets"
)

var syllables = []string{"BAR", "WIN", "DOO", "KAL", "MOR", "TEN", "ROO", "GUM", "LIN", "DAL", "BER", "WAR", "NAN", "GIL", "YAR", "COO", "MIL", "TOR", "BEL", "KIN"}
var types = []string{"ST", "RD", "AVE", "CT", "DR", "PL", "CRES", "LANE", "WAY", "TCE"}

func word(r *rand.Rand) string {
	w := ""
	for i := 0; i < 2+r.IntN(2); i++ {
		w += syllables[r.IntN(len(syllables))]
	}
	return w
}

func main() {
	out := flag.String("o", "a15-addresses.csv.gz", "output CSV (gzipped)")
	sample := flag.String("sample", "a15-sample.txt", "sample addresses for the load tool")
	nLoc := flag.Int("localities", 3000, "localities")
	flag.Parse()
	r := rand.New(rand.NewPCG(4, 15))

	f, err := os.Create(*out)
	check(err)
	zw := gzip.NewWriter(f)
	w := csv.NewWriter(zw)
	check(w.Write(streets.CSVColumns))
	sf, err := os.Create(*sample)
	check(err)

	// The example network's addresses, so its golden rows still place.
	ex := 0
	_, err = streets.Scan("profiles/example/example-addresses.json", func(a streets.Address) error {
		ex++
		return w.Write([]string{a.Number, a.Street, a.Locality, ftoa(a.Lon), ftoa(a.Lat)})
	})
	check(err)

	seenLoc := map[string]bool{}
	nStreets, nAddr := 0, ex
	for l := 0; l < *nLoc; l++ {
		loc := word(r)
		if r.IntN(10) < 3 {
			loc = word(r) + " " + loc
		}
		if seenLoc[loc] {
			l--
			continue
		}
		seenLoc[loc] = true
		// Localities on a grid across the ocean east of the example.
		baseLon, baseLat := -139.5+float64(l%60)*0.1, -36.0+float64(l/60)*0.06
		seenSt := map[string]bool{}
		nStreetsHere := 55 + r.IntN(25) // about 67 streets a locality
		for s := 0; s < nStreetsHere; s++ {
			st := word(r) + " " + types[r.IntN(len(types))]
			if seenSt[st] {
				continue
			}
			seenSt[st] = true
			nStreets++
			lon, lat := baseLon+r.Float64()*0.08, baseLat+r.Float64()*0.05
			dLon, dLat := (r.Float64()-0.5)*0.0004, (r.Float64()-0.5)*0.0004
			count := 8 + r.IntN(23) // about 19 addresses a street
			for n := 1; n <= count; n++ {
				check(w.Write([]string{strconv.Itoa(n), st, loc, ftoa(lon + float64(n)*dLon), ftoa(lat + float64(n)*dLat)}))
				nAddr++
				if r.IntN(4000) == 0 {
					fmt.Fprintf(sf, "%d %s|%s\n", n, st, loc)
				}
			}
		}
	}
	w.Flush()
	check(w.Error())
	check(zw.Close())
	check(f.Close())
	check(sf.Close())
	fmt.Printf("%s: %d localities, %d streets, %d addresses\n", *out, len(seenLoc)+2, nStreets, nAddr)
}

func ftoa(v float64) string { return strconv.FormatFloat(v, 'f', 6, 64) }

func check(err error) {
	if err != nil {
		panic(err)
	}
}
