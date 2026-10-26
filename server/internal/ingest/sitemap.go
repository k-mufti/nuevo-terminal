package ingest

import (
	"bytes"
	"encoding/xml"
	"errors"
	"io"
	"sort"
	"strings"
	"time"
)

// Google News sitemaps list a publisher's articles from the last ~48 hours,
// with exact publication times. Many publishers update them the moment an
// article goes live (so Google finds it), which often makes them fresher than
// the same publisher's RSS feed.

type sitemapEntry struct {
	Loc     string `xml:"loc"`
	LastMod string `xml:"lastmod"`
	News    struct {
		Title       string `xml:"title"`
		PubDate     string `xml:"publication_date"`
		Keywords    string `xml:"keywords"`
		Publication struct {
			Language string `xml:"language"`
		} `xml:"publication"`
	} `xml:"news"`
}

type urlSet struct {
	URLs []sitemapEntry `xml:"url"`
}

type sitemapIndex struct {
	Sitemaps []struct {
		Loc     string `xml:"loc"`
		LastMod string `xml:"lastmod"`
	} `xml:"sitemap"`
}

var errNotSitemap = errors.New("not a sitemap")

// parseSitemap returns the entries of a urlset, or for a sitemap index the
// child sitemap URLs, most recently modified first.
func parseSitemap(body []byte) (entries []sitemapEntry, children []string, err error) {
	dec := xml.NewDecoder(bytes.NewReader(body))
	dec.Strict = false
	var root string
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			return nil, nil, errNotSitemap
		}
		if err != nil {
			return nil, nil, err
		}
		if se, ok := tok.(xml.StartElement); ok {
			root = se.Name.Local
			break
		}
	}
	switch root {
	case "urlset":
		var us urlSet
		if err := xml.Unmarshal(body, &us); err != nil {
			return nil, nil, err
		}
		return us.URLs, nil, nil
	case "sitemapindex":
		var si sitemapIndex
		if err := xml.Unmarshal(body, &si); err != nil {
			return nil, nil, err
		}
		sort.SliceStable(si.Sitemaps, func(i, j int) bool {
			return parseTime(si.Sitemaps[i].LastMod).After(parseTime(si.Sitemaps[j].LastMod))
		})
		for _, s := range si.Sitemaps {
			children = append(children, strings.TrimSpace(s.Loc))
		}
		return nil, children, nil
	}
	return nil, nil, errNotSitemap
}

var timeLayouts = []string{
	time.RFC3339Nano,
	"2006-01-02T15:04:05Z0700",
	"2006-01-02T15:04Z07:00",
	"2006-01-02T15:04:05",
	"2006-01-02 15:04:05",
	"2006-01-02",
}

func parseTime(s string) time.Time {
	s = strings.TrimSpace(s)
	for _, l := range timeLayouts {
		if t, err := time.Parse(l, s); err == nil {
			return t
		}
	}
	return time.Time{}
}
