// Command test-collector is a test OTLP/HTTP collector that keeps what
// it's sent in memory and, when stopped, writes how much it got: for
// scripts/a15.sh -t, the resource budget with telemetry on. Never for a
// deployment.
//
// usage: go run ./scripts/test-collector -addr 0.0.0.0:14320 -summary FILE
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	"github.com/RIKIKU/PagerArchive/internal/telemetry/telemetrytest"
)

func main() {
	addr := flag.String("addr", "0.0.0.0:14320", "address to listen on")
	summary := flag.String("summary", "", "file to write the counts to when stopped (default: stdout)")
	flag.Parse()
	c := &telemetrytest.Collector{}
	srv := &http.Server{Addr: *addr, Handler: c}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatal(err)
		}
	}()
	<-ctx.Done()
	_ = srv.Shutdown(context.Background())
	spans, logs, points := c.Records()
	out := fmt.Sprintf("test collector received %d spans, %d log records and %d metric points (%d bytes as JSON)\n", spans, logs, points, len(c.Dump()))
	if *summary == "" {
		fmt.Print(out)
		return
	}
	if err := os.WriteFile(*summary, []byte(out), 0o644); err != nil {
		log.Fatal(err)
	}
}
