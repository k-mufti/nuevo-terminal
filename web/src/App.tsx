import { createEffect, createMemo, createSignal, For, onCleanup, onMount, Show } from "solid-js";
import Detail from "./components/Detail";
import Help from "./components/Help";
import MarketStrip from "./components/MarketStrip";
import Panel from "./components/Panel";
import Ticker from "./components/Ticker";
import { ago } from "./format";
import { findPanel, PANELS } from "./panels";
import { breaking, conn, connect, latest, loaded, markets, panelList, status, top } from "./store";
import type { Cluster } from "./types";

// Lowercased text searched by the filter, cached per cluster object.
const haystacks = new WeakMap<Cluster, string>();
function haystack(c: Cluster): string {
  let h = haystacks.get(c);
  if (h === undefined) {
    h = [c.title, c.pub, c.src, c.summary ?? "", ...(c.items ?? []).map((i) => `${i.title} ${i.pub}`)].join(" ").toLowerCase();
    haystacks.set(c, h);
  }
  return h;
}

const COMMANDS = ["ALL", "HOME", "MAIN", "HELP", "BRK", "BREAKING", "CLR", "CLEAR"];
const stripGo = (s: string) => s.trim().replace(/\s*<GO>\s*$/i, "").trim();
const asCommand = (s: string): string | null => {
  const t = stripGo(s).replace(/\s+GO$/i, "").toUpperCase();
  if (!t) return null;
  if (findPanel(t) >= 0 || COMMANDS.includes(t)) return t;
  return null;
};

export default function App() {
  const [active, setActive] = createSignal(0);
  const [maxed, setMaxed] = createSignal<number | null>(null);
  const [selected, setSelected] = createSignal<Record<string, string>>({});
  const [cmd, setCmd] = createSignal("");
  const [query, setQuery] = createSignal("");
  const [help, setHelp] = createSignal(false);
  const [message, setMessage] = createSignal("");
  const [now, setNow] = createSignal(Date.now());
  let input!: HTMLInputElement;
  let messageTimer: number | undefined;

  const terms = createMemo(() => query().toLowerCase().split(/\s+/).filter(Boolean));
  const views = PANELS.map((p) => {
    const base = panelList(p.cat);
    return createMemo(() => {
      const t = terms();
      const items = base();
      return t.length ? items.filter((c) => t.every((w) => haystack(c).includes(w))) : items;
    });
  });

  const selId = (i: number) => selected()[PANELS[i].code] ?? null;
  const current = createMemo(() => {
    const id = selId(active());
    return id ? (views[active()]().find((c) => c.id === id) ?? null) : null;
  });

  const flash = (msg: string) => {
    setMessage(msg);
    clearTimeout(messageTimer);
    messageTimer = window.setTimeout(() => setMessage(""), 2500);
  };

  const activate = (i: number) => {
    setActive(i);
    if (maxed() !== null) setMaxed(i);
  };

  const select = (i: number, id: string) => setSelected((s) => ({ ...s, [PANELS[i].code]: id }));

  const move = (delta: number) => {
    const i = active();
    const list = views[i]();
    if (!list.length) return;
    const idx = list.findIndex((c) => c.id === selId(i));
    const next = idx < 0 ? (delta > 0 ? 0 : list.length - 1) : Math.max(0, Math.min(list.length - 1, idx + delta));
    select(i, list[next].id);
  };

  const moveTo = (pos: "first" | "last") => {
    const list = views[active()]();
    if (list.length) select(active(), list[pos === "first" ? 0 : list.length - 1].id);
  };

  const open = (c: Cluster | null) => {
    if (c?.link) window.open(c.link, "_blank", "noopener,noreferrer");
  };

  const clearFilter = () => {
    setQuery("");
    setCmd("");
  };

  // Show a cluster: prefer the current panel, otherwise the first panel it belongs to.
  const reveal = (c: Cluster) => {
    let i = active();
    if (!c.cats.includes(PANELS[i].cat)) i = PANELS.findIndex((p) => c.cats.includes(p.cat));
    if (i < 0) return;
    if (!views[i]().some((x) => x.id === c.id)) clearFilter();
    activate(i);
    select(i, c.id);
  };

  const jumpBreaking = () => {
    const b = breaking()[0];
    if (b) reveal(b);
    else flash("No breaking stories right now");
  };

  const runCommand = (raw: string) => {
    const command = asCommand(raw);
    if (command) {
      clearFilter();
      const p = findPanel(command);
      if (p >= 0) {
        setActive(p);
        setMaxed(p);
        flash(`${PANELS[p].code} · ${PANELS[p].title}`);
      } else if (["ALL", "HOME", "MAIN"].includes(command)) {
        setMaxed(null);
      } else if (command === "HELP") {
        setHelp(true);
      } else if (command === "BRK" || command === "BREAKING") {
        jumpBreaking();
      }
    } else {
      setQuery(stripGo(raw));
    }
    input.blur();
  };

  const focusCommand = () => {
    input.focus();
    input.select();
  };

  const onKey = (e: KeyboardEvent) => {
    if ((e.metaKey || e.ctrlKey) && e.key.toLowerCase() === "k") {
      e.preventDefault();
      focusCommand();
      return;
    }
    const target = e.target as HTMLElement | null;
    if (target && (target.tagName === "INPUT" || target.tagName === "TEXTAREA" || target.isContentEditable)) return;
    if (e.metaKey || e.ctrlKey || e.altKey) return;

    if (help()) {
      if (e.key === "Escape" || e.key === "?") setHelp(false);
      e.preventDefault();
      return;
    }

    let handled = true;
    switch (e.key) {
      case "/":
      case ":":
        focusCommand();
        break;
      case "j":
      case "ArrowDown":
        move(1);
        break;
      case "k":
      case "ArrowUp":
        move(-1);
        break;
      case "PageDown":
        move(20);
        break;
      case "PageUp":
        move(-20);
        break;
      case "g":
      case "Home":
        moveTo("first");
        break;
      case "G":
      case "End":
        moveTo("last");
        break;
      case "h":
      case "ArrowLeft":
        activate((active() + PANELS.length - 1) % PANELS.length);
        break;
      case "l":
      case "ArrowRight":
        activate((active() + 1) % PANELS.length);
        break;
      case "Enter":
      case "o":
        open(current());
        break;
      case "f":
      case "z":
        setMaxed(maxed() === null ? active() : null);
        break;
      case "b":
        jumpBreaking();
        break;
      case "?":
        setHelp(true);
        break;
      case "Escape":
        if (query()) clearFilter();
        else if (maxed() !== null) setMaxed(null);
        else setSelected((s) => ({ ...s, [PANELS[active()].code]: "" }));
        break;
      default:
        if (/^[1-9]$/.test(e.key) && Number(e.key) <= PANELS.length) activate(Number(e.key) - 1);
        else handled = false;
    }
    if (handled) e.preventDefault();
  };

  onMount(() => {
    connect();
    window.addEventListener("keydown", onKey);
    const clock = setInterval(() => setNow(Date.now()), 1000);
    onCleanup(() => {
      window.removeEventListener("keydown", onKey);
      clearInterval(clock);
    });
  });

  createEffect(() => {
    const n = breaking().length;
    document.title = `${n ? `(${n}) ` : ""}NUEVO Terminal`;
  });

  const clock = createMemo(() => {
    const d = new Date(now());
    const t = (h: number, m: number, s: number) => [h, m, s].map((v) => String(v).padStart(2, "0")).join(":");
    return { local: t(d.getHours(), d.getMinutes(), d.getSeconds()), utc: t(d.getUTCHours(), d.getUTCMinutes(), d.getUTCSeconds()) };
  });

  const pendingCommand = createMemo(() => asCommand(cmd()));

  return (
    <div class="app">
      <header class="top">
        <div class="brand">
          <span class="brand-mark">NUEVO</span>
          <span class="brand-sub">TERMINAL</span>
        </div>
        <form
          class="cmd"
          onSubmit={(e) => {
            e.preventDefault();
            runCommand(cmd());
          }}
        >
          <span class="cmd-prompt">&gt;</span>
          <input
            ref={input}
            value={cmd()}
            placeholder="TECH <GO>  ·  type to filter  ·  ? for help"
            spellcheck={false}
            autocomplete="off"
            aria-label="Command bar"
            onInput={(e) => {
              const v = e.currentTarget.value;
              setCmd(v);
              setQuery(asCommand(v) ? "" : stripGo(v));
            }}
            onKeyDown={(e) => {
              if (e.key === "Escape") {
                clearFilter();
                input.blur();
              }
            }}
          />
          <Show when={pendingCommand()}>
            <span class="cmd-hint">↵ GO</span>
          </Show>
          <Show when={!pendingCommand() && query()}>
            <span class="cmd-hint">FILTER · Esc clears</span>
          </Show>
        </form>
        <Show when={message()}>
          <div class="msg">{message()}</div>
        </Show>
        <div class="stat">
          <span class={`dot dot-${conn()}`} />
          <span>{conn() === "live" ? "LIVE" : conn() === "connecting" ? "CONNECTING" : "OFFLINE"}</span>
          <Show when={status()}>
            {(s) => (
              <span class="dim" title={`${s().stories} stories · ${s().clusters} clusters`}>
                {s().feedsOk}/{s().feedsTotal} FEEDS
              </span>
            )}
          </Show>
        </div>
        <div class="clock">
          <span>{clock().local}</span>
          <span class="dim">UTC {clock().utc}</span>
        </div>
      </header>

      <Ticker items={latest()} onPick={reveal} />
      <MarketStrip quotes={markets()} />

      <Show when={breaking()[0]}>
        {(b) => (
          <button class="breaking" onClick={jumpBreaking}>
            <span class="breaking-tag">BREAKING</span>
            <span class="breaking-title">{b().title}</span>
            <span class="breaking-meta">
              {b().n} sources · {ago(b().upd, now())} ago
              {breaking().length > 1 ? ` · +${breaking().length - 1} more` : ""} · <kbd>b</kbd>
            </span>
          </button>
        )}
      </Show>

      <main class="grid" classList={{ "grid-max": maxed() !== null }}>
        <Show when={!loaded()}>
          <div class="loading">CONNECTING TO FEED SERVER…</div>
        </Show>
        <For each={PANELS}>
          {(p, i) => (
            <div class="cell" classList={{ hidden: maxed() !== null && maxed() !== i() }}>
              <Panel
                def={p}
                num={i() + 1}
                items={views[i()]()}
                active={active() === i()}
                maximized={maxed() === i()}
                filtered={terms().length > 0}
                selectedId={selId(i())}
                onSelect={(id) => {
                  setActive(i());
                  select(i(), id);
                }}
                onActivate={() => setActive(i())}
                onOpen={open}
                onMaximize={() => setMaxed(maxed() === i() ? null : i())}
              />
            </div>
          )}
        </For>
        <div class="cell cell-detail">
          <Detail cluster={current()} top={top()} now={now()} onPick={reveal} />
        </div>
      </main>

      <Show when={help()}>
        <Help onClose={() => setHelp(false)} />
      </Show>
    </div>
  );
}
