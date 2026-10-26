import { For, Show } from "solid-js";
import { ago, hhmm, host } from "../format";
import { PANELS } from "../panels";
import type { Cluster } from "../types";

interface Props {
  cluster: Cluster | null;
  top: Cluster[];
  now: number;
  onPick: (c: Cluster) => void;
}

const catCode = (cat: string) => PANELS.find((p) => p.cat === cat)?.code ?? cat.toUpperCase();

export default function Detail(props: Props) {
  return (
    <section class="panel detail" aria-label="Story detail">
      <header class="panel-head">
        <span class="panel-num">◆</span>
        <span class="panel-code">{props.cluster ? "DETAIL" : "TOP"}</span>
        <span class="panel-title">{props.cluster ? "Selected story" : "Most-covered stories · last 6h"}</span>
      </header>
      <div class="panel-body detail-body">
        <Show
          when={props.cluster}
          fallback={
            <div class="top-list">
              <ol>
                <For each={props.top}>
                  {(c, i) => (
                    <li classList={{ brk: c.brk }} onClick={() => props.onPick(c)}>
                      <span class="top-rank">{i() + 1}</span>
                      <span class="top-title">{c.title}</span>
                      <span class="top-meta">
                        {c.n} sources · {ago(c.upd, props.now)}
                      </span>
                    </li>
                  )}
                </For>
              </ol>
              <p class="top-hint">
                <kbd>j</kbd>/<kbd>k</kbd> move · <kbd>1</kbd>–<kbd>7</kbd> panels · <kbd>/</kbd> command · <kbd>?</kbd> help
              </p>
            </div>
          }
        >
          {(c) => (
            <>
              <div class="d-meta">
                <Show when={c().brk}>
                  <span class="tag tag-brk">BREAKING</span>
                </Show>
                <span class="tag">{c().n} {c().n === 1 ? "SOURCE" : "SOURCES"}</span>
                <For each={c().cats}>{(cat) => <span class="tag tag-dim">{catCode(cat)}</span>}</For>
                <span class="d-ago">
                  {hhmm(c().time)} · {ago(c().time, props.now)} ago
                </span>
              </div>
              <h2 class="d-title">
                <a href={c().link} target="_blank" rel="noopener noreferrer">
                  {c().title}
                </a>
              </h2>
              <div class="d-src">
                {c().src} · {host(c().link)}
              </div>
              <Show when={c().summary}>
                <p class="d-summary">{c().summary}</p>
              </Show>
              <div class="d-hint">
                <kbd>Enter</kbd> open story
              </div>
              <Show when={(c().items?.length ?? 0) > 1}>
                <h3 class="d-sub">COVERAGE · first report first</h3>
                <ol class="d-items">
                  <For each={c().items ?? []}>
                    {(it) => (
                      <li>
                        <span class="r-time">{hhmm(it.time)}</span>
                        <span class="d-pub">{it.pub}</span>
                        <a href={it.link} target="_blank" rel="noopener noreferrer">
                          {it.title}
                        </a>
                      </li>
                    )}
                  </For>
                </ol>
              </Show>
            </>
          )}
        </Show>
      </div>
    </section>
  );
}
