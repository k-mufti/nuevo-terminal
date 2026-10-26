package ingest

import (
	"crypto/sha1"
	"encoding/hex"
	"html"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/mmcdole/gofeed"

	"nuevo-rss/internal/classify"
	"nuevo-rss/internal/cluster"
	"nuevo-rss/internal/config"
)

const (
	maxItemsPerFeed    = 100
	maxItemsPerSitemap = 300
	maxSummaryRunes    = 280
	futureTolerance    = 5 * time.Minute
)

var (
	tagRe   = regexp.MustCompile(`<[^>]*>`)
	spaceRe = regexp.MustCompile(`\s+`)
	// Affiliate/betting promos that some sports feeds mix in with the news.
	spamRe = regexp.MustCompile(`(?i)\b(promo|bonus|referral|coupon) code\b|\bsportsbook promo\b|\bbetting promo\b|\bbet \$\d+,? get\b`)
)

// toStories converts parsed feed items into stories, newest first.
func toStories(feed config.Feed, items []*gofeed.Item, now time.Time) []*cluster.Story {
	out := make([]*cluster.Story, 0, min(len(items), maxItemsPerFeed))
	for _, it := range items {
		title := cleanText(it.Title)
		if title == "" || spamRe.MatchString(title) {
			continue
		}
		link := canonicalURL(strings.TrimSpace(it.Link))
		if link == "" && strings.HasPrefix(it.GUID, "http") {
			link = canonicalURL(it.GUID)
		}
		key := link
		if key == "" {
			key = feed.ID + "|" + it.GUID + "|" + title
		}

		published := now
		if it.PublishedParsed != nil {
			published = *it.PublishedParsed
		} else if it.UpdatedParsed != nil {
			published = *it.UpdatedParsed
		}
		if published.After(now.Add(futureTolerance)) {
			published = now // bad clocks or time zones; don't let a story pin itself to the top
		}

		summary := it.Description
		if summary == "" {
			summary = it.Content
		}
		summary = truncate(cleanText(summary), maxSummaryRunes)
		// hnrss and similar aggregators put link boilerplate in the description.
		if strings.EqualFold(summary, title) || strings.HasPrefix(summary, "Article URL:") || strings.Contains(summary, "Comments URL:") {
			summary = ""
		}

		out = append(out, &cluster.Story{
			ID:         hashID(key),
			FeedID:     feed.ID,
			Publisher:  feed.Publisher,
			Source:     feed.Name,
			Categories: []string{feed.Category},
			Title:      title,
			Link:       link,
			Summary:    summary,
			Published:  published.UTC(),
			Seen:       now,
		})
	}
	// Some feeds list hundreds of items; keep the newest.
	if len(out) > maxItemsPerFeed {
		sortNewestFirst(out)
		out = out[:maxItemsPerFeed]
	}
	return out
}

// toSitemapStories converts news-sitemap entries into stories. Sitemaps carry
// no section, so each story's category comes from the URL, keywords or the
// headline model.
func toSitemapStories(feed config.Feed, entries []sitemapEntry, now time.Time, model *classify.Model) []*cluster.Story {
	out := make([]*cluster.Story, 0, min(len(entries), maxItemsPerSitemap))
	for _, e := range entries {
		lang := strings.ToLower(strings.TrimSpace(e.News.Publication.Language))
		if feed.Lang != "*" && lang != "" && !strings.HasPrefix(lang, feed.Lang) {
			continue
		}
		title := cleanText(e.News.Title)
		link := canonicalURL(strings.TrimSpace(e.Loc))
		if title == "" || link == "" || spamRe.MatchString(title) {
			continue
		}
		// Some sitemaps tag every entry "en", even translated ones.
		if feed.Lang == "en" && !looksEnglish(title) {
			continue
		}
		published := parseTime(e.News.PubDate)
		if published.IsZero() {
			published = parseTime(e.LastMod)
		}
		if published.IsZero() || published.After(now.Add(futureTolerance)) {
			published = now
		}
		cat, guessed := model.Categorize(link, title, e.News.Keywords, feed.Category)
		if cat == classify.Skip {
			continue
		}
		out = append(out, &cluster.Story{
			ID:         hashID(link),
			FeedID:     feed.ID,
			Publisher:  feed.Publisher,
			Source:     feed.Name,
			Categories: []string{cat},
			Guessed:    guessed,
			Title:      title,
			Link:       link,
			Published:  published.UTC(),
			Seen:       now,
		})
	}
	if len(out) > maxItemsPerSitemap {
		sortNewestFirst(out)
		out = out[:maxItemsPerSitemap]
	}
	return out
}

// Function words that give away a headline's language.
var (
	englishWords = toWordSet("the a an of to in on for and with as at by from is are was after over says new")
	foreignWords = toWordSet("de da do das dos em com para contra uma um que el la los las del por con se y en al lo une le les des du et est pour sur dans au aux der die das und mit für von zu ist ein eine")
)

func toWordSet(s string) map[string]bool {
	m := make(map[string]bool)
	for _, w := range strings.Fields(s) {
		m[w] = true
	}
	return m
}

// looksEnglish rejects headlines that are clearly in another language.
func looksEnglish(title string) bool {
	en, other := 0, 0
	for _, w := range strings.FieldsFunc(strings.ToLower(title), func(r rune) bool { return r == ' ' || r == ',' || r == ':' || r == '\'' }) {
		if englishWords[w] {
			en++
		}
		if foreignWords[w] {
			other++
		}
	}
	return !(other >= 2 && other > en)
}

func sortNewestFirst(s []*cluster.Story) {
	sort.SliceStable(s, func(i, j int) bool { return s[i].Published.After(s[j].Published) })
}

func hashID(key string) string {
	sum := sha1.Sum([]byte(key))
	return hex.EncodeToString(sum[:10])
}

func cleanText(s string) string {
	s = tagRe.ReplaceAllString(s, " ")
	s = html.UnescapeString(html.UnescapeString(s)) // some feeds double-escape
	return strings.TrimSpace(spaceRe.ReplaceAllString(s, " "))
}

func truncate(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)[:n]
	cut := string(r)
	if i := strings.LastIndexByte(cut, ' '); i > n/2 {
		cut = cut[:i]
	}
	return strings.TrimRight(cut, " ,.;:-") + "…"
}

// canonicalURL strips tracking parameters and fragments so the same article
// carried by two feeds gets the same ID.
func canonicalURL(raw string) string {
	if raw == "" {
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return raw
	}
	u.Scheme = strings.ToLower(u.Scheme)
	u.Host = strings.ToLower(u.Host)
	u.Fragment = ""
	q := u.Query()
	for k := range q {
		lk := strings.ToLower(k)
		if strings.HasPrefix(lk, "utm_") || lk == "fbclid" || lk == "gclid" || lk == "ref" ||
			lk == "cmpid" || lk == "ns_mchannel" || lk == "ns_source" || lk == "at_medium" || lk == "at_campaign" || lk == "traffic_source" {
			q.Del(k)
		}
	}
	u.RawQuery = q.Encode()
	if len(u.Path) > 1 {
		u.Path = strings.TrimRight(u.Path, "/")
	}
	return u.String()
}
