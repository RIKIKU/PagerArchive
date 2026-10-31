// Command pagerarchive-client receives POCSAG pages and posts them to a
// PagerArchive server. With RADIO_FREQUENCY set it runs rtl_fm and
// multimon-ng itself (R13), passing the audio from one to the other and
// measuring it on the way (R74a, internal/radio; rtl_fm runs without
// squelch); otherwise it reads multimon-ng output on stdin.
//
// Settings (environment):
//
//	SERVER_URL       server base URL, e.g. https://pager.example.test (required)
//	CLIENT_ID        this client's ID, as issued by an admin (required)
//	CLIENT_KEY       this client's API key, or CLIENT_KEY_FILE: a file holding it
//	PRINT_PAGES      true prints decoded pages to stdout (default false; R13a)
//	BUFFER_SIZE      pages held in memory while the server is unreachable (default 1000)
//	RADIO_FREQUENCY  e.g. 148.8125M; empty reads stdin
//	RADIO_GAIN       rtl_fm gain in dB (default: automatic)
//	RADIO_PPM        rtl_fm frequency correction (default 0)
//	POCSAG_BAUDS     comma-separated baud rates to decode (default 512,1200,2400)
//	MULTIMON_MODE    multimon-ng -f mode: alpha, numeric or auto (default alpha)
//
// Telemetry (R69-R74, docs/monitoring.md) is off unless the standard
// OTEL_EXPORTER_OTLP_ENDPOINT is set; the other OTEL_* settings apply as
// usual. Its service.name is pagerarchive-client and its
// service.instance.id the client's ID.
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/RIKIKU/PagerArchive/internal/client"
	"github.com/RIKIKU/PagerArchive/internal/decoder"
	"github.com/RIKIKU/PagerArchive/internal/radio"
	"github.com/RIKIKU/PagerArchive/internal/telemetry"
)

var version = "dev"

func main() {
	if len(os.Args) > 1 && (os.Args[1] == "--version" || os.Args[1] == "version") {
		fmt.Println("pagerarchive-client", version)
		return
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	if err := run(log); err != nil {
		log.Error("client stopped", "error", err.Error())
		os.Exit(1)
	}
}

func run(plain *slog.Logger) error {
	log := plain
	serverURL, id := os.Getenv("SERVER_URL"), os.Getenv("CLIENT_ID")
	key := os.Getenv("CLIENT_KEY")
	if f := os.Getenv("CLIENT_KEY_FILE"); key == "" && f != "" {
		b, err := os.ReadFile(f)
		if err != nil {
			return fmt.Errorf("CLIENT_KEY_FILE: %w", err)
		}
		key = strings.TrimSpace(string(b))
	}
	if serverURL == "" || id == "" || key == "" {
		return errors.New("SERVER_URL, CLIENT_ID and CLIENT_KEY (or CLIENT_KEY_FILE) are required")
	}
	bufSize := 1000
	if v := os.Getenv("BUFFER_SIZE"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 {
			return errors.New("BUFFER_SIZE must be a positive number")
		}
		bufSize = n
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// Telemetry: log lines go to stderr as before and, when it's on, over
	// OTLP too (R73); the console's page text never does (R13a).
	tel := telemetry.Start(ctx, telemetry.Settings{ServiceName: "pagerarchive-client", Version: version, InstanceID: id, Metrics: true, Log: plain})
	defer func() {
		flush, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		tel.Shutdown(flush)
	}()
	log = slog.New(tel.LogHandler(plain.Handler(), "pagerarchive-client"))
	tracer := tel.Tracer("pagerarchive-client")
	// With RADIO_FREQUENCY set the client runs the radio and measures its
	// audio (R74a); reading stdin, it has no radio health to report.
	var health *radio.Health
	if os.Getenv("RADIO_FREQUENCY") != "" {
		health = radio.NewHealth(time.Now)
	}

	buf := client.NewBuffer(bufSize)
	metrics, err := client.NewMetrics(tel.Meter("pagerarchive-client"), buf, health)
	if err != nil {
		log.Error("telemetry: client metrics are off", "error", err.Error())
	}
	buf.Metrics = metrics
	sender := &client.Sender{
		URL: serverURL, Key: key, HTTP: &http.Client{}, Buffer: buf, Log: log,
		MinBackoff: time.Second, MaxBackoff: time.Minute, Tracer: tracer, Metrics: metrics,
	}
	sendDone := make(chan struct{})
	sendCtx, stopSend := context.WithCancel(context.Background())
	defer stopSend()
	go func() { sender.Run(sendCtx); close(sendDone) }()

	reader := &client.Reader{
		ClientID: id, Adapter: decoder.Multimon{}, Buffer: buf, Now: time.Now,
		PrintPages: os.Getenv("PRINT_PAGES") == "true", Stdout: os.Stdout, Log: log,
		Tracer: tracer, Metrics: metrics,
	}
	var input io.Reader = os.Stdin
	var wait func() error
	if health != nil {
		freq := os.Getenv("RADIO_FREQUENCY")
		out, w, err := radio.Start(ctx, radio.Settings{
			Frequency: freq, Gain: os.Getenv("RADIO_GAIN"), PPM: getenv("RADIO_PPM", "0"), Mode: getenv("MULTIMON_MODE", "alpha"),
			Bauds: strings.Split(getenv("POCSAG_BAUDS", "512,1200,2400"), ","),
		}, health, os.Stderr) // rtl_fm's messages to stderr, as before; never page text
		if err != nil {
			return err
		}
		input, wait = out, w
		metrics.Radio(true)
		log.Info("receiving", "frequency", freq, "client", id, "version", version)
	} else {
		log.Info("reading decoder output on stdin", "client", id, "version", version)
	}
	readErr := reader.Run(input)
	if wait != nil {
		err := wait()
		metrics.Radio(false)
		if err != nil && ctx.Err() == nil {
			readErr = errors.Join(readErr, fmt.Errorf("radio pipeline stopped: %w", err))
		}
	}

	// Input ended: give buffered pages a short time to go out.
	drain(ctx, buf, 10*time.Second)
	if n := buf.Len(); n > 0 {
		log.Warn("exiting with unsent pages", "buffered", n)
	}
	stopSend()
	<-sendDone
	return readErr
}

func drain(ctx context.Context, buf *client.Buffer, limit time.Duration) {
	deadline := time.After(limit)
	for buf.Len() > 0 {
		select {
		case <-deadline:
			return
		case <-ctx.Done():
			return
		case <-time.After(100 * time.Millisecond):
		}
	}
}

func getenv(name, def string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return def
}
