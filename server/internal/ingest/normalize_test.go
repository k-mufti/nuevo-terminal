package ingest

import (
	"bytes"
	"compress/gzip"
	"testing"
	"time"

	"nuevo-rss/internal/cluster"
)

func TestCanonicalURL(t *testing.T) {
	cases := map[string]string{
		"https://WWW.BBC.co.uk/news/world-123/?utm_source=rss&at_medium=RSS#top": "https://www.bbc.co.uk/news/world-123",
		"https://example.com/a?id=5&fbclid=abc":                                  "https://example.com/a?id=5",
		"":                                                                       "",
	}
	for in, want := range cases {
		if got := canonicalURL(in); got != want {
			t.Errorf("canonicalURL(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestCleanText(t *testing.T) {
	if got := cleanText("<p>Hello&amp;amp; <b>world</b></p>\n\n"); got != "Hello& world" {
		t.Errorf("got %q", got)
	}
}

func TestSpam(t *testing.T) {
	for _, s := range []string{"Dabble Promo Code YAHOOBONUS: Claim $20", "Betr Promo Code FOXNEWS: Get a $200 Bonus", "Fanatics Sportsbook Promo Code FOXNEWS350: Bet $20, Get $350"} {
		if !spamRe.MatchString(s) {
			t.Errorf("not caught: %q", s)
		}
	}
	if spamRe.MatchString("Arsenal go top of Premier League") {
		t.Error("false positive")
	}
}

func TestParseMaxAge(t *testing.T) {
	cases := map[string]int{"public, max-age=300": 300, "max-age=60, s-maxage=120": 120, "no-cache, no-store, max-age=0": 0, "": -1, "private": -1}
	for in, want := range cases {
		got := parseMaxAge(in)
		if (want < 0 && got >= 0) || (want >= 0 && int(got.Seconds()) != want) {
			t.Errorf("parseMaxAge(%q) = %v, want %d", in, got, want)
		}
	}
}

func TestParseSitemap(t *testing.T) {
	urlset := []byte(`<?xml version="1.0"?><urlset xmlns="http://www.sitemaps.org/schemas/sitemap/0.9" xmlns:news="http://www.google.com/schemas/sitemap-news/0.9">
<url><loc>https://www.reuters.com/world/middle-east/x-2026-10-25/</loc><news:news><news:publication><news:name>Reuters</news:name><news:language>en</news:language></news:publication>
<news:publication_date>2026-10-25T14:03:00.123Z</news:publication_date><news:title>Israel &amp; Iran talk</news:title></news:news></url>
<url><loc>https://www.bbc.com/arabic/a1</loc><news:news><news:publication><news:language>ar</news:language></news:publication><news:publication_date>2026-10-25T14:00:00Z</news:publication_date><news:title>عنوان</news:title></news:news></url>
</urlset>`)
	entries, children, err := parseSitemap(urlset)
	if err != nil || len(children) != 0 || len(entries) != 2 {
		t.Fatalf("entries=%d children=%d err=%v", len(entries), len(children), err)
	}
	if entries[0].News.Title != "Israel & Iran talk" || parseTime(entries[0].News.PubDate).IsZero() {
		t.Fatalf("bad entry: %+v", entries[0])
	}

	index := []byte(`<sitemapindex><sitemap><loc>https://a/old.xml</loc><lastmod>2026-10-24T00:00:00Z</lastmod></sitemap>
<sitemap><loc>https://a/new.xml</loc><lastmod>2026-10-25T00:00:00Z</lastmod></sitemap></sitemapindex>`)
	_, children, err = parseSitemap(index)
	if err != nil || len(children) != 2 || children[0] != "https://a/new.xml" {
		t.Fatalf("children=%v err=%v", children, err)
	}
}

func TestDiscoverHub(t *testing.T) {
	body := []byte(`<rss><channel><atom:link rel="hub" href="https://pubsubhubbub.appspot.com/"/><atom:link href="https://x.com/feed" rel="self" type="application/rss+xml"/>`)
	hub, self := discoverHub(body)
	if hub != "https://pubsubhubbub.appspot.com/" || self != "https://x.com/feed" {
		t.Fatalf("hub=%q self=%q", hub, self)
	}
}

func TestLooksEnglish(t *testing.T) {
	if looksEnglish("Lula lidera apuração do 2º turno com 50,55% contra 49,45% de Flávio") {
		t.Error("Portuguese passed")
	}
	if !looksEnglish("Venezuela opposition leader Machado returns home after fraught year away") {
		t.Error("English rejected")
	}
	if !looksEnglish("Real Madrid beat Barcelona in El Clasico") {
		t.Error("English with Spanish names rejected")
	}
}

func TestGunzipAndHasTime(t *testing.T) {
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	zw.Write([]byte("<urlset/>"))
	zw.Close()
	got, err := gunzip(buf.Bytes())
	if err != nil || string(got) != "<urlset/>" {
		t.Fatalf("gunzip = %q, %v", got, err)
	}

	now := time.Date(2026, 10, 28, 15, 4, 5, 0, time.UTC)
	cases := []struct {
		pub  time.Time
		want bool
	}{
		{now.Add(-time.Minute), true},
		{now, false}, // no date in the feed
		{time.Date(2026, 10, 28, 0, 0, 0, 0, time.UTC), false}, // date only
	}
	for _, c := range cases {
		if got := hasTime(&cluster.Story{Published: c.pub, Seen: now}); got != c.want {
			t.Errorf("hasTime(%v) = %v, want %v", c.pub, got, c.want)
		}
	}
}
