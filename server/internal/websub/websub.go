// Package websub subscribes to WebSub (PubSubHubbub) hubs so publishers can
// push "this feed changed" to us instead of waiting for the next poll.
//
// Hubs call us back over the internet, so this only works when the server is
// reachable at a public URL (the -public-url flag). Polling keeps running as
// the fallback either way.
package websub

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/hex"
	"hash"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	leaseSeconds = 7 * 24 * 3600
	renewBefore  = 12 * time.Hour   // resubscribe this long before the lease ends
	retryAfter   = 10 * time.Minute // after a failed or unconfirmed request
)

type sub struct {
	hub, topic, secret string
	requested          time.Time
	expires            time.Time // zero until the hub confirms
	pushes             int
}

type Manager struct {
	public string // e.g. https://news.example.com
	kick   func(feedID string) bool
	client *http.Client
	log    *slog.Logger

	mu   sync.Mutex
	subs map[string]*sub // by feed ID
}

func New(publicURL string, kick func(feedID string) bool, log *slog.Logger) *Manager {
	return &Manager{
		public: strings.TrimRight(publicURL, "/"),
		kick:   kick,
		client: &http.Client{Timeout: 15 * time.Second},
		log:    log,
		subs:   make(map[string]*sub),
	}
}

func (m *Manager) callback(feedID string) string {
	return m.public + "/websub/" + url.PathEscape(feedID)
}

// Discovered is called on every poll of a feed that advertises a hub. It
// subscribes when there is no active lease and none was requested recently.
func (m *Manager) Discovered(feedID, hub, topic string) {
	now := time.Now()
	m.mu.Lock()
	s := m.subs[feedID]
	if s != nil && s.hub == hub && s.topic == topic {
		active := !s.expires.IsZero() && now.Before(s.expires.Add(-renewBefore))
		if active || now.Sub(s.requested) < retryAfter {
			m.mu.Unlock()
			return
		}
	}
	if s == nil || s.hub != hub || s.topic != topic {
		s = &sub{hub: hub, topic: topic, secret: randomHex(20)}
		m.subs[feedID] = s
	}
	s.requested = now
	secret := s.secret
	m.mu.Unlock()

	go m.subscribe(feedID, hub, topic, secret)
}

func (m *Manager) subscribe(feedID, hub, topic, secret string) {
	form := url.Values{
		"hub.mode":          {"subscribe"},
		"hub.topic":         {topic},
		"hub.callback":      {m.callback(feedID)},
		"hub.secret":        {secret},
		"hub.lease_seconds": {strconv.Itoa(leaseSeconds)},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, hub, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := m.client.Do(req)
	if err != nil {
		m.log.Warn("websub subscribe failed", "feed", feedID, "hub", hub, "err", err)
		return
	}
	io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		m.log.Warn("websub subscribe rejected", "feed", feedID, "hub", hub, "status", resp.StatusCode)
		return
	}
	m.log.Info("websub subscribe requested", "feed", feedID, "hub", hub)
}

// Handler serves /websub/{feedID}: GET verifies a subscription, POST is a
// content notification.
func (m *Manager) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		feedID, _ := url.PathUnescape(strings.TrimPrefix(r.URL.Path, "/websub/"))
		switch r.Method {
		case http.MethodGet:
			m.verify(w, r, feedID)
		case http.MethodPost:
			m.notify(w, r, feedID)
		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	})
}

func (m *Manager) verify(w http.ResponseWriter, r *http.Request, feedID string) {
	q := r.URL.Query()
	m.mu.Lock()
	s := m.subs[feedID]
	ok := s != nil && q.Get("hub.topic") == s.topic
	if ok && q.Get("hub.mode") == "subscribe" {
		lease, _ := strconv.Atoi(q.Get("hub.lease_seconds"))
		if lease <= 0 {
			lease = leaseSeconds
		}
		s.expires = time.Now().Add(time.Duration(lease) * time.Second)
	}
	m.mu.Unlock()
	if !ok || (q.Get("hub.mode") != "subscribe" && q.Get("hub.mode") != "unsubscribe") {
		http.NotFound(w, r)
		return
	}
	m.log.Info("websub subscription confirmed", "feed", feedID, "mode", q.Get("hub.mode"))
	w.Header().Set("Content-Type", "text/plain")
	io.WriteString(w, q.Get("hub.challenge"))
}

func (m *Manager) notify(w http.ResponseWriter, r *http.Request, feedID string) {
	body, _ := io.ReadAll(io.LimitReader(r.Body, 8<<20))
	m.mu.Lock()
	s := m.subs[feedID]
	var secret string
	if s != nil {
		secret = s.secret
	}
	m.mu.Unlock()
	// Per the spec, always answer 2xx; just ignore anything we can't verify.
	w.WriteHeader(http.StatusAccepted)
	if s == nil || !validSignature(secret, r.Header.Get("X-Hub-Signature"), body) {
		return
	}
	m.mu.Lock()
	s.pushes++
	m.mu.Unlock()
	// The body is the updated feed, but simply polling it now keeps one code path.
	m.kick(feedID)
}

func validSignature(secret, header string, body []byte) bool {
	algo, sig, ok := strings.Cut(header, "=")
	if !ok {
		return false
	}
	var h func() hash.Hash
	switch strings.ToLower(algo) {
	case "sha1":
		h = sha1.New
	case "sha256":
		h = sha256.New
	case "sha384":
		h = sha512.New384
	case "sha512":
		h = sha512.New
	default:
		return false
	}
	mac := hmac.New(h, []byte(secret))
	mac.Write(body)
	want, err := hex.DecodeString(sig)
	return err == nil && hmac.Equal(mac.Sum(nil), want)
}

type Status struct {
	Feed    string    `json:"feed"`
	Hub     string    `json:"hub"`
	Active  bool      `json:"active"`
	Expires time.Time `json:"expires"`
	Pushes  int       `json:"pushes"`
}

func (m *Manager) Statuses() []Status {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]Status, 0, len(m.subs))
	for id, s := range m.subs {
		out = append(out, Status{Feed: id, Hub: s.hub, Active: time.Now().Before(s.expires), Expires: s.expires, Pushes: s.pushes})
	}
	return out
}

func randomHex(n int) string {
	b := make([]byte, n)
	rand.Read(b)
	return hex.EncodeToString(b)
}
