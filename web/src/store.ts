// Live state: one EventSource to the server, a Map of clusters, and one
// sorted list per panel. Bursts of updates are coalesced into a single
// re-sort per animation frame so the UI never does more work than it can show.
import { batch, createSignal, type Accessor, type Setter } from "solid-js";
import { PANELS } from "./panels";
import type { Cluster, Quote, Status } from "./types";

export type Conn = "connecting" | "live" | "down";

const clusters = new Map<string, Cluster>();
const freshUntil = new Map<string, number>();
const FRESH_MS = 6000;
const TICKER_SIZE = 40;

const lists = new Map<string, [Accessor<Cluster[]>, Setter<Cluster[]>]>();
for (const p of PANELS) lists.set(p.cat, createSignal<Cluster[]>([]));

const TOP_SIZE = 14;
const TOP_WINDOW = 6 * 3600_000;

const [breaking, setBreaking] = createSignal<Cluster[]>([]);
const [top, setTop] = createSignal<Cluster[]>([]);
// Ticks every 30s so "new" highlighting fades without waiting for an update.
const [clock, setClock] = createSignal(Date.now());
setInterval(() => setClock(Date.now()), 30_000);
const [latest, setLatest] = createSignal<Cluster[]>([]);
const [markets, setMarkets] = createSignal<Quote[]>([]);
const [status, setStatus] = createSignal<Status | null>(null);
const [conn, setConn] = createSignal<Conn>("connecting");
const [lastEvent, setLastEvent] = createSignal(0);
const [loaded, setLoaded] = createSignal(false);

export { breaking, top, clock, latest, markets, status, conn, lastEvent, loaded };

export const panelList = (cat: string): Accessor<Cluster[]> => lists.get(cat)![0];
export const isFresh = (id: string) => (freshUntil.get(id) ?? 0) > Date.now();

const byTimeDesc = (a: Cluster, b: Cluster) => b.time - a.time || (a.id < b.id ? -1 : 1);

let dirtyCats = new Set<string>();
let dirtyAll = false;
let scheduled = false;

function schedule() {
  if (scheduled) return;
  scheduled = true;
  // rAF doesn't run in background tabs; a timer keeps state current there.
  if (document.hidden) setTimeout(flush, 250);
  else requestAnimationFrame(flush);
}

function flush() {
  scheduled = false;
  const cats = dirtyAll ? [...lists.keys()] : [...dirtyCats].filter((c) => lists.has(c));
  dirtyCats = new Set();
  dirtyAll = false;

  const all = [...clusters.values()];
  batch(() => {
    for (const cat of cats) {
      lists.get(cat)![1](all.filter((c) => c.cats.includes(cat)).sort(byTimeDesc));
    }
    setBreaking(all.filter((c) => c.brk).sort((a, b) => (b.brkAt ?? 0) - (a.brkAt ?? 0)));
    const since = Date.now() - TOP_WINDOW;
    setTop(
      all
        .filter((c) => c.n >= 2 && c.upd >= since)
        .sort((a, b) => b.n - a.n || b.upd - a.upd)
        .slice(0, TOP_SIZE),
    );
    // Ticker: most recent stories, multi-source ones first so it reads like a wire.
    const recent = all.sort(byTimeDesc).slice(0, 150);
    setLatest([...recent.filter((c) => c.n > 1), ...recent.filter((c) => c.n === 1)].slice(0, TICKER_SIZE));
  });
}

function upsert(c: Cluster, markFresh: boolean) {
  const prev = clusters.get(c.id);
  if (prev) for (const cat of prev.cats) dirtyCats.add(cat);
  else if (markFresh) freshUntil.set(c.id, Date.now() + FRESH_MS);
  for (const cat of c.cats) dirtyCats.add(cat);
  clusters.set(c.id, c);
}

function remove(id: string) {
  const prev = clusters.get(id);
  if (!prev) return;
  for (const cat of prev.cats) dirtyCats.add(cat);
  clusters.delete(id);
  freshUntil.delete(id);
}

let es: EventSource | null = null;
let retryTimer: number | undefined;
let generation = 0; // bumps on every (re)connect so stale async work is ignored

// Story events carry a version (the SSE id). Until the snapshot is in, they are
// queued; afterwards anything not newer than the snapshot is skipped.
let snapSeq = -1;
let queue: { seq: number; apply: () => void }[] | null = null;

function applyVersioned(seq: number, apply: () => void) {
  if (queue) queue.push({ seq, apply });
  else if (seq > snapSeq) {
    apply();
    snapSeq = seq;
    schedule();
  }
}

// The snapshot comes in two parts: the last few hours first so headlines
// appear immediately, then everything older.
const RECENT_MS = 6 * 3600_000;
type Part = { seq: number; clusters: Cluster[] };

// Clusters changed or removed by live events since the snapshot started
// loading; a snapshot part must not undo those.
let touched = new Set<string>();
let removed = new Set<string>();

async function fetchPart(query: string): Promise<Part> {
  const res = await fetch(`/api/snapshot?${query}`, { cache: "no-store" });
  if (!res.ok) throw new Error(`snapshot ${res.status}`);
  return res.json();
}

// Replace the clusters on one side of the boundary with a snapshot part.
function applyPart(part: Part, recent: boolean, boundary: number, markFresh: boolean) {
  const keep = new Set(part.clusters.map((c) => c.id));
  for (const [id, c] of clusters) {
    if (recent === c.time >= boundary && !keep.has(id) && !touched.has(id)) remove(id);
  }
  for (const c of part.clusters) {
    if (touched.has(c.id) || removed.has(c.id)) continue;
    upsert(c, markFresh && !clusters.has(c.id));
  }
}

// Started at the same time as the event stream (not after it) to save a round
// trip. If the stream subscribed at a newer version than this snapshot, the
// snapshot could miss a change, so it is fetched again.
let early: { boundary: number; part: Promise<Part> } | null = null;

function recentBoundary() {
  return Math.floor((Date.now() - RECENT_MS) / 60000) * 60000;
}

function prefetchRecent() {
  const boundary = recentBoundary();
  const part = fetchPart(`from=${boundary}`);
  part.catch(() => {}); // handled when awaited
  early = { boundary, part };
}

async function loadSnapshot(gen: number, helloSeq: number, attempt = 0): Promise<void> {
  let boundary = recentBoundary();
  try {
    let recent: Part;
    if (early && attempt === 0) {
      boundary = early.boundary;
      recent = await early.part;
      early = null;
      if (recent.seq < helloSeq) recent = await fetchPart(`from=${boundary}`);
    } else {
      recent = await fetchPart(`from=${boundary}`);
    }
    if (gen !== generation) return;
    const firstLoad = !loaded();
    touched = new Set();
    removed = new Set();
    // On reconnect, anything we hadn't seen yet still counts as new.
    applyPart(recent, true, boundary, !firstLoad);
    snapSeq = recent.seq;
    const pending = queue ?? [];
    queue = null;
    for (const p of pending) if (p.seq > snapSeq) (p.apply(), (snapSeq = p.seq));
    dirtyAll = true;
    setLoaded(true);
    schedule();
    loadOlder(gen, boundary);
  } catch {
    early = null;
    if (gen !== generation) return;
    setTimeout(() => loadSnapshot(gen, helloSeq, attempt + 1), Math.min(500 * 2 ** attempt, 8000));
  }
}

async function loadOlder(gen: number, boundary: number, attempt = 0): Promise<void> {
  try {
    const older = await fetchPart(`to=${boundary}`);
    if (gen !== generation) return;
    applyPart(older, false, boundary, false);
    dirtyAll = true;
    schedule();
  } catch {
    if (gen !== generation) return;
    setTimeout(() => loadOlder(gen, boundary, attempt + 1), Math.min(500 * 2 ** attempt, 8000));
  }
}

export function connect() {
  clearTimeout(retryTimer);
  es?.close();
  setConn("connecting");
  const gen = ++generation;
  if (!loaded()) prefetchRecent();
  const source = new EventSource("/api/stream");
  es = source;

  source.onerror = () => {
    if (gen !== generation) return;
    if (source.readyState === EventSource.CLOSED) {
      // The browser gave up (e.g. server returned an error); retry ourselves.
      setConn("down");
      retryTimer = window.setTimeout(connect, 3000);
    } else {
      setConn("connecting"); // browser is retrying on its own
    }
  };

  const on = (type: string, fn: (data: any, seq: number) => void) =>
    source.addEventListener(type, (e) => {
      const m = e as MessageEvent;
      setLastEvent(Date.now());
      fn(JSON.parse(m.data), Number(m.lastEventId || 0));
    });

  // Sent first on every (re)connection. The snapshot is fetched separately so
  // the server can encode it once and share it with every client.
  on("hello", (h: { seq: number; markets: Quote[]; status: Status }) => {
    queue = [];
    batch(() => {
      setMarkets(h.markets);
      setStatus(h.status);
      setConn("live");
    });
    loadSnapshot(gen, h.seq);
  });
  on("clusters", (cs: Cluster[], seq) =>
    applyVersioned(seq, () => {
      for (const c of cs) {
        touched.add(c.id);
        upsert(c, true);
      }
    }),
  );
  on("remove", (ids: string[], seq) =>
    applyVersioned(seq, () => {
      for (const id of ids) {
        removed.add(id);
        remove(id);
      }
    }),
  );
  on("markets", (q: Quote[]) => setMarkets(q));
  on("status", (st: Status) => setStatus(st));
}

// Drop stale "fresh" marks so the map doesn't grow forever.
setInterval(() => {
  const now = Date.now();
  for (const [id, t] of freshUntil) if (t < now) freshUntil.delete(id);
}, 30_000);
