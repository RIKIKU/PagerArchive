package integrations

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"
	"sync"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"
	"github.com/eclipse/paho.mqtt.golang/packets"
	"github.com/jackc/pgx/v5"
)

// Default ports for mqtt:// and mqtts://.
const (
	mqttPort  = "1883"
	mqttsPort = "8883"
)

// publish connects, publishes with QoS 1, waits for the broker's
// acknowledgement and disconnects (ADR 0006 §7). One connection per
// delivery: no idle connections, and each one passes the address check.
func (s *Service) publish(ctx context.Context, l loaded, sec Secrets, topic string, body []byte, retained bool) result {
	u, err := url.Parse(l.config.Broker)
	if err != nil || u.Host == "" {
		return result{outcome: refused, cause: "bad_request", err: "the broker's address isn't valid"}
	}
	var tlsConfig *tls.Config
	if u.Scheme == "mqtts" {
		tlsConfig = &tls.Config{ServerName: u.Hostname(), MinVersion: tls.VersionTLS12}
		if l.config.CACert != "" {
			pool := x509.NewCertPool()
			if !pool.AppendCertsFromPEM([]byte(l.config.CACert)) {
				return result{outcome: refused, cause: "bad_request", err: "the CA certificate isn't valid PEM"}
			}
			tlsConfig.RootCAs = pool
		}
	}
	// The dial error is kept here: paho's own errors can quote the broker.
	var mu sync.Mutex
	var dialErr error
	suffix := make([]byte, 3)
	_, _ = rand.Read(suffix)
	opts := mqtt.NewClientOptions().
		AddBroker("tcp://" + u.Host).
		SetClientID("pagerarchive-" + l.row.Uid + "-" + hex.EncodeToString(suffix)).
		SetProtocolVersion(4). // MQTT 3.1.1
		SetCleanSession(true).
		SetAutoReconnect(false).
		SetConnectRetry(false).
		SetConnectTimeout(AttemptTimeout).
		SetWriteTimeout(AttemptTimeout).
		SetCustomOpenConnectionFn(func(*url.URL, mqtt.ClientOptions) (net.Conn, error) {
			conn, err := s.Guard.DialContext(ctx, "tcp", u.Host)
			if err == nil && tlsConfig != nil {
				tc := tls.Client(conn, tlsConfig)
				hctx, cancel := context.WithTimeout(ctx, AttemptTimeout)
				err = tc.HandshakeContext(hctx)
				cancel()
				if err != nil {
					conn.Close()
				} else {
					return tc, nil
				}
			}
			if err != nil {
				mu.Lock()
				dialErr = err
				mu.Unlock()
			}
			return conn, err
		})
	if l.config.Username != "" {
		opts.SetUsername(l.config.Username)
		opts.SetPassword(sec.Password)
	}
	c := mqtt.NewClient(opts)
	t := c.Connect()
	if !t.WaitTimeout(AttemptTimeout + time.Second) {
		c.Disconnect(0)
		return result{outcome: retryable, cause: "timeout", err: "the broker didn't answer the connection"}
	}
	if err := t.Error(); err != nil {
		mu.Lock()
		de := dialErr
		mu.Unlock()
		var timeout interface{ Timeout() bool }
		switch {
		case de != nil:
			cause, text := netError(de)
			return result{outcome: retryable, cause: cause, err: text}
		case errors.Is(err, packets.ErrorRefusedBadUsernameOrPassword):
			return result{outcome: refused, status: "MQTT refused", cause: "auth_refused", err: "MQTT: bad user name or password"}
		case errors.Is(err, packets.ErrorRefusedNotAuthorised):
			return result{outcome: refused, status: "MQTT refused", cause: "auth_refused", err: "MQTT: not authorised"}
		case errors.Is(err, packets.ErrorRefusedBadProtocolVersion):
			return result{outcome: refused, status: "MQTT refused", cause: "mqtt_protocol_refused", err: "MQTT: the broker doesn't speak MQTT 3.1.1"}
		case errors.Is(err, packets.ErrorRefusedIDRejected), errors.Is(err, packets.ErrorRefusedServerUnavailable):
			return result{outcome: retryable, status: "MQTT refused", cause: "mqtt_unavailable", err: "MQTT: the broker refused the connection for now"}
		case errors.As(err, &timeout) && timeout.Timeout(): // connected, but no CONNACK within the timeout
			return result{outcome: retryable, cause: "timeout", err: "the broker didn't answer the connection"}
		}
		return result{outcome: retryable, cause: "connection_failed", err: "MQTT: couldn't connect"}
	}
	defer c.Disconnect(250)
	pt := c.Publish(topic, 1, retained, body)
	if !pt.WaitTimeout(AttemptTimeout) || pt.Error() != nil {
		// Sent, or maybe sent: never retried (owner, Q2).
		return result{outcome: noAnswer, cause: "no_answer", err: fmt.Sprintf("no acknowledgement from the broker within %d s", int(AttemptTimeout.Seconds()))}
	}
	return result{outcome: delivered, status: "MQTT acknowledged"}
}

// discoveryTopic is where an integration's Home Assistant entity is
// configured: single-component discovery, one event entity per integration.
func discoveryTopic(cfg Config, uid string) string {
	return cfg.HAPrefix + "/event/pagerarchive/" + uid + "/config"
}

// deploymentID identifies this PagerArchive in Home Assistant, so all its
// integrations share one device and two deployments on a broker don't
// collide. Made once and kept in settings.
func (s *Service) deploymentID(ctx context.Context) (string, error) {
	const key = "deployment_id"
	v, err := s.Store.GetSetting(ctx, key)
	if err == nil {
		return v, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return "", err
	}
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	id := hex.EncodeToString(b)
	if _, err := s.Store.Pool.Exec(ctx, "INSERT INTO settings (key, value) VALUES ($1, $2) ON CONFLICT DO NOTHING", key, id); err != nil {
		return "", err
	}
	return s.Store.GetSetting(ctx, key)
}

// discoveryConfig is the retained Home Assistant config (R50b, A38).
func (s *Service) discoveryConfig(ctx context.Context, l loaded) ([]byte, error) {
	dep, err := s.deploymentID(ctx)
	if err != nil {
		return nil, err
	}
	device := map[string]any{
		"identifiers":  []string{"pagerarchive_" + dep},
		"name":         "PagerArchive",
		"manufacturer": "PagerArchive",
		"model":        "PagerArchive server",
		"sw_version":   s.version(),
	}
	if s.AppURL != nil {
		device["configuration_url"] = s.AppURL.JoinPath("integrations").String()
	}
	return json.Marshal(map[string]any{
		"name":        l.row.Name,
		"unique_id":   "pagerarchive_" + l.row.Uid,
		"state_topic": l.config.Topic,
		"event_types": []string{"page", "test"},
		"icon":        "mdi:pager",
		"qos":         1,
		"device":      device,
		"origin":      map[string]string{"name": "PagerArchive", "sw_version": s.version(), "support_url": "https://github.com/RIKIKU/PagerArchive"},
	})
}

// publishDiscovery publishes (or with remove, clears) an integration's
// retained discovery config, and says what happened.
func (s *Service) publishDiscovery(ctx context.Context, l loaded, sec Secrets, remove bool) Outcome {
	var body []byte
	if !remove {
		var err error
		if body, err = s.discoveryConfig(ctx, l); err != nil {
			return Outcome{Message: "Saved, but Home Assistant discovery couldn't be prepared."}
		}
	}
	actx, cancel := context.WithTimeout(ctx, AttemptTimeout+2*time.Second)
	defer cancel()
	r := s.publish(actx, l, sec, discoveryTopic(l.config, l.row.Uid), body, true)
	if r.outcome != delivered {
		s.Log.Warn("home assistant discovery", "integration", l.row.ID, "remove", remove, "error", r.err)
		if remove {
			return Outcome{Message: "The old Home Assistant entity couldn't be removed: " + r.err + "."}
		}
		return Outcome{Message: "Saved, but Home Assistant discovery wasn't published: " + r.err + "."}
	}
	if remove {
		return Outcome{OK: true, Message: "Home Assistant entity removed."}
	}
	return Outcome{OK: true, Message: "Home Assistant discovery published."}
}

// discoveryChanged reports whether an edit moved or turned off discovery,
// so the old entity must be removed.
func discoveryChanged(old, cur Config) bool {
	if !old.HADiscovery {
		return false
	}
	return !cur.HADiscovery || old.HAPrefix != cur.HAPrefix || old.Broker != cur.Broker || old.Topic != cur.Topic ||
		!strings.EqualFold(old.Username, cur.Username)
}

// DiscoveryCleanup prepares the removal of an owner's Home Assistant
// entities, to run once their account is deleted (their integrations go
// with it). Best effort: a broker that can't be reached keeps a stale
// entity, which the owner can delete in Home Assistant.
func (s *Service) DiscoveryCleanup(ctx context.Context, ownerID int64) func() {
	if s == nil {
		return func() {}
	}
	rows, err := s.Store.IntegrationsByOwner(ctx, ownerID)
	if err != nil {
		return func() {}
	}
	type target struct {
		l   loaded
		sec Secrets
	}
	var targets []target
	for _, r := range rows {
		l, err := decode(r)
		if err != nil || l.kind != MQTT || !l.config.HADiscovery {
			continue
		}
		sec, err := s.secretsOf(l.row)
		if err != nil {
			continue
		}
		targets = append(targets, target{l, sec})
	}
	return func() {
		for _, t := range targets {
			s.publishDiscovery(context.Background(), t.l, t.sec, true)
		}
	}
}
