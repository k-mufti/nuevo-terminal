package cluster

import (
	"testing"
	"time"

	"nuevo-rss/internal/live"
)

func story(id, pub, cat, title string, at time.Time) *Story {
	return &Story{ID: id, Publisher: pub, Source: pub, Categories: []string{cat}, Title: title, Published: at, Seen: at}
}

func TestClusteringAndBreaking(t *testing.T) {
	now := time.Now()
	e := NewEngine(live.NewHub())
	e.now = func() time.Time { return now }

	// Background noise so word weights are realistic.
	noise := []string{
		"Stocks rise as investors weigh earnings", "Apple unveils new iPad lineup", "Storm batters coast of Florida",
		"Champions League draw announced", "Senate passes spending bill", "Scientists discover new exoplanet",
		"Oil prices fall on supply fears", "Election results delayed in Alaska", "Kim Jong Un visits China",
	}
	for i, n := range noise {
		e.Ingest([]*Story{story("n"+string(rune('a'+i)), "Noise", "world", n, now.Add(-time.Hour))}, nil)
	}

	same := []*Story{
		story("1", "BBC", "world", "Gunmen kill seven in attack on Jordan church", now.Add(-30*time.Minute)),
		story("2", "NYT", "world", "Gunmen open fire at church in Jordan, killing at least seven", now.Add(-25*time.Minute)),
		story("3", "CBS", "us", "Gunmen kill at least 7 in attack on Jordan church, authorities say", now.Add(-20*time.Minute)),
		story("4", "NPR", "us", "Gunmen open fire at a church funeral in Jordan", now.Add(-10*time.Minute)),
	}
	e.Ingest(same, nil)
	other := story("5", "ESPN", "sports", "Valkyries win Game 4 to level WNBA finals", now)
	e.Ingest([]*Story{other}, nil)

	c := e.stories["1"].cluster
	for _, s := range same {
		if s.cluster != c {
			t.Fatalf("story %q not clustered with the others", s.Title)
		}
	}
	if other.cluster == c {
		t.Fatal("unrelated story joined the cluster")
	}
	if !c.breaking {
		t.Fatal("4 publishers within 90 minutes should be breaking")
	}
	if w := c.wire(); w.N != 4 || w.Title != same[0].Title {
		t.Fatalf("wire: n=%d title=%q", w.N, w.Title)
	}

	// Breaking expires once the story goes quiet.
	now = now.Add(3 * time.Hour)
	e.Sweep()
	if c.breaking {
		t.Fatal("breaking should expire")
	}
}

func TestDuplicateLinkAddsCategory(t *testing.T) {
	e := NewEngine(live.NewHub())
	now := time.Now()
	e.Ingest([]*Story{story("x", "BBC", "world", "Israel and Hamas agree ceasefire", now)}, nil)
	persist := e.Ingest([]*Story{story("x", "BBC", "mideast", "Israel and Hamas agree ceasefire", now)}, nil)
	if len(persist) != 1 || len(e.stories["x"].Categories) != 2 {
		t.Fatalf("expected category merge, got %v", e.stories["x"].Categories)
	}
}

func TestCategoriesShare(t *testing.T) {
	now := time.Now()
	c := &Cluster{}
	for i, cat := range []string{"sports", "sports", "sports", "mideast", "world"} {
		c.insert(story(string(rune('a'+i)), "P", cat, "x", now))
	}
	if got := c.categories(); len(got) != 1 || got[0] != "sports" {
		t.Fatalf("got %v, want [sports]", got)
	}
}

func TestStemConsistency(t *testing.T) {
	groups := [][]string{
		{"kill", "kills", "killed", "killing"},
		{"strike", "strikes", "striking"},
		{"stop", "stopped", "stopping", "stops"},
		{"agree", "agreed", "agrees"},
	}
	for _, g := range groups {
		for _, w := range g[1:] {
			if stem(w) != stem(g[0]) {
				t.Errorf("stem(%q)=%q, stem(%q)=%q", w, stem(w), g[0], stem(g[0]))
			}
		}
	}
	if got := Tokenize("Seven killed"); got[0] != "7" {
		t.Errorf("number word not converted: %v", got)
	}
}

func TestTokenize(t *testing.T) {
	got := Tokenize("U.S. strikes Iran's nuclear sites, officials say")
	want := []string{"usa", "strik", "iran", "nuclear", "sit", "official"}
	if len(got) != len(want) {
		t.Fatalf("got %v want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v want %v", got, want)
		}
	}
}
