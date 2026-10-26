// Package config loads the list of RSS/Atom feeds to poll.
package config

import (
	"encoding/json"
	"fmt"
	"os"
	"time"
)

type Feed struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Publisher string `json:"publisher"` // used to count distinct sources (BBC World + BBC Tech = 1 source)
	Category  string `json:"category"`
	URL       string `json:"url"`
	// Interval overrides the default poll interval, in seconds.
	IntervalSec int `json:"interval,omitempty"`
	// Kind is "rss" (default; RSS or Atom) or "sitemap" (a Google News sitemap).
	Kind string `json:"kind,omitempty"`
	// Bust adds a changing query parameter so the publisher's CDN cache is
	// skipped. Only for large publishers whose feeds are cached for minutes.
	Bust bool `json:"bust,omitempty"`
	// Classify guesses each story's category instead of using Category for
	// all of them (Category becomes the fallback). Always on for sitemaps.
	Classify bool `json:"classify,omitempty"`
	// Lang keeps only sitemap entries in this language. Default "en"; "*" keeps all.
	Lang string `json:"lang,omitempty"`
}

func (f Feed) IsSitemap() bool { return f.Kind == "sitemap" }

func (f Feed) Interval(def time.Duration) time.Duration {
	if f.IntervalSec > 0 {
		return time.Duration(f.IntervalSec) * time.Second
	}
	return def
}

func LoadFeeds(path string) ([]Feed, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var feeds []Feed
	if err := json.Unmarshal(b, &feeds); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	seen := make(map[string]bool, len(feeds))
	for i := range feeds {
		f := &feeds[i]
		if f.ID == "" || f.URL == "" || f.Category == "" {
			return nil, fmt.Errorf("feed #%d (%q) needs id, url and category", i, f.Name)
		}
		if seen[f.ID] {
			return nil, fmt.Errorf("duplicate feed id %q", f.ID)
		}
		seen[f.ID] = true
		if f.Name == "" {
			f.Name = f.ID
		}
		if f.Publisher == "" {
			f.Publisher = f.Name
		}
		switch f.Kind {
		case "", "rss":
			f.Kind = "rss"
		case "sitemap":
			f.Classify = true
		default:
			return nil, fmt.Errorf("feed %q: unknown kind %q", f.ID, f.Kind)
		}
		if f.Lang == "" {
			f.Lang = "en"
		}
	}
	return feeds, nil
}
