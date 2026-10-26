package classify

import (
	"database/sql"
	"math/rand/v2"
	"os"
	"testing"

	_ "modernc.org/sqlite"
)

func TestFromURL(t *testing.T) {
	cases := map[string]string{
		"https://www.reuters.com/world/middle-east/israel-strikes-2026-10-25/": "mideast",
		"https://www.reuters.com/world/us/senate-vote-2026-10-25/":             "us",
		"https://www.cnn.com/2026/10/25/americas/brazil-election":              "world",
		"https://www.theguardian.com/sport/2026/oct/25/arsenal":                "sports",
		"https://www.bbc.com/news/videos/c123":                                 Skip,
		"https://www.bbc.com/news/articles/c123":                               "",
		"https://www.wsj.com/finance/stocks/fed-meeting-123":                   "markets",
	}
	for in, want := range cases {
		if got := FromURL(in); got != want {
			t.Errorf("FromURL(%s) = %q, want %q", in, got, want)
		}
	}
}

// TestModelAccuracy trains on 80% of the stored RSS headlines and reports
// accuracy on the rest. Run with NUEVO_DB=data/nuevo.db (from server/).
func TestModelAccuracy(t *testing.T) {
	path := os.Getenv("NUEVO_DB")
	if path == "" {
		t.Skip("set NUEVO_DB to evaluate on real data")
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	// Single-topic feeds only; general feeds have noisy labels.
	rows, err := db.Query(`SELECT title, categories FROM stories WHERE feed_id NOT IN
		('al-jazeera','asharq-al-awsat','dw','euronews','france-24','haaretz','the-national','arab-news','abc-news-intl')
		AND categories NOT LIKE '%,%'`)
	if err != nil {
		t.Fatal(err)
	}
	type ex struct{ title, cat string }
	var all []ex
	for rows.Next() {
		var e ex
		rows.Scan(&e.title, &e.cat)
		all = append(all, e)
	}
	rows.Close()
	r := rand.New(rand.NewPCG(1, 2))
	r.Shuffle(len(all), func(i, j int) { all[i], all[j] = all[j], all[i] })
	split := len(all) * 8 / 10
	m := NewModel()
	for _, e := range all[:split] {
		m.Learn(e.title, e.cat)
	}
	var correct, confident, confidentCorrect int
	for _, e := range all[split:] {
		c, p := m.Predict(e.title)
		if c == e.cat {
			correct++
		}
		if p >= minConfidence {
			confident++
			if c == e.cat {
				confidentCorrect++
			}
		}
	}
	test := len(all) - split
	t.Logf("trained on %d, tested on %d", split, test)
	t.Logf("accuracy (all): %.1f%%", 100*float64(correct)/float64(test))
	t.Logf("confident (p>=%.1f): %.1f%% of stories, %.1f%% correct", minConfidence,
		100*float64(confident)/float64(test), 100*float64(confidentCorrect)/float64(max(confident, 1)))
}

func TestSourcePrior(t *testing.T) {
	m := NewModel()
	for i := 0; i < 40; i++ {
		m.Learn("Defense holds as Chiefs beat Raiders in overtime thriller", "sports")
		m.Learn("Stocks rally as Fed signals rate cut, Treasury yields fall", "markets")
		m.Learn("Firms report weak demand as costs rise, a growing problem for investors", "markets")
	}
	cat, _ := m.Categorize("https://www.bloomberg.com/news/articles/x", "For defense firms, too much demand is the new problem", "", "markets")
	if cat != "markets" {
		t.Errorf("got %q; one sports word should not override the source's category", cat)
	}
	cat, _ = m.Categorize("https://www.bloomberg.com/news/articles/y", "Chiefs beat Raiders in overtime as defense holds", "", "markets")
	if cat != "sports" {
		t.Errorf("got %q; a clearly sports headline should still be sports", cat)
	}
}
