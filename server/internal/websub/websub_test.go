package websub

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// A fake hub exercises the whole flow: subscribe → verify → signed push → kick.
func TestSubscribeVerifyPush(t *testing.T) {
	var kicked atomic.Int32
	var srv *httptest.Server
	m := New("", func(id string) bool {
		if id == "feed1" {
			kicked.Add(1)
		}
		return true
	}, slog.New(slog.NewTextHandler(io.Discard, nil)))

	mux := http.NewServeMux()
	mux.Handle("/websub/", m.Handler())
	gotSecret := make(chan string, 1)
	mux.HandleFunc("/hub", func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		gotSecret <- r.Form.Get("hub.secret")
		w.WriteHeader(http.StatusAccepted)
		// Hub verifies the callback asynchronously.
		go func() {
			cb := r.Form.Get("hub.callback") + "?" + url.Values{
				"hub.mode": {"subscribe"}, "hub.topic": {r.Form.Get("hub.topic")},
				"hub.challenge": {"abc123"}, "hub.lease_seconds": {"3600"},
			}.Encode()
			resp, err := http.Get(cb)
			if err != nil {
				t.Error(err)
				return
			}
			b, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			if string(b) != "abc123" {
				t.Errorf("challenge echo = %q", b)
			}
		}()
	})
	srv = httptest.NewServer(mux)
	defer srv.Close()
	m.public = srv.URL

	m.Discovered("feed1", srv.URL+"/hub", "https://example.com/feed.xml")
	secret := <-gotSecret
	deadline := time.Now().Add(2 * time.Second)
	for {
		st := m.Statuses()
		if len(st) == 1 && st[0].Active {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("subscription never confirmed")
		}
		time.Sleep(10 * time.Millisecond)
	}

	push := func(sig string) {
		req, _ := http.NewRequest(http.MethodPost, srv.URL+"/websub/feed1", strings.NewReader("<rss/>"))
		req.Header.Set("X-Hub-Signature", sig)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
	}
	push("sha256=deadbeef") // forged: ignored
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte("<rss/>"))
	push("sha256=" + hex.EncodeToString(mac.Sum(nil)))
	if kicked.Load() != 1 {
		t.Fatalf("kicks = %d, want 1 (only the signed push)", kicked.Load())
	}

	// A second poll during an active lease must not resubscribe.
	m.Discovered("feed1", srv.URL+"/hub", "https://example.com/feed.xml")
	select {
	case <-gotSecret:
		t.Fatal("resubscribed during an active lease")
	case <-time.After(100 * time.Millisecond):
	}
}
