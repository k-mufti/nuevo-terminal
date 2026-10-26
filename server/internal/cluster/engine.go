// Package cluster keeps the recent stories in memory, groups stories about the
// same event into clusters, flags breaking news and publishes changes live.
package cluster

import (
	"encoding/json"
	"math"
	"slices"
	"sort"
	"sync"
	"time"

	"nuevo-rss/internal/live"
)

const (
	// Window is how long stories stay in memory (and in the live view).
	Window = 36 * time.Hour

	matchThreshold = 0.45           // IDF-weighted cosine similarity needed to join a cluster
	minShared      = 2              // ...and at least this many shared words
	maxMatchGap    = 24 * time.Hour // only match stories published this close together
	commonCutoff   = 0.05           // words in >5% of stories are not used to find candidates

	breakingSources = 4                // distinct publishers...
	breakingWindow  = 90 * time.Minute // ...within this window...
	breakingFresh   = 2 * time.Hour    // ...and the newest one no older than this

	maxWireItems = 40
	maxSnapshot  = 10000
)

type Story struct {
	ID         string
	FeedID     string
	Publisher  string
	Source     string
	Categories []string
	Title      string
	Link       string
	Summary    string
	Published  time.Time
	Seen       time.Time
	// Guessed is true when Categories came from the classifier rather than
	// the source. A later copy with a real category replaces the guess.
	Guessed bool
	// Backfill marks stories from a feed's first fetch after startup; their
	// timing says nothing about speed.
	Backfill bool

	cluster *Cluster
	tokens  []string
	seenBy  map[string]struct{} // other feeds that carried the same link
}

// Race records the same article (same link) arriving from two feeds, e.g. a
// publisher's sitemap and its RSS feed. Gap is how much later the loser had it.
type Race struct {
	Winner, Loser string // feed IDs
	Gap           time.Duration
}

const maxRaces = 5000

type Cluster struct {
	ID         string
	members    []*Story // sorted by Published, oldest first
	breaking   bool
	breakingAt time.Time
}

func (c *Cluster) newest() time.Time { return c.members[len(c.members)-1].Published }

func (c *Cluster) insert(s *Story) {
	i := sort.Search(len(c.members), func(i int) bool { return c.members[i].Published.After(s.Published) })
	c.members = slices.Insert(c.members, i, s)
}

type Engine struct {
	mu       sync.Mutex
	stories  map[string]*Story
	clusters map[string]*Cluster
	index    map[string]map[*Story]struct{} // word -> stories containing it
	df       map[string]int                 // word -> number of stories containing it
	hub      *live.Hub
	now      func() time.Time
	seq      uint64 // bumped on every published change to clusters
	races    []Race
	racePos  int
}

func NewEngine(hub *live.Hub) *Engine {
	return &Engine{
		stories:  make(map[string]*Story),
		clusters: make(map[string]*Cluster),
		index:    make(map[string]map[*Story]struct{}),
		df:       make(map[string]int),
		hub:      hub,
		now:      time.Now,
	}
}

// Restore loads stories from storage at startup without publishing anything.
func (e *Engine) Restore(stories []*Story) {
	e.mu.Lock()
	defer e.mu.Unlock()
	cutoff := e.now().Add(-Window)
	for _, s := range stories {
		if _, dup := e.stories[s.ID]; dup || s.Published.Before(cutoff) {
			continue
		}
		e.add(s)
	}
	now := e.now()
	for _, c := range e.clusters {
		e.updateBreaking(c, now)
	}
}

// Ingest adds freshly fetched stories. known holds IDs already in storage
// (seen before, possibly expired from memory) so they are not re-added.
// It returns copies of the stories that need saving (new ones, or ones that
// gained a category because a second feed carried the same link).
func (e *Engine) Ingest(in []*Story, known map[string]bool) []Story {
	e.mu.Lock()
	defer e.mu.Unlock()
	now := e.now()
	cutoff := now.Add(-Window)

	var persist []Story
	dirty := make(map[*Cluster]struct{})
	for _, s := range in {
		if ex, ok := e.stories[s.ID]; ok {
			e.noteRace(ex, s)
			if mergeStory(ex, s) {
				dirty[ex.cluster] = struct{}{}
				persist = append(persist, ex.copy())
			}
			continue
		}
		if known[s.ID] || s.Published.Before(cutoff) {
			continue
		}
		e.add(s)
		dirty[s.cluster] = struct{}{}
		persist = append(persist, s.copy())
	}

	if len(dirty) > 0 {
		changed := make([]*Cluster, 0, len(dirty))
		for c := range dirty {
			e.updateBreaking(c, now)
			changed = append(changed, c)
		}
		e.publishClusters(changed)
	}
	return persist
}

// Sweep drops clusters that fell out of the window and expires breaking flags.
func (e *Engine) Sweep() {
	e.mu.Lock()
	defer e.mu.Unlock()
	now := e.now()
	cutoff := now.Add(-Window)
	var removed []string
	var changed []*Cluster
	for id, c := range e.clusters {
		if c.newest().Before(cutoff) {
			for _, s := range c.members {
				e.removeStory(s)
			}
			delete(e.clusters, id)
			removed = append(removed, id)
			continue
		}
		if c.breaking && e.updateBreaking(c, now) {
			changed = append(changed, c)
		}
	}
	if len(removed) > 0 {
		b, _ := json.Marshal(removed)
		e.seq++
		e.hub.Publish(live.Event{Type: "remove", Data: b, Seq: e.seq})
	}
	if len(changed) > 0 {
		e.publishClusters(changed)
	}
}

// Subscribe registers for every change after the returned version. The client
// then fetches a snapshot (always at that version or later) and applies only
// the changes newer than the snapshot, so nothing is missed or applied twice.
func (e *Engine) Subscribe() (uint64, *live.Sub) {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.seq, e.hub.Subscribe()
}

// Seq is the current version of the story state.
func (e *Engine) Seq() uint64 {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.seq
}

// Snapshot returns the clusters first reported in [from, to) (zero = open
// ended), newest first, and the version it reflects.
func (e *Engine) Snapshot(from, to time.Time) ([]WireCluster, uint64) {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([]WireCluster, 0, len(e.clusters))
	for _, c := range e.clusters {
		t := c.members[0].Published
		if (!from.IsZero() && t.Before(from)) || (!to.IsZero() && !t.Before(to)) {
			continue
		}
		out = append(out, c.wire())
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Time > out[j].Time })
	if len(out) > maxSnapshot {
		out = out[:maxSnapshot]
	}
	return out, e.seq
}

func (e *Engine) noteRace(ex, s *Story) {
	if s.FeedID == ex.FeedID {
		return
	}
	if ex.seenBy == nil {
		ex.seenBy = make(map[string]struct{})
	}
	if _, dup := ex.seenBy[s.FeedID]; dup {
		return
	}
	ex.seenBy[s.FeedID] = struct{}{}
	gap := s.Seen.Sub(ex.Seen)
	if s.Backfill || gap < 0 || gap > 6*time.Hour {
		return
	}
	// Only count articles that were new when the winner saw them. Sitemaps
	// re-list old articles whenever they're edited; that isn't a race.
	if ex.Seen.Sub(ex.Published) > 30*time.Minute {
		return
	}
	r := Race{Winner: ex.FeedID, Loser: s.FeedID, Gap: gap}
	if len(e.races) < maxRaces {
		e.races = append(e.races, r)
	} else {
		e.races[e.racePos] = r
		e.racePos = (e.racePos + 1) % maxRaces
	}
}

// Races returns the recorded same-article races.
func (e *Engine) Races() []Race {
	e.mu.Lock()
	defer e.mu.Unlock()
	return slices.Clone(e.races)
}

// FirstReporters counts, over clusters covered by at least minSources
// publishers, which publisher had the story first.
func (e *Engine) FirstReporters(minSources int) map[string]int {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make(map[string]int)
	for _, c := range e.clusters {
		pubs := make(map[string]struct{})
		for _, s := range c.members {
			pubs[s.Publisher] = struct{}{}
		}
		if len(pubs) >= minSources {
			out[c.members[0].Publisher]++
		}
	}
	return out
}

func (e *Engine) Stats() (stories, clusters int) {
	e.mu.Lock()
	defer e.mu.Unlock()
	return len(e.stories), len(e.clusters)
}

func (e *Engine) add(s *Story) {
	s.tokens = Tokenize(s.Title)
	c := e.bestCluster(s)
	if c == nil {
		c = &Cluster{ID: s.ID}
		e.clusters[c.ID] = c
	}
	c.insert(s)
	s.cluster = c
	e.stories[s.ID] = s
	for _, t := range s.tokens {
		m := e.index[t]
		if m == nil {
			m = make(map[*Story]struct{})
			e.index[t] = m
		}
		m[s] = struct{}{}
		e.df[t]++
	}
}

func (e *Engine) removeStory(s *Story) {
	delete(e.stories, s.ID)
	for _, t := range s.tokens {
		if m := e.index[t]; m != nil {
			delete(m, s)
			if len(m) == 0 {
				delete(e.index, t)
			}
		}
		if e.df[t]--; e.df[t] <= 0 {
			delete(e.df, t)
		}
	}
}

func (e *Engine) idf(t string, n float64) float64 {
	return math.Log(1 + n/float64(1+e.df[t]))
}

// bestCluster finds the existing cluster most similar to s, or nil.
func (e *Engine) bestCluster(s *Story) *Cluster {
	if len(s.tokens) < minShared {
		return nil
	}
	n := float64(len(e.stories) + 1)
	limit := max(20, int(n*commonCutoff))

	candidates := make(map[*Story]struct{})
	for _, t := range s.tokens {
		if e.df[t] > limit {
			continue
		}
		for c := range e.index[t] {
			candidates[c] = struct{}{}
		}
	}
	if len(candidates) == 0 {
		return nil
	}

	weights := make(map[string]float64, len(s.tokens))
	var normS float64
	for _, t := range s.tokens {
		w := e.idf(t, n)
		weights[t] = w
		normS += w * w
	}

	var best *Cluster
	bestScore := 0.0
	for c := range candidates {
		if gap := s.Published.Sub(c.Published); gap > maxMatchGap || gap < -maxMatchGap {
			continue
		}
		var dot, normC float64
		shared := 0
		for _, t := range c.tokens {
			w := e.idf(t, n)
			normC += w * w
			if ws, ok := weights[t]; ok {
				dot += ws * w
				shared++
			}
		}
		if shared < minShared || normC == 0 {
			continue
		}
		score := dot / math.Sqrt(normS*normC)
		if score >= matchThreshold && score > bestScore {
			best, bestScore = c.cluster, score
		}
	}
	return best
}

// updateBreaking recomputes the breaking flag and reports whether it changed.
func (e *Engine) updateBreaking(c *Cluster, now time.Time) bool {
	brk := false
	newest := c.newest()
	if now.Sub(newest) <= breakingFresh {
		from := newest.Add(-breakingWindow)
		pubs := make(map[string]struct{})
		for i := len(c.members) - 1; i >= 0 && !c.members[i].Published.Before(from); i-- {
			pubs[c.members[i].Publisher] = struct{}{}
		}
		brk = len(pubs) >= breakingSources
	}
	if brk == c.breaking {
		return false
	}
	c.breaking = brk
	if brk {
		c.breakingAt = now
	}
	return true
}

func (e *Engine) publishClusters(cs []*Cluster) {
	out := make([]WireCluster, len(cs))
	for i, c := range cs {
		out[i] = c.wire()
	}
	b, err := json.Marshal(out)
	if err != nil {
		return
	}
	e.seq++
	e.hub.Publish(live.Event{Type: "clusters", Data: b, Seq: e.seq})
}

func (s *Story) copy() Story {
	cp := *s
	cp.Categories = slices.Clone(s.Categories)
	cp.cluster, cp.tokens = nil, nil
	return cp
}

// mergeStory folds a second copy of the same article (same link, another
// feed) into the existing story. It reports whether anything changed.
func mergeStory(ex, s *Story) bool {
	changed := false
	if ex.Guessed && !s.Guessed {
		ex.Categories, ex.Guessed = slices.Clone(s.Categories), false
		changed = true
	} else if !s.Guessed || ex.Guessed {
		changed = mergeCats(&ex.Categories, s.Categories)
	}
	if ex.Summary == "" && s.Summary != "" {
		ex.Summary = s.Summary
		changed = true
	}
	return changed
}

func mergeCats(dst *[]string, add []string) bool {
	changed := false
	for _, c := range add {
		if !slices.Contains(*dst, c) {
			*dst = append(*dst, c)
			changed = true
		}
	}
	return changed
}
