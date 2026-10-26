package cluster

import "sort"

// WireItem and WireCluster are the JSON shapes sent to the browser.
// Times are Unix milliseconds.
type WireItem struct {
	Src   string `json:"src"`
	Pub   string `json:"pub"`
	Title string `json:"title"`
	Link  string `json:"link"`
	Time  int64  `json:"time"`
}

type WireCluster struct {
	ID      string     `json:"id"`
	Title   string     `json:"title"`
	Link    string     `json:"link"`
	Src     string     `json:"src"`
	Pub     string     `json:"pub"`
	Summary string     `json:"summary,omitempty"`
	Time    int64      `json:"time"` // first report
	Upd     int64      `json:"upd"`  // latest report
	Cats    []string   `json:"cats"`
	N       int        `json:"n"` // distinct publishers
	Brk     bool       `json:"brk"`
	BrkAt   int64      `json:"brkAt,omitempty"`
	Items   []WireItem `json:"items,omitempty"` // only when more than one story
}

func (c *Cluster) wire() WireCluster {
	lead := c.members[0]
	w := WireCluster{
		ID:    c.ID,
		Title: lead.Title,
		Link:  lead.Link,
		Src:   lead.Source,
		Pub:   lead.Publisher,
		Time:  lead.Published.UnixMilli(),
		Upd:   c.newest().UnixMilli(),
		Brk:   c.breaking,
	}
	if c.breaking {
		w.BrkAt = c.breakingAt.UnixMilli()
	}
	pubs := make(map[string]struct{})
	for _, s := range c.members {
		pubs[s.Publisher] = struct{}{}
		if w.Summary == "" {
			w.Summary = s.Summary
		}
	}
	w.N = len(pubs)
	w.Cats = c.categories()
	if len(c.members) == 1 {
		return w // the lead fields already describe it
	}
	n := min(len(c.members), maxWireItems)
	w.Items = make([]WireItem, n)
	for i, s := range c.members[:n] {
		w.Items[i] = WireItem{Src: s.Source, Pub: s.Publisher, Title: s.Title, Link: s.Link, Time: s.Published.UnixMilli()}
	}
	return w
}

// categories decides which panels a cluster shows in. A category counts if
// at least a third of the cluster's stories came from it, or if it is the
// most common one. This keeps a football story out of the Middle East panel
// just because one Middle East outlet also covered it.
func (c *Cluster) categories() []string {
	counts := make(map[string]int)
	total, top := 0, 0
	for _, s := range c.members {
		for _, cat := range s.Categories {
			counts[cat]++
			total++
			top = max(top, counts[cat])
		}
	}
	out := make([]string, 0, len(counts))
	for cat, n := range counts {
		if n == top || float64(n) >= float64(total)/3 {
			out = append(out, cat)
		}
	}
	sort.Strings(out)
	return out
}
