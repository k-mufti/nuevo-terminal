# nuevo-rss

A Bloomberg-terminal-style live news dashboard fed by RSS. See [IDEA.md](IDEA.md) for the plan.

## Run it

Needs Go 1.25+ and Node 22+.

```sh
make run            # builds the web app + server, serves everything on http://localhost:8080
```

For development with hot reload, use two terminals:

```sh
make server         # Go API on :8080
make web            # Vite on http://localhost:5173 (proxies /api to :8080)
```

`make test` runs the Go tests and the TypeScript type check.

## How it works

```
~66 RSS/Atom feeds ─┐   polled 30s–2min (follows each feed's cache headers;
20 news sitemaps ───┤   big publishers' CDN caches skipped), ETag/304 aware,
WebSub pushes ──────┘   or pushed by WebSub hubs (needs -public-url)
        │
        ▼
   fetcher (Go) ── classifier: URL section → keywords → naive Bayes on headlines
        │
        ▼
   SQLite (data/nuevo.db) ◄── restored on startup, 14 days kept
        │
        ▼
   cluster engine (in memory, last 36h)
     · same link from two feeds → one story
     · similar headlines (IDF-weighted word overlap) → one cluster
     · ≥4 outlets within 90 min → BREAKING
        │
        ▼  /api/snapshot (built once per change, shared, gzip; last 6h first)
        ▼  /api/stream   (SSE: versioned changes only)
   browser (SolidJS) ── virtualised panels, ticker, market strip, top stories
        ▲
Yahoo Finance ──┘ every 30s
```

| Path | What |
|---|---|
| `server/feeds.json` | Sources: id, name, publisher, category, url; optional `kind: "sitemap"`, `bust`, `classify`, `lang`, `interval` |
| `server/internal/ingest` | Polling, sitemaps, cache busting, URL cleanup, spam filter |
| `server/internal/classify` | Picks a panel for stories whose source doesn't say |
| `server/internal/cluster` | Story grouping, breaking detection, live events, race tracking |
| `server/internal/websub` | WebSub (PubSubHubbub) subscriptions |
| `server/internal/store` | SQLite persistence |
| `server/internal/markets` | Market strip quotes |
| `server/internal/api` | `/api/stream`, `/api/snapshot`, `/api/feeds`, `/api/health`, `/api/metrics` |
| `web/src` | The terminal UI |

## Measuring it

- `GET /api/metrics`: publish→seen lag by source kind, sitemap-vs-RSS races, who reports first, snapshot cost, memory.
- `GET /api/feeds`: per-source health, poll interval, errors, network vs processing time, and publish→seen lag.
- `cd server && go test ./internal/cluster -bench .`: ingest, snapshot and fan-out benchmarks.
- `NUEVO_DB=data/nuevo.db go test ./internal/classify -run Accuracy -v`: classifier accuracy on stored headlines.

## Keyboard

`1`–`8` panels (`8` = all news, full screen) · `j`/`k` stories · `h`/`l` panels · `Enter` open · `f` full-screen · `b` breaking ·
`/` command bar (`TECH <GO>`, `ALL` for every story, `HOME` for the grid, `BRK`, or words to filter) · `Esc` back · `?` help

## Server flags

`-addr :8080` · `-feeds feeds.json` · `-db data/nuevo.db` · `-web ../web/dist` · `-interval 2m` · `-quotes 30s` · `-public-url https://…` (enables WebSub) · `-debug`
