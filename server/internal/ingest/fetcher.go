// Package ingest polls RSS/Atom feeds and Google News sitemaps and hands new
// stories to the cluster engine.
package ingest

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/rand/v2"
	"net"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/mmcdole/gofeed"

	"nuevo-rss/internal/classify"
	"nuevo-rss/internal/cluster"
	"nuevo-rss/internal/config"
	"nuevo-rss/internal/store"
)

const (
	userAgent    = "Mozilla/5.0 (compatible; nuevo-rss/0.1)"
	maxBody      = 8 << 20
	maxBackoff   = 30 * time.Minute
	concurrency  = 16
	fetchTimeout = 20 * time.Second
	// Feeds whose CDN cache is shorter than the default interval are polled
	// as often as the cache allows, but never more than once per minInterval.
	minInterval = 30 * time.Second
	// Cache-busted feeds always reach the publisher's server, so poll them
	// at a steady, polite rate.
	bustInterval = 60 * time.Second
	// A push (WebSub) never triggers polls closer together than this.
	minKickGap = 5 * time.Second
	lagSamples = 2000
)

// HubWatcher is told about WebSub hubs found in feeds (see package websub).
type HubWatcher interface {
	Discovered(feedID, hub, topic string)
}

type FeedStatus struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Publisher string    `json:"publisher"`
	Category  string    `json:"category"`
	Kind      string    `json:"kind"`
	URL       string    `json:"url"`
	LastFetch time.Time `json:"lastFetch"`
	LastOK    time.Time `json:"lastOk"`
	Error     string    `json:"error,omitempty"`
	Fails     int       `json:"fails"`
	Items     int       `json:"items"`    // items in the last parsed response
	NewTotal  int       `json:"newTotal"` // new stories since startup
	LatencyMs int64     `json:"latencyMs"`
	Interval  int       `json:"intervalSec"` // current poll interval
	NextFetch time.Time `json:"nextFetch"`
	Hub       string    `json:"hub,omitempty"` // WebSub hub, if the feed has one
	Pushes    int       `json:"pushes"`        // fetches triggered by WebSub
}

type lagRing struct {
	vals []time.Duration
	pos  int
}

func (r *lagRing) add(d time.Duration) {
	if len(r.vals) < lagSamples {
		r.vals = append(r.vals, d)
		return
	}
	r.vals[r.pos] = d
	r.pos = (r.pos + 1) % lagSamples
}

type Fetcher struct {
	feeds    []config.Feed
	engine   *cluster.Engine
	store    *store.Store
	model    *classify.Model
	client   *http.Client
	interval time.Duration
	sem      chan struct{}
	log      *slog.Logger
	hubs     HubWatcher
	kicks    map[string]chan struct{}

	mu     sync.Mutex
	state  map[string]store.FeedState
	status map[string]*FeedStatus
	lags   map[string]*lagRing // by feed kind: publish → first seen
	polls  int64
	n304   int64
	busted int64
}

func New(feeds []config.Feed, eng *cluster.Engine, st *store.Store, state map[string]store.FeedState, interval time.Duration, log *slog.Logger) *Fetcher {
	transport := &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		DialContext:           (&net.Dialer{Timeout: 8 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          100,
		MaxIdleConnsPerHost:   4,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   8 * time.Second,
		ResponseHeaderTimeout: 15 * time.Second,
	}
	f := &Fetcher{
		feeds:    feeds,
		engine:   eng,
		store:    st,
		model:    classify.NewModel(),
		client:   &http.Client{Transport: transport, Timeout: fetchTimeout},
		interval: interval,
		sem:      make(chan struct{}, concurrency),
		log:      log,
		kicks:    make(map[string]chan struct{}, len(feeds)),
		state:    state,
		status:   make(map[string]*FeedStatus, len(feeds)),
		lags:     map[string]*lagRing{"rss": {}, "sitemap": {}},
	}
	for _, fd := range feeds {
		f.status[fd.ID] = &FeedStatus{ID: fd.ID, Name: fd.Name, Publisher: fd.Publisher, Category: fd.Category, Kind: fd.Kind, URL: fd.URL}
		f.kicks[fd.ID] = make(chan struct{}, 1)
	}
	return f
}

// SetHubWatcher enables WebSub: hubs found in feeds are reported to w.
func (f *Fetcher) SetHubWatcher(w HubWatcher) { f.hubs = w }

// Kick makes a feed poll right away (used when a WebSub hub pings us).
func (f *Fetcher) Kick(feedID string) bool {
	ch, ok := f.kicks[feedID]
	if !ok {
		return false
	}
	select {
	case ch <- struct{}{}:
	default: // a kick is already pending
	}
	return true
}

// Train teaches the category model from stored stories of single-topic feeds.
func (f *Fetcher) Train(stories []*cluster.Story) int {
	learnable := make(map[string]bool)
	for _, fd := range f.feeds {
		learnable[fd.ID] = !fd.Classify
	}
	n := 0
	for _, s := range stories {
		if learnable[s.FeedID] && len(s.Categories) > 0 {
			f.model.Learn(s.Title, s.Categories[0])
			n++
		}
	}
	return n
}

// Run polls every feed on its own schedule until ctx is cancelled.
func (f *Fetcher) Run(ctx context.Context) {
	var wg sync.WaitGroup
	for _, fd := range f.feeds {
		wg.Add(1)
		go func() {
			defer wg.Done()
			f.loop(ctx, fd)
		}()
	}
	wg.Wait()
}

func (f *Fetcher) loop(ctx context.Context, fd config.Feed) {
	base := fd.Interval(f.interval)
	interval := base
	if fd.Bust && fd.IntervalSec == 0 {
		interval = min(bustInterval, base)
	}
	delay := time.Duration(rand.Int64N(int64(2 * time.Second))) // spread the first burst a little
	fails := 0
	var last time.Time
	for {
		t := time.NewTimer(delay)
		pushed := false
		select {
		case <-ctx.Done():
			t.Stop()
			return
		case <-t.C:
		case <-f.kicks[fd.ID]:
			t.Stop()
			if wait := minKickGap - time.Since(last); wait > 0 {
				select {
				case <-ctx.Done():
					return
				case <-time.After(wait):
				}
			}
			pushed = true
		}

		last = time.Now()
		maxAge, err := f.fetchOnce(ctx, fd)
		if ctx.Err() != nil {
			return
		}
		if fd.IntervalSec == 0 && !fd.Bust && maxAge >= 0 {
			interval = min(max(maxAge, minInterval), base)
		}
		if err != nil {
			fails++
			delay = min(interval<<min(fails, 5), maxBackoff)
			f.log.Warn("feed failed", "feed", fd.ID, "fails", fails, "err", err)
		} else {
			fails = 0
			delay = interval
		}
		// ±10% jitter keeps feeds from synchronising.
		delay += time.Duration(rand.Int64N(int64(delay)/5+1)) - delay/10

		f.mu.Lock()
		st := f.status[fd.ID]
		st.Fails = fails
		st.Interval = int(interval / time.Second)
		st.NextFetch = time.Now().Add(delay)
		if pushed {
			st.Pushes++
		}
		f.mu.Unlock()
	}
}

var errStatus = errors.New("unexpected status")

type response struct {
	body    []byte
	maxAge  time.Duration
	etag    string
	lastMod string
	notMod  bool
}

func (f *Fetcher) get(ctx context.Context, url string, prev store.FeedState) (*response, error) {
	r, err := f.getOnce(ctx, url, prev, true)
	// Some bot filters (Cloudflare) reject requests somewhat at random; one
	// immediate retry with plainer headers usually gets through.
	if errors.Is(err, errForbidden) {
		r, err = f.getOnce(ctx, url, prev, false)
	}
	return r, err
}

var errForbidden = fmt.Errorf("%w 403", errStatus)

func (f *Fetcher) getOnce(ctx context.Context, url string, prev store.FeedState, accept bool) (*response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", userAgent)
	if accept {
		req.Header.Set("Accept", "application/rss+xml, application/atom+xml, application/xml;q=0.9, text/xml;q=0.9, */*;q=0.5")
	}
	if prev.ETag != "" {
		req.Header.Set("If-None-Match", prev.ETag)
	}
	if prev.LastModified != "" {
		req.Header.Set("If-Modified-Since", prev.LastModified)
	}
	resp, err := f.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	r := &response{maxAge: parseMaxAge(resp.Header.Get("Cache-Control")), etag: resp.Header.Get("ETag"), lastMod: resp.Header.Get("Last-Modified")}
	if resp.StatusCode == http.StatusNotModified {
		r.notMod = true
		return r, nil
	}
	if resp.StatusCode != http.StatusOK {
		io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
		if resp.StatusCode == http.StatusForbidden {
			return r, errForbidden
		}
		return r, fmt.Errorf("%w %d", errStatus, resp.StatusCode)
	}
	r.body, err = io.ReadAll(io.LimitReader(resp.Body, maxBody))
	return r, err
}

func bustURL(u string) string {
	sep := "?"
	if strings.Contains(u, "?") {
		sep = "&"
	}
	return u + sep + "_nrss=" + strconv.FormatInt(time.Now().Unix(), 10)
}

// fetchOnce polls one feed. It returns the response's Cache-Control max-age
// (-1 if absent), which sets how often the feed is worth polling.
func (f *Fetcher) fetchOnce(ctx context.Context, fd config.Feed) (time.Duration, error) {
	select {
	case f.sem <- struct{}{}:
	case <-ctx.Done():
		return -1, ctx.Err()
	}
	defer func() { <-f.sem }()

	start := time.Now()
	f.mu.Lock()
	prev := f.state[fd.ID]
	warm := !f.status[fd.ID].LastOK.IsZero() // first fetch is backfill, not news
	f.polls++
	if fd.Bust {
		f.busted++
	}
	f.mu.Unlock()

	maxAge := time.Duration(-1)
	items := 0
	err := func() error {
		url := fd.URL
		if fd.Bust {
			url = bustURL(url)
		}
		resp, err := f.get(ctx, url, prev)
		if resp != nil {
			maxAge = resp.maxAge
		}
		if err != nil {
			return err
		}
		if resp.notMod {
			f.mu.Lock()
			f.n304++
			f.mu.Unlock()
			return nil
		}

		now := time.Now().UTC()
		var stories []*cluster.Story
		if fd.IsSitemap() {
			stories, items, err = f.sitemapStories(ctx, fd, resp.body, now)
		} else {
			stories, items, err = f.feedStories(fd, resp.body, now)
		}
		if err != nil {
			return err
		}

		ids := make([]string, len(stories))
		for i, s := range stories {
			ids[i] = s.ID
			s.Backfill = !warm
		}
		known, err := f.store.Known(ctx, ids)
		if err != nil {
			return fmt.Errorf("store lookup: %w", err)
		}
		persist := f.engine.Ingest(stories, known)
		if err := f.store.SaveStories(ctx, persist); err != nil {
			return fmt.Errorf("store save: %w", err)
		}

		next := store.FeedState{ETag: resp.etag, LastModified: resp.lastMod}
		if next != prev {
			f.mu.Lock()
			f.state[fd.ID] = next
			f.mu.Unlock()
			if err := f.store.SaveFeedState(ctx, fd.ID, next); err != nil {
				f.log.Warn("save feed state", "feed", fd.ID, "err", err)
			}
		}

		f.mu.Lock()
		st := f.status[fd.ID]
		for _, s := range persist {
			if known[s.ID] {
				continue
			}
			st.NewTotal++
			// Published == Seen means the feed gave no date; skip those.
			if warm && !s.Published.Equal(s.Seen) {
				if d := s.Seen.Sub(s.Published); d >= 0 && d <= 6*time.Hour {
					f.lags[fd.Kind].add(d)
				}
			}
		}
		f.mu.Unlock()
		if !fd.Classify {
			for _, s := range persist {
				if !known[s.ID] {
					f.model.Learn(s.Title, fd.Category)
				}
			}
		}
		return nil
	}()

	f.mu.Lock()
	st := f.status[fd.ID]
	st.LastFetch = time.Now()
	st.LatencyMs = time.Since(start).Milliseconds()
	if items > 0 {
		st.Items = items
	}
	if err != nil {
		st.Error = err.Error()
	} else {
		st.Error = ""
		st.LastOK = st.LastFetch
	}
	f.mu.Unlock()
	return maxAge, err
}

func (f *Fetcher) feedStories(fd config.Feed, body []byte, now time.Time) ([]*cluster.Story, int, error) {
	parsed, err := gofeed.NewParser().Parse(bytes.NewReader(body))
	if err != nil {
		return nil, 0, fmt.Errorf("parse: %w", err)
	}
	stories := toStories(fd, parsed.Items, now)
	if fd.Classify {
		kept := stories[:0]
		for _, s := range stories {
			cat, guessed := f.model.Categorize(s.Link, s.Title, "", fd.Category)
			if cat == classify.Skip {
				continue
			}
			s.Categories, s.Guessed = []string{cat}, guessed
			kept = append(kept, s)
		}
		stories = kept
	}
	if f.hubs != nil {
		if hub, self := discoverHub(body); hub != "" {
			if self == "" {
				self = fd.URL
			}
			f.mu.Lock()
			f.status[fd.ID].Hub = hub
			f.mu.Unlock()
			f.hubs.Discovered(fd.ID, hub, self)
		}
	}
	return stories, len(parsed.Items), nil
}

func (f *Fetcher) sitemapStories(ctx context.Context, fd config.Feed, body []byte, now time.Time) ([]*cluster.Story, int, error) {
	entries, children, err := parseSitemap(body)
	if err != nil {
		return nil, 0, fmt.Errorf("sitemap: %w", err)
	}
	if len(children) > 0 {
		// An index: the newest child sitemap holds the latest articles.
		child, err := f.get(ctx, children[0], store.FeedState{})
		if err != nil {
			return nil, 0, fmt.Errorf("sitemap child: %w", err)
		}
		if entries, _, err = parseSitemap(child.body); err != nil {
			return nil, 0, fmt.Errorf("sitemap child: %w", err)
		}
	}
	return toSitemapStories(fd, entries, now, f.model), len(entries), nil
}

var (
	linkTagRe = regexp.MustCompile(`(?i)<(?:atom:)?link\b[^>]*>`)
	relRe     = regexp.MustCompile(`(?i)\brel\s*=\s*["']([^"']+)["']`)
	hrefRe    = regexp.MustCompile(`(?i)\bhref\s*=\s*["']([^"']+)["']`)
)

// discoverHub finds the WebSub hub and self (topic) URLs in a feed's header.
func discoverHub(body []byte) (hub, self string) {
	head := body[:min(len(body), 64<<10)]
	for _, tag := range linkTagRe.FindAll(head, -1) {
		rel, href := relRe.FindSubmatch(tag), hrefRe.FindSubmatch(tag)
		if rel == nil || href == nil || !bytes.HasPrefix(href[1], []byte("http")) {
			continue
		}
		switch strings.ToLower(string(rel[1])) {
		case "hub":
			if hub == "" {
				hub = string(href[1])
			}
		case "self":
			if self == "" {
				self = string(href[1])
			}
		}
	}
	return hub, self
}

func parseMaxAge(cc string) time.Duration {
	cc = strings.ToLower(cc)
	if cc == "" {
		return -1
	}
	best := time.Duration(-1)
	for _, part := range strings.Split(cc, ",") {
		k, v, ok := strings.Cut(strings.TrimSpace(part), "=")
		if !ok || (k != "max-age" && k != "s-maxage") {
			if k == "no-cache" || k == "no-store" {
				best = 0
			}
			continue
		}
		if n, err := strconv.Atoi(strings.Trim(v, `"`)); err == nil {
			best = time.Duration(n) * time.Second // s-maxage (CDN) wins if listed after max-age
		}
	}
	return best
}

type LagStats struct {
	Samples int     `json:"samples"`
	P50     float64 `json:"p50Sec"`
	P90     float64 `json:"p90Sec"`
}

type Metrics struct {
	Polls      int64               `json:"polls"`
	NotMod     int64               `json:"notModified"`
	CacheBust  int64               `json:"cacheBusted"`
	Lag        map[string]LagStats `json:"lagByKind"` // publish time → first seen
	FetchP50   int64               `json:"fetchP50Ms"`
	FetchP90   int64               `json:"fetchP90Ms"`
	AvgPoll    float64             `json:"avgPollSec"`
	Pushes     int                 `json:"websubPushes"`
	ModelTrain int                 `json:"classifierExamples"`
}

func (f *Fetcher) Metrics() Metrics {
	f.mu.Lock()
	defer f.mu.Unlock()
	m := Metrics{Polls: f.polls, NotMod: f.n304, CacheBust: f.busted, Lag: map[string]LagStats{}, ModelTrain: f.model.Size()}
	for kind, r := range f.lags {
		if len(r.vals) == 0 {
			continue
		}
		l := append([]time.Duration(nil), r.vals...)
		sort.Slice(l, func(i, j int) bool { return l[i] < l[j] })
		m.Lag[kind] = LagStats{Samples: len(l), P50: l[len(l)/2].Seconds(), P90: l[len(l)*9/10].Seconds()}
	}
	var fetch []int64
	var sum, n float64
	for _, s := range f.status {
		if s.LatencyMs > 0 {
			fetch = append(fetch, s.LatencyMs)
		}
		if s.Interval > 0 {
			sum += float64(s.Interval)
			n++
		}
		m.Pushes += s.Pushes
	}
	if len(fetch) > 0 {
		sort.Slice(fetch, func(i, j int) bool { return fetch[i] < fetch[j] })
		m.FetchP50 = fetch[len(fetch)/2]
		m.FetchP90 = fetch[len(fetch)*9/10]
	}
	if n > 0 {
		m.AvgPoll = sum / n
	}
	return m
}

// Statuses returns a copy of every feed's status, sorted by category then name.
func (f *Fetcher) Statuses() []FeedStatus {
	f.mu.Lock()
	out := make([]FeedStatus, 0, len(f.status))
	for _, s := range f.status {
		out = append(out, *s)
	}
	f.mu.Unlock()
	sort.Slice(out, func(i, j int) bool {
		if out[i].Category != out[j].Category {
			return out[i].Category < out[j].Category
		}
		return out[i].Name < out[j].Name
	})
	return out
}

// Health returns how many feeds are currently healthy (last attempt succeeded).
func (f *Fetcher) Health() (ok, total int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, s := range f.status {
		if !s.LastOK.IsZero() && s.Error == "" {
			ok++
		}
	}
	return ok, len(f.status)
}
