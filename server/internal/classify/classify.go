// Package classify decides which panel a story belongs in when its source
// doesn't say (news sitemaps, general "all news" feeds).
//
// It tries, in order: the URL's section (/sport/, /world/middle-east/), the
// sitemap's keywords, then a naive Bayes model over headline words that is
// trained continuously on stories from single-topic feeds.
package classify

import (
	"math"
	"net/url"
	"strings"
	"sync"

	"nuevo-rss/internal/cluster"
)

// Skip marks sections that are not news for this terminal (video, recipes, shopping...).
const Skip = "skip"

// When several URL segments match, the more specific category wins.
var priority = []string{Skip, "sports", "mideast", "tech", "science", "markets", "us", "world"}

var sections = map[string][]string{
	"sports": {"sport", "sports", "football", "soccer", "nfl", "nba", "mlb", "nhl", "wnba", "ncaa", "college-football",
		"tennis", "golf", "cricket", "rugby", "rugby-union", "boxing", "mma", "ufc", "f1", "formula1", "formula-one",
		"olympics", "motorsport", "cycling", "athletics"},
	"mideast": {"middle-east", "middleeast", "mideast", "israel", "iran", "gaza", "lebanon", "syria", "iraq", "yemen",
		"saudi-arabia", "israel-hamas-war", "israel-palestine", "palestine", "qatar", "uae", "jordan", "gulf"},
	"tech": {"technology", "tech", "ai", "artificial-intelligence", "cybersecurity", "gadgets", "innovation", "computing", "internet"},
	"science": {"science", "health", "environment", "climate", "space", "wellness", "medicine", "climate-environment",
		"sustainability", "climate-change"},
	"markets": {"business", "markets", "market", "finance", "economy", "money", "investing", "personal-finance",
		"companies", "earnings", "crypto", "cryptocurrency", "currencies", "commodities", "energy", "deals", "banking",
		"real-estate", "economics"},
	"us": {"us", "us-news", "politics", "us-politics", "election", "elections", "congress", "white-house",
		"supreme-court", "washington", "national"},
	"world": {"world", "international", "europe", "asia", "africa", "americas", "uk", "uk-news", "australia-news",
		"china", "india", "ukraine", "russia", "asia-pacific", "latin-america", "global-development", "world-news"},
	Skip: {"video", "videos", "podcasts", "podcast", "audio", "recipes", "crosswords", "games", "puzzles",
		"horoscope", "horoscopes", "shopping", "underscored", "select", "lifestyle", "lifeandstyle", "style",
		"fashion", "food", "travel", "entertainment", "celebrity", "arts", "culture", "books", "movies", "film",
		"tv", "tv-and-radio", "music", "weddings", "opinion", "commentisfree", "newsround", "artanddesign",
		"arts-culture", "obituaries", "photography", "gallery", "pictures", "quiz", "coupons", "reviews"},
}

var sectionOf = func() map[string]string {
	m := make(map[string]string)
	for cat, words := range sections {
		for _, w := range words {
			m[w] = cat
		}
	}
	return m
}()

func rank(cat string) int {
	for i, c := range priority {
		if c == cat {
			return i
		}
	}
	return len(priority)
}

func best(words []string) string {
	found := ""
	for _, w := range words {
		if cat, ok := sectionOf[w]; ok && (found == "" || rank(cat) < rank(found)) {
			found = cat
		}
	}
	return found
}

// FromURL returns the category implied by the URL path, or "".
func FromURL(link string) string {
	u, err := url.Parse(link)
	if err != nil {
		return ""
	}
	return best(strings.Split(strings.ToLower(strings.Trim(u.Path, "/")), "/"))
}

// FromKeywords maps sitemap keywords ("Middle East, Israel, Politics") to a category, or "".
func FromKeywords(kw string) string {
	var words []string
	for _, k := range strings.Split(kw, ",") {
		k = strings.ToLower(strings.TrimSpace(k))
		if k != "" {
			words = append(words, strings.ReplaceAll(k, " ", "-"))
		}
	}
	return best(words)
}

// Model is a multinomial naive Bayes classifier over headline words.
type Model struct {
	mu     sync.RWMutex
	docs   map[string]int
	words  map[string]map[string]int
	totals map[string]int
	vocab  map[string]struct{}
	n      int
}

func NewModel() *Model {
	return &Model{docs: map[string]int{}, words: map[string]map[string]int{}, totals: map[string]int{}, vocab: map[string]struct{}{}}
}

func (m *Model) Learn(title, cat string) {
	toks := cluster.Tokenize(title)
	if len(toks) == 0 || cat == "" {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.n++
	m.docs[cat]++
	w := m.words[cat]
	if w == nil {
		w = map[string]int{}
		m.words[cat] = w
	}
	for _, t := range toks {
		w[t]++
		m.totals[cat]++
		m.vocab[t] = struct{}{}
	}
}

func (m *Model) Size() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.n
}

// Predict returns the most likely category and its probability.
func (m *Model) Predict(title string) (string, float64) {
	return m.predict(title, "")
}

// sourcePrior is how much more likely a story is to be in its source's usual
// category before reading the headline. It stops one ambiguous word
// ("defense") from moving a Bloomberg story into Sports.
const sourcePrior = 6.0

func (m *Model) predict(title, prior string) (string, float64) {
	toks := cluster.Tokenize(title)
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.n < 50 || len(toks) == 0 {
		return "", 0
	}
	known := 0
	for _, t := range toks {
		if _, ok := m.vocab[t]; ok {
			known++
		}
	}
	if known < 2 { // too little evidence to judge
		return "", 0
	}
	v := float64(len(m.vocab))
	scores := make(map[string]float64, len(m.docs))
	top, bestCat := math.Inf(-1), ""
	for cat, d := range m.docs {
		s := math.Log(float64(d) / float64(m.n))
		if cat == prior {
			s += math.Log(sourcePrior)
		}
		w, total := m.words[cat], float64(m.totals[cat])
		for _, t := range toks {
			if _, known := m.vocab[t]; !known {
				continue
			}
			s += math.Log((float64(w[t]) + 1) / (total + v))
		}
		scores[cat] = s
		if s > top {
			top, bestCat = s, cat
		}
	}
	var sum float64
	for _, s := range scores {
		sum += math.Exp(s - top)
	}
	return bestCat, 1 / sum
}

// minConfidence is how sure the model must be before overriding the feed's default.
const minConfidence = 0.6

// Categorize picks a category for one story. guessed is false when the URL
// or keywords said so explicitly.
func (m *Model) Categorize(link, title, keywords, fallback string) (cat string, guessed bool) {
	if c := FromURL(link); c != "" {
		return c, false
	}
	if c := FromKeywords(keywords); c != "" {
		return c, false
	}
	if c, p := m.predict(title, fallback); c != "" && p >= minConfidence {
		return c, true
	}
	return fallback, true
}
