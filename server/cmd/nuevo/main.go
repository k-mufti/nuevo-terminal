// Command nuevo runs the nuevo-rss server: feed fetcher, clustering, market
// data and the live web terminal.
package main

import (
	"context"
	"errors"
	"flag"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"nuevo-rss/internal/api"
	"nuevo-rss/internal/cluster"
	"nuevo-rss/internal/config"
	"nuevo-rss/internal/ingest"
	"nuevo-rss/internal/live"
	"nuevo-rss/internal/markets"
	"nuevo-rss/internal/store"
	"nuevo-rss/internal/websub"
)

func main() {
	addr := flag.String("addr", ":8080", "listen address")
	feedsPath := flag.String("feeds", "feeds.json", "feed list")
	dbPath := flag.String("db", "data/nuevo.db", "SQLite database path")
	webDir := flag.String("web", "../web/dist", "built frontend to serve (empty = API only)")
	interval := flag.Duration("interval", 2*time.Minute, "default feed poll interval")
	quoteEvery := flag.Duration("quotes", 30*time.Second, "market data poll interval")
	debug := flag.Bool("debug", false, "verbose logging")
	publicURL := flag.String("public-url", "", "public base URL of this server; enables WebSub push (e.g. https://news.example.com)")
	flag.Parse()

	level := slog.LevelInfo
	if *debug {
		level = slog.LevelDebug
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level}))
	if err := run(log, *addr, *feedsPath, *dbPath, *webDir, *publicURL, *interval, *quoteEvery); err != nil {
		log.Error("fatal", "err", err)
		os.Exit(1)
	}
}

func run(log *slog.Logger, addr, feedsPath, dbPath, webDir, publicURL string, interval, quoteEvery time.Duration) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	feeds, err := config.LoadFeeds(feedsPath)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(dbPath), 0o755); err != nil {
		return err
	}
	st, err := store.Open(dbPath)
	if err != nil {
		return err
	}
	defer st.Close()

	hub := live.NewHub()
	eng := cluster.NewEngine(hub)

	t0 := time.Now()
	saved, err := st.LoadSince(ctx, time.Now().Add(-cluster.Window))
	if err != nil {
		return err
	}
	eng.Restore(saved)
	stories, clusters := eng.Stats()
	log.Info("restored", "stories", stories, "clusters", clusters, "took", time.Since(t0).Round(time.Millisecond))

	states, err := st.FeedStates(ctx)
	if err != nil {
		return err
	}
	fetcher := ingest.New(feeds, eng, st, states, interval, log)

	// Teach the category model (used for sitemaps and general feeds) from
	// the last two weeks of headlines.
	history, err := st.LoadSince(ctx, time.Now().Add(-14*24*time.Hour))
	if err != nil {
		return err
	}
	log.Info("classifier trained", "examples", fetcher.Train(history))

	mk := markets.New(hub, markets.DefaultSymbols, quoteEvery, log)
	srv := &api.Server{Engine: eng, Fetcher: fetcher, Markets: mk, Hub: hub, WebDir: webDir, Log: log}

	handler := srv.Handler()
	if publicURL != "" {
		ws := websub.New(publicURL, fetcher.Kick, log)
		fetcher.SetHubWatcher(ws)
		srv.WebSub = ws
		mux := http.NewServeMux()
		mux.Handle("/websub/", ws.Handler())
		mux.Handle("/", handler)
		handler = mux
		log.Info("websub enabled", "callback", publicURL+"/websub/{feed}")
	}

	go fetcher.Run(ctx)
	go mk.Run(ctx)
	go srv.RunStatus(ctx, 20*time.Second)
	go maintenance(ctx, eng, st, log)

	httpSrv := &http.Server{
		Addr:              addr,
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
		// Request contexts end on shutdown, which closes open event streams.
		BaseContext: func(net.Listener) context.Context { return ctx },
	}
	errc := make(chan error, 1)
	go func() {
		log.Info("listening", "addr", addr, "feeds", len(feeds))
		errc <- httpSrv.ListenAndServe()
	}()

	select {
	case err := <-errc:
		if !errors.Is(err, http.ErrServerClosed) {
			return err
		}
	case <-ctx.Done():
	}
	log.Info("shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return httpSrv.Shutdown(shutdownCtx)
}

func maintenance(ctx context.Context, eng *cluster.Engine, st *store.Store, log *slog.Logger) {
	sweep := time.NewTicker(time.Minute)
	prune := time.NewTicker(6 * time.Hour)
	defer sweep.Stop()
	defer prune.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-sweep.C:
			eng.Sweep()
		case <-prune.C:
			n, err := st.Prune(ctx, time.Now().Add(-14*24*time.Hour))
			if err != nil {
				log.Warn("prune", "err", err)
			} else if n > 0 {
				log.Info("pruned old stories", "count", n)
			}
		}
	}
}
