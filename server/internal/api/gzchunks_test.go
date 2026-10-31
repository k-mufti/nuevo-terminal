package api

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"testing"
	"time"

	"nuevo-rss/internal/cluster"
)

func fakeClusters(n int, base time.Time) []cluster.WireCluster {
	out := make([]cluster.WireCluster, n)
	for i := range out {
		t := base.Add(-time.Duration(i) * 3 * time.Minute)
		out[i] = cluster.WireCluster{
			ID: fmt.Sprintf("c%d", i), Title: fmt.Sprintf("Headline number %d about things", i),
			Link: fmt.Sprintf("https://example.com/news/%d", i), Src: "Ex", Pub: "Ex",
			Time: t.UnixMilli(), Upd: t.UnixMilli(), Cats: []string{"world"}, N: 1,
		}
	}
	return out
}

func checkGzip(t *testing.T, seq uint64, cs []cluster.WireCluster, raw, gz []byte) {
	t.Helper()
	want, _ := json.Marshal(snapshotBody{Seq: seq, Clusters: cs})
	if !bytes.Equal(raw, want) {
		t.Fatalf("raw JSON differs from json.Marshal:\n%s\n%s", raw, want)
	}
	zr, err := gzip.NewReader(bytes.NewReader(gz))
	if err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(zr) // also checks the CRC and length
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, raw) {
		t.Fatal("gzip does not decode to the raw JSON")
	}
}

func TestChunkedGzip(t *testing.T) {
	var cc chunkCache
	base := time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)

	for _, n := range []int{0, 1, 5, 300} {
		cs := fakeClusters(n, base)
		raw, gz, err := cc.encode(7, cs)
		if err != nil {
			t.Fatal(err)
		}
		checkGzip(t, 7, cs, raw, gz)
	}

	// Change one cluster: only its chunk should be recompressed.
	cs := fakeClusters(300, base)
	cc = chunkCache{}
	cc.encode(1, cs)
	cs[150].Title = "Changed"
	cc.hits, cc.all = 0, 0
	raw, gz, _ := cc.encode(2, cs)
	checkGzip(t, 2, cs, raw, gz)
	if miss := cc.all - cc.hits; miss != 1 {
		t.Fatalf("recompressed %d chunks, want 1 (of %d)", miss, cc.all)
	}
}
