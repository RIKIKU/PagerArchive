//go:build ignore

// Load for the A15 resource-budget check (SPEC.md section 9, section 14):
// N live-update connections (viewers) and a page stream generated from the
// example formats at a given rate, posted as a client. It prints a summary
// and exits non-zero if a viewer lost its stream or a post failed.
//
//	usage: go run scripts/a15/load.go -url http://127.0.0.1:8080 -key KEY \
//	           -sessions s1,s2,... -rate 1500 -for 1h [-sample a15-sample.txt]
package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"math/rand/v2"
	"net/http"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

func main() {
	url := flag.String("url", "http://127.0.0.1:8080", "API URL")
	key := flag.String("key", "", "client API key")
	sessions := flag.String("sessions", "", "session tokens, comma-separated: one viewer each")
	rate := flag.Float64("rate", 1500, "pages per day")
	dur := flag.Duration("for", time.Hour, "how long")
	sample := flag.String("sample", "", "addresses to use, NUMBER STREET|LOCALITY per line (optional)")
	flag.Parse()

	addrs := [][2]string{{"12 FICTION ST", "MADEUPTON"}, {"3 FICTION ST", "MADEUPTON"}, {"SAMPLE RD", "MADEUPTON"}, {"4 DEMO PL", "MADEUPTON"}, {"7 EXAMPLE ST", "MADEUPTON"}}
	if *sample != "" {
		f, err := os.Open(*sample)
		if err != nil {
			panic(err)
		}
		sc := bufio.NewScanner(f)
		for sc.Scan() {
			if a, l, ok := strings.Cut(sc.Text(), "|"); ok {
				addrs = append(addrs, [2]string{a, l})
			}
		}
		f.Close()
	}

	ctx, cancel := context.WithTimeout(context.Background(), *dur)
	defer cancel()
	var events, drops atomic.Int64
	var wg sync.WaitGroup
	for _, s := range strings.Split(*sessions, ",") {
		if s == "" {
			continue
		}
		wg.Add(1)
		go func(token string) {
			defer wg.Done()
			for ctx.Err() == nil {
				req, _ := http.NewRequestWithContext(ctx, http.MethodGet, *url+"/api/v1/stream", nil)
				req.AddCookie(&http.Cookie{Name: "pa_session", Value: token})
				resp, err := http.DefaultClient.Do(req)
				if err != nil || resp.StatusCode != http.StatusOK {
					if ctx.Err() == nil {
						drops.Add(1)
						time.Sleep(time.Second)
					}
					continue
				}
				sc := bufio.NewScanner(resp.Body)
				for sc.Scan() {
					if strings.HasPrefix(sc.Text(), "event: message") {
						events.Add(1)
					}
				}
				resp.Body.Close()
				if ctx.Err() == nil {
					drops.Add(1) // the stream ended while the run was on
				}
			}
		}(s)
	}

	r := rand.New(rand.NewPCG(15, 1500))
	interval := time.Duration(float64(24*time.Hour) / *rate)
	var posted, failed int
	job := 1000000
	tick := time.NewTicker(interval)
	defer tick.Stop()
	start := time.Now()
	for ctx.Err() == nil {
		capcode, text := page(r, addrs, &job)
		body, _ := json.Marshal(map[string]any{"capcode": capcode, "text": text, "received_at": time.Now().UTC(), "baud": 1200})
		req, _ := http.NewRequest(http.MethodPost, *url+"/api/v1/pages", bytes.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+*key)
		if resp, err := http.DefaultClient.Do(req); err != nil || resp.StatusCode != http.StatusOK {
			failed++
		} else {
			resp.Body.Close()
			posted++
		}
		select {
		case <-ctx.Done():
		case <-tick.C:
		}
	}
	wg.Wait()
	viewers := len(strings.Split(*sessions, ","))
	fmt.Printf("load: %s, %d viewers, %d pages posted (%d failed) at %.0f/day, %d live events delivered, %d stream drops\n",
		time.Since(start).Round(time.Second), viewers, posted, failed, *rate, events.Load(), drops.Load())
	if failed > 0 || drops.Load() > 0 {
		os.Exit(1)
	}
}

// page makes a page in one of the example formats (SPEC.md section 10).
func page(r *rand.Rand, addrs [][2]string, job *int) (string, string) {
	*job++
	capcode := fmt.Sprintf("010000%d", 1+r.IntN(4))
	a := addrs[r.IntN(len(addrs))]
	switch n := r.IntN(10); {
	case n < 6:
		types := []string{"HSF1", "GRS3", "MED1", "ALM2"}
		grid := ""
		if r.IntN(3) == 0 {
			grid = fmt.Sprintf(" GRID %03d%03d", r.IntN(1000), r.IntN(1000))
		}
		return capcode, fmt.Sprintf("@@EX%07d %s LOAD TEST INCIDENT @ %s, %s%s UNITS T%d #STN%d", *job, types[r.IntN(len(types))], a[0], a[1], grid, r.IntN(9), r.IntN(4)+1)
	case n < 8:
		return capcode, fmt.Sprintf("HbRE EX%07d LOAD TEST UPDATE #STN%d", *job-1-r.IntN(20), r.IntN(4)+1)
	case n < 9:
		return capcode, "QDLoad test admin notice"
	default:
		if r.IntN(2) == 0 {
			return "0000050", "QDHEARTBEAT"
		}
		return fmt.Sprintf("0200%03d", r.IntN(1000)), "LOAD TEST UNKNOWN PAGER"
	}
}
