package api

import (
	"bytes"
	"compress/flate"
	"encoding/binary"
	"encoding/json"
	"hash/crc32"
	"strconv"
	"time"

	"nuevo-rss/internal/cluster"
)

// Snapshots are big (megabytes of JSON) and gzip is most of the cost of
// building one. But between two versions almost every story is unchanged.
// So the JSON is cut into chunks of clusters by 30-minute bucket of first
// report, and each chunk is deflated on its own and cached by its content.
// A rebuild only compresses the chunks that changed, then splices them into
// one ordinary gzip stream: every piece ends on a byte boundary (a deflate
// sync flush) and only refers back to its own bytes, so the concatenation is
// valid deflate. This is how pigz compresses in parallel.

const chunkSpan = 30 * time.Minute

type chunkKey struct {
	crc uint32
	n   int
}

type chunk struct {
	gz   []byte
	used time.Time
}

type chunkCache struct {
	m         map[chunkKey]*chunk
	hits, all int64
}

// deflateFlushed compresses b as a run of deflate blocks ending in a sync
// flush, with no final block, so it can be followed by more blocks.
func deflateFlushed(b []byte, level int) []byte {
	var buf bytes.Buffer
	zw, _ := flate.NewWriter(&buf, level)
	zw.Write(b)
	zw.Flush()
	return buf.Bytes()
}

// encode returns the snapshot JSON and its gzip encoding. clusters must be
// sorted newest first.
func (cc *chunkCache) encode(seq uint64, clusters []cluster.WireCluster) (raw, gz []byte, err error) {
	if cc.m == nil {
		cc.m = make(map[chunkKey]*chunk)
	}
	now := time.Now()

	// Build the JSON, remembering where each bucket's chunk sits in it.
	// Bytes between chunks (the prefix, commas, the suffix) are "glue".
	type span struct {
		start, end int
		glue       bool
	}
	var spans []span
	var out bytes.Buffer
	out.Grow(len(clusters) * 700)
	prev := 0
	mark := func(glue bool) { // close the span that ends here
		if out.Len() > prev {
			spans = append(spans, span{prev, out.Len(), glue})
			prev = out.Len()
		}
	}
	out.WriteString(`{"seq":`)
	out.WriteString(strconv.FormatUint(seq, 10))
	out.WriteString(`,"clusters":[`)
	bucket := int64(-1)
	for i := range clusters {
		b := clusters[i].Time / chunkSpan.Milliseconds()
		if i > 0 {
			if b != bucket {
				mark(false)
			}
			out.WriteByte(',')
			if b != bucket {
				mark(true)
			}
		} else {
			mark(true)
		}
		bucket = b
		j, err := json.Marshal(&clusters[i])
		if err != nil {
			return nil, nil, err
		}
		out.Write(j)
	}
	mark(len(clusters) == 0)
	out.WriteString(`]}`)
	mark(true)
	raw = out.Bytes()

	// gzip header: magic, deflate, no flags, no mtime, no extra flags, OS unknown.
	g := make([]byte, 0, len(raw)/3)
	g = append(g, 0x1f, 0x8b, 8, 0, 0, 0, 0, 0, 0, 0xff)
	for _, s := range spans {
		part := raw[s.start:s.end]
		if s.glue {
			g = append(g, deflateFlushed(part, flate.BestSpeed)...)
			continue
		}
		cc.all++
		k := chunkKey{crc32.ChecksumIEEE(part), len(part)}
		c, ok := cc.m[k]
		if ok {
			cc.hits++
		} else {
			c = &chunk{gz: deflateFlushed(part, flate.DefaultCompression)}
			cc.m[k] = c
		}
		c.used = now
		g = append(g, c.gz...)
	}
	g = append(g, 0x03, 0x00) // an empty final block
	g = binary.LittleEndian.AppendUint32(g, crc32.ChecksumIEEE(raw))
	g = binary.LittleEndian.AppendUint32(g, uint32(len(raw)))

	// Drop chunks no snapshot has used for a while.
	for k, c := range cc.m {
		if now.Sub(c.used) > 2*time.Minute {
			delete(cc.m, k)
		}
	}
	return raw, g, nil
}
