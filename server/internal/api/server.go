// Package api serves the live event stream, JSON endpoints and the web app.
package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"nuevo-rss/internal/cluster"
	"nuevo-rss/internal/ingest"
	"nuevo-rss/internal/live"
	"nuevo-rss/internal/markets"
	"nuevo-rss/internal/websub"
)

type Server struct {
	Engine  *cluster.Engine
	Fetcher *ingest.Fetcher
	Markets *markets.Service
	Hub     *live.Hub
	WebDir  string // built frontend; empty or missing = API only
	Log     *slog.Logger
	WebSub  *websub.Manager // nil unless -public-url is set

	started time.Time
	snap    snapCache
}

// snapCache holds recent snapshots, encoded and compressed once and shared by
// every client until the story state changes. Clients ask for the last few
// hours first (fast first paint), then the older part.
type snapCache struct {
	mu      sync.Mutex
	entries map[snapKey]*snapEntry
	buildMs float64
	builds  int64
	served  int64
	lastRaw int
	lastGz  int
	chunks  chunkCache
}

type snapKey struct {
	seq      uint64
	from, to int64 // unix ms bounds; 0 = open
}

type snapEntry struct {
	raw, gz []byte
	used    time.Time
}

type Status struct {
	FeedsOK    int `json:"feedsOk"`
	FeedsTotal int `json:"feedsTotal"`
	Stories    int `json:"stories"`
	Clusters   int `json:"clusters"`
	Clients    int `json:"clients"`
}

// hello is the first event on a stream: the version the client is subscribed
// from, plus the small bits of state that are not versioned.
type hello struct {
	Seq        uint64          `json:"seq"`
	Markets    []markets.Quote `json:"markets"`
	Status     Status          `json:"status"`
	ServerTime int64           `json:"serverTime"`
}

type snapshotBody struct {
	Seq      uint64                `json:"seq"`
	Clusters []cluster.WireCluster `json:"clusters"`
}

func (s *Server) Handler() http.Handler {
	s.started = time.Now()
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/stream", s.stream)
	mux.HandleFunc("GET /api/snapshot", s.snapshot)
	mux.HandleFunc("GET /api/feeds", func(w http.ResponseWriter, r *http.Request) { writeJSON(w, s.Fetcher.Statuses()) })
	mux.HandleFunc("GET /api/health", func(w http.ResponseWriter, r *http.Request) { writeJSON(w, s.status()) })
	mux.HandleFunc("GET /api/metrics", s.metrics)
	if s.WebDir != "" {
		if fi, err := os.Stat(s.WebDir); err == nil && fi.IsDir() {
			mux.Handle("/", spa(s.WebDir))
		} else {
			s.Log.Info("web build not found, serving API only", "dir", s.WebDir)
		}
	}
	return mux
}

// RunStatus pushes feed health to clients periodically.
func (s *Server) RunStatus(ctx context.Context, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			b, _ := json.Marshal(s.status())
			s.Hub.Publish(live.Event{Type: "status", Data: b})
		}
	}
}

func (s *Server) status() Status {
	ok, total := s.Fetcher.Health()
	stories, clusters := s.Engine.Stats()
	return Status{FeedsOK: ok, FeedsTotal: total, Stories: stories, Clusters: clusters, Clients: s.Hub.Count()}
}

// snapshotBytes returns the cached snapshot for [from, to), rebuilding it if
// stories changed since it was built.
func (s *Server) snapshotBytes(from, to int64) (seq uint64, raw, gz []byte, err error) {
	c := &s.snap
	c.mu.Lock()
	defer c.mu.Unlock()
	c.served++
	if c.entries == nil {
		c.entries = make(map[snapKey]*snapEntry)
	}
	cur := s.Engine.Seq()
	if e, ok := c.entries[snapKey{cur, from, to}]; ok {
		e.used = time.Now()
		return cur, e.raw, e.gz, nil
	}
	t0 := time.Now()
	var fromT, toT time.Time
	if from > 0 {
		fromT = time.UnixMilli(from)
	}
	if to > 0 {
		toT = time.UnixMilli(to)
	}
	clusters, seq := s.Engine.Snapshot(fromT, toT)
	raw, gz, err = c.chunks.encode(seq, clusters)
	if err != nil {
		return 0, nil, nil, err
	}
	// Keep only current-version entries (plus anything used in the last few
	// seconds, for clients mid-way through their two requests).
	for k, e := range c.entries {
		if k.seq != seq && time.Since(e.used) > 5*time.Second {
			delete(c.entries, k)
		}
	}
	c.entries[snapKey{seq, from, to}] = &snapEntry{raw: raw, gz: gz, used: time.Now()}
	c.buildMs = float64(time.Since(t0).Microseconds()) / 1000
	c.builds++
	c.lastRaw, c.lastGz = len(raw), len(gz)
	return seq, raw, gz, nil
}

func (s *Server) snapshot(w http.ResponseWriter, r *http.Request) {
	// Bounds are rounded to the minute so clients share cache entries.
	bound := func(name string) int64 {
		v, _ := strconv.ParseInt(r.URL.Query().Get(name), 10, 64)
		return v / 60000 * 60000
	}
	from, to := bound("from"), bound("to")
	seq, raw, gz, err := s.snapshotBytes(from, to)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	h := w.Header()
	h.Set("Content-Type", "application/json")
	h.Set("Cache-Control", "no-cache")
	h.Set("ETag", fmt.Sprintf(`"%d-%d-%d"`, seq, from, to))
	h.Set("Vary", "Accept-Encoding")
	if r.Header.Get("If-None-Match") == h.Get("ETag") {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	body := raw
	if strings.Contains(r.Header.Get("Accept-Encoding"), "gzip") {
		h.Set("Content-Encoding", "gzip")
		body = gz
	}
	h.Set("Content-Length", strconv.Itoa(len(body)))
	w.Write(body)
}

// stream is the Server-Sent Events endpoint. It sends a hello with the
// version it is subscribed from, then every change. Story events carry their
// version as the SSE id. Events are small, so the stream is not compressed;
// that keeps per-connection memory tiny.
func (s *Server) stream(w http.ResponseWriter, r *http.Request) {
	rc := http.NewResponseController(w)
	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache, no-transform")
	h.Set("X-Accel-Buffering", "no")

	seq, sub := s.Engine.Subscribe()
	defer s.Hub.Unsubscribe(sub)
	hi, err := json.Marshal(hello{
		Seq:        seq,
		Markets:    s.Markets.Latest(), // read after subscribing, so it is never older than what we'd miss
		Status:     s.status(),
		ServerTime: time.Now().UnixMilli(),
	})
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	bw := newEventWriter(w)
	bw.raw("retry: 2000\n\n")
	bw.event(live.Event{Type: "hello", Data: hi})
	if bw.err != nil || rc.Flush() != nil {
		return
	}

	ping := time.NewTicker(15 * time.Second)
	defer ping.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case ev, ok := <-sub.C:
			if !ok {
				return // too slow; the browser will reconnect
			}
			bw.event(ev)
			// Send whatever else is already queued in the same flush.
		drain:
			for range 64 {
				select {
				case ev, ok := <-sub.C:
					if !ok {
						rc.Flush()
						return
					}
					bw.event(ev)
				default:
					break drain
				}
			}
			if bw.err != nil || rc.Flush() != nil {
				return
			}
		case <-ping.C:
			bw.raw(": ping\n\n")
			if bw.err != nil || rc.Flush() != nil {
				return
			}
		}
	}
}

type eventWriter struct {
	w   io.Writer
	err error
}

func newEventWriter(w io.Writer) *eventWriter { return &eventWriter{w: w} }

func (e *eventWriter) raw(s string) {
	if e.err == nil {
		_, e.err = io.WriteString(e.w, s)
	}
}

func (e *eventWriter) event(ev live.Event) {
	if ev.Seq > 0 {
		e.raw("id: " + strconv.FormatUint(ev.Seq, 10) + "\n")
	}
	e.raw("event: " + ev.Type + "\ndata: ")
	if e.err == nil {
		_, e.err = e.w.Write(ev.Data)
	}
	e.raw("\n\n")
}

func (s *Server) metrics(w http.ResponseWriter, r *http.Request) {
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	s.snap.mu.Lock()
	snap := map[string]any{
		"lastRawKB": s.snap.lastRaw / 1024, "lastGzipKB": s.snap.lastGz / 1024,
		"buildMs": s.snap.buildMs, "builds": s.snap.builds, "served": s.snap.served, "cached": len(s.snap.entries),
		"chunkHitPct": pctOf(s.snap.chunks.hits, s.snap.chunks.all),
	}
	s.snap.mu.Unlock()
	writeJSON(w, map[string]any{
		"uptimeSec":     int(time.Since(s.started).Seconds()),
		"status":        s.status(),
		"ingest":        s.Fetcher.Metrics(),
		"snapshot":      snap,
		"websub":        s.websubStatus(),
		"races":         s.raceStats(),
		"firstToReport": s.firstReporters(),
		"runtime": map[string]any{
			"heapMB":     float64(ms.HeapAlloc) / (1 << 20),
			"sysMB":      float64(ms.Sys) / (1 << 20),
			"goroutines": runtime.NumGoroutine(),
			"gcPauseMs":  float64(ms.PauseNs[(ms.NumGC+255)%256]) / 1e6,
		},
	})
}

// raceStats summarizes same-article races between feed kinds, and per
// publisher between its sitemap and RSS feed.
func (s *Server) raceStats() any {
	kinds := make(map[string]string)
	pubs := make(map[string]string)
	for _, f := range s.Fetcher.Statuses() {
		kinds[f.ID], pubs[f.ID] = f.Kind, f.Publisher
	}
	type agg struct {
		Count     int     `json:"count"`
		MedianMin float64 `json:"medianLeadMin"`
		P90Min    float64 `json:"p90LeadMin"`
		gaps      []float64
	}
	byKind := map[string]*agg{}
	byPub := map[string]*agg{}
	add := func(m map[string]*agg, k string, g float64) {
		a := m[k]
		if a == nil {
			a = &agg{}
			m[k] = a
		}
		a.gaps = append(a.gaps, g)
	}
	for _, r := range s.Engine.Races() {
		wk, lk := kinds[r.Winner], kinds[r.Loser]
		if wk == "" || lk == "" || pubs[r.Winner] != pubs[r.Loser] {
			continue // only compare a publisher against itself
		}
		g := r.Gap.Minutes()
		add(byKind, wk+" beat "+lk, g)
		if wk != lk {
			add(byPub, pubs[r.Winner]+": "+wk+" beat "+lk, g)
		}
	}
	finish := func(m map[string]*agg) map[string]*agg {
		for _, a := range m {
			sortFloats(a.gaps)
			a.Count = len(a.gaps)
			a.MedianMin = round1(a.gaps[len(a.gaps)/2])
			a.P90Min = round1(a.gaps[len(a.gaps)*9/10])
		}
		return m
	}
	return map[string]any{"byKind": finish(byKind), "byPublisher": finish(byPub)}
}

func (s *Server) firstReporters() any {
	type row struct {
		Publisher string `json:"publisher"`
		First     int    `json:"first"`
	}
	var rows []row
	for p, n := range s.Engine.FirstReporters(3) {
		rows = append(rows, row{p, n})
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].First > rows[j].First })
	return rows[:min(len(rows), 12)]
}

func sortFloats(v []float64) { sort.Float64s(v) }

func round1(v float64) float64 { return float64(int(v*10+0.5)) / 10 }

func (s *Server) websubStatus() any {
	if s.WebSub == nil {
		return "disabled (start with -public-url to enable)"
	}
	return s.WebSub.Statuses()
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	json.NewEncoder(w).Encode(v)
}

// spa serves static files and falls back to index.html for client routes.
func spa(dir string) http.Handler {
	fs := http.FileServer(http.Dir(dir))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := filepath.Join(dir, filepath.Clean("/"+r.URL.Path))
		if fi, err := os.Stat(p); err != nil || fi.IsDir() {
			w.Header().Set("Cache-Control", "no-cache")
			http.ServeFile(w, r, filepath.Join(dir, "index.html"))
			return
		}
		if strings.HasPrefix(r.URL.Path, "/assets/") {
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable") // Vite hashes these names
		}
		fs.ServeHTTP(w, r)
	})
}

func pctOf(a, b int64) float64 {
	if b == 0 {
		return 0
	}
	return float64(a*1000/b) / 10
}
