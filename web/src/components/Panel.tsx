import { createEffect, createMemo, createSignal, Index, on, onCleanup, onMount, Show, untrack } from "solid-js";
import { hhmm, isToday } from "../format";
import { clock, isFresh } from "../store";
import type { Cluster, PanelDef } from "../types";

export const ROW = 24; // px; must match --row in CSS
const NEW_MS = 15 * 60_000;
const OVERSCAN = 8;

interface Props {
  def: PanelDef;
  num: number;
  items: Cluster[];
  active: boolean;
  maximized: boolean;
  filtered: boolean;
  selectedId: string | null;
  onSelect: (id: string) => void;
  onActivate: () => void;
  onOpen: (c: Cluster) => void;
  onMaximize: () => void;
}

export default function Panel(props: Props) {
  let scroller!: HTMLDivElement;
  const [scrollTop, setScrollTop] = createSignal(0);
  const [height, setHeight] = createSignal(400);

  onMount(() => {
    const ro = new ResizeObserver(() => setHeight(scroller.clientHeight));
    ro.observe(scroller);
    onCleanup(() => ro.disconnect());
  });

  const range = createMemo(() => {
    const n = props.items.length;
    const start = Math.max(0, Math.floor(scrollTop() / ROW) - OVERSCAN);
    const end = Math.min(n, Math.ceil((scrollTop() + height()) / ROW) + OVERSCAN);
    return { start, end };
  });
  const visible = createMemo(() => props.items.slice(range().start, range().end));

  // Keep what you're reading in place when new stories arrive above it.
  let anchorId: string | undefined;
  let anchorOffset = 0;
  const rememberAnchor = () => {
    const i = Math.floor(scroller.scrollTop / ROW);
    anchorId = props.items[i]?.id;
    anchorOffset = scroller.scrollTop - i * ROW;
  };
  const onScroll = () => {
    setScrollTop(scroller.scrollTop);
    rememberAnchor();
  };
  createEffect(
    on(
      () => props.items,
      (items) => {
        if (scroller.scrollTop > 0 && anchorId) {
          const idx = items.findIndex((c) => c.id === anchorId);
          if (idx >= 0) {
            const top = idx * ROW + anchorOffset;
            if (Math.abs(top - scroller.scrollTop) >= 1) {
              scroller.scrollTop = top;
              setScrollTop(top);
            }
          }
        }
        rememberAnchor();
      },
      { defer: true },
    ),
  );

  // Scroll the selected row into view when the selection moves.
  createEffect(
    on([() => props.selectedId, () => props.active], ([id, active]) => {
      if (!id || !active) return;
      const idx = untrack(() => props.items.findIndex((c) => c.id === id));
      if (idx < 0) return;
      const top = idx * ROW;
      const h = scroller.clientHeight;
      if (top < scroller.scrollTop) scroller.scrollTop = top;
      else if (top + ROW > scroller.scrollTop + h) scroller.scrollTop = top + ROW - h;
    }),
  );

  const breakingCount = createMemo(() => props.items.reduce((n, c) => n + (c.brk ? 1 : 0), 0));

  return (
    <section
      class="panel"
      classList={{ active: props.active, maximized: props.maximized }}
      onMouseDown={() => props.onActivate()}
      aria-label={props.def.title}
    >
      <header class="panel-head" onDblClick={() => props.onMaximize()}>
        <span class="panel-num">{props.num}</span>
        <span class="panel-code">{props.def.code}</span>
        <span class="panel-title">{props.def.title}</span>
        <Show when={breakingCount() > 0}>
          <span class="panel-brk">{breakingCount()} BRK</span>
        </Show>
        <span class="panel-count" classList={{ filtered: props.filtered }}>
          {props.items.length}
        </span>
      </header>
      <div class="panel-body" ref={scroller} onScroll={onScroll}>
        <Show when={props.items.length > 0} fallback={<div class="empty">{props.filtered ? "No matches" : "Waiting for stories…"}</div>}>
          <div class="spacer" style={{ height: `${props.items.length * ROW}px` }}>
            <div class="rows" style={{ transform: `translateY(${range().start * ROW}px)` }}>
              <Index each={visible()}>
                {(c) => (
                  <div
                    class="row"
                    classList={{
                      sel: c().id === props.selectedId,
                      brk: c().brk,
                      fresh: isFresh(c().id),
                      recent: clock() - c().time < NEW_MS,
                      major: c().n >= 3,
                      single: c().n === 1,
                      old: !isToday(c().time),
                    }}
                    onClick={() => props.onSelect(c().id)}
                    onDblClick={() => props.onOpen(c())}
                    title={c().title}
                  >
                    <span class="r-time">{hhmm(c().time)}</span>
                    <span class="r-title">{c().title}</span>
                    <span class="r-n">{c().n > 1 ? c().n : ""}</span>
                    <span class="r-src">{c().pub}</span>
                  </div>
                )}
              </Index>
            </div>
          </div>
        </Show>
      </div>
    </section>
  );
}
