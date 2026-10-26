package cluster

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"nuevo-rss/internal/live"
)

// testdata/stories.tsv holds real headlines (publisher, categories, published ms, title).
func loadStories(tb testing.TB) []*Story {
	f, err := os.Open("testdata/stories.tsv")
	if err != nil {
		tb.Skip("no testdata")
	}
	defer f.Close()
	var out []*Story
	sc := bufio.NewScanner(f)
	for i := 0; sc.Scan(); i++ {
		p := strings.SplitN(sc.Text(), "\t", 4)
		if len(p) != 4 {
			continue
		}
		ms, _ := strconv.ParseInt(p[2], 10, 64)
		out = append(out, &Story{ID: strconv.Itoa(i), Publisher: p[0], Source: p[0], Categories: strings.Split(p[1], ","),
			Title: p[3], Published: time.UnixMilli(ms), Seen: time.UnixMilli(ms)})
	}
	return out
}

func freshCopy(in []*Story, salt int) []*Story {
	out := make([]*Story, len(in))
	for i, s := range in {
		cp := *s
		cp.ID = fmt.Sprintf("%s-%d", s.ID, salt)
		out[i] = &cp
	}
	return out
}

func newestTime(ss []*Story) time.Time {
	var t time.Time
	for _, s := range ss {
		if s.Published.After(t) {
			t = s.Published
		}
	}
	return t
}

// Cost to ingest + cluster one story into a warm engine of realistic size.
func BenchmarkIngestPerStory(b *testing.B) {
	base := loadStories(b)
	now := newestTime(base)
	e := NewEngine(live.NewHub())
	e.now = func() time.Time { return now }
	e.Ingest(base, nil)
	extra := freshCopy(base, 1)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		s := extra[i%len(extra)]
		cp := *s
		cp.ID = fmt.Sprintf("x%d", i)
		e.Ingest([]*Story{&cp}, nil)
		if i%len(extra) == len(extra)-1 { // keep the engine from growing without bound
			b.StopTimer()
			e = NewEngine(live.NewHub())
			e.now = func() time.Time { return now }
			e.Ingest(base, nil)
			b.StartTimer()
		}
	}
}

// Full snapshot for one connecting browser: build + JSON encode.
func BenchmarkSnapshot(b *testing.B) {
	base := loadStories(b)
	now := newestTime(base)
	e := NewEngine(live.NewHub())
	e.now = func() time.Time { return now }
	e.Ingest(base, nil)
	var size int
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		cs, _ := e.Snapshot(time.Time{}, time.Time{})
		data, _ := json.Marshal(cs)
		size = len(data)
	}
	b.ReportMetric(float64(size)/1024, "KB")
}

// Ingest → event on a subscriber's channel.
func BenchmarkPushLatency(b *testing.B) {
	base := loadStories(b)
	now := newestTime(base)
	e := NewEngine(live.NewHub())
	e.now = func() time.Time { return now }
	e.Ingest(base, nil)
	var recent []*Story
	for _, s := range base {
		if now.Sub(s.Published) < time.Hour {
			recent = append(recent, s)
		}
	}
	_, sub := e.Subscribe()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		s := *recent[i%len(recent)]
		s.ID = fmt.Sprintf("p%d", i)
		e.Ingest([]*Story{&s}, nil)
		<-sub.C
	}
}

func BenchmarkFanout1000(b *testing.B) {
	h := live.NewHub()
	subs := make([]*live.Sub, 1000)
	for i := range subs {
		subs[i] = h.Subscribe()
	}
	ev := live.Event{Type: "clusters", Data: make([]byte, 2048)}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		h.Publish(ev)
		for _, s := range subs {
			<-s.C
		}
	}
}
