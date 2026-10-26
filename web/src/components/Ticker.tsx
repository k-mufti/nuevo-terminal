import { createEffect, createSignal, For, on, untrack } from "solid-js";
import type { Cluster } from "../types";

const SPEED = 70; // px per second

interface Props {
  items: Cluster[];
  onPick: (c: Cluster) => void;
}

// A scrolling headline crawl. New headlines are swapped in only when the loop
// restarts, so text never jumps mid-scroll.
export default function Ticker(props: Props) {
  let track!: HTMLDivElement;
  const [shown, setShown] = createSignal<Cluster[]>([]);
  const [duration, setDuration] = createSignal(120);

  createEffect(() => {
    if (shown().length === 0 && props.items.length > 0) setShown(props.items);
  });
  createEffect(
    on(shown, () => {
      requestAnimationFrame(() => {
        const half = track.scrollWidth / 2;
        if (half > 0) setDuration(half / SPEED);
      });
    }),
  );

  return (
    <div class="ticker" aria-label="Latest headlines">
      <div
        class="ticker-track"
        ref={track}
        style={{ "animation-duration": `${duration()}s` }}
        onAnimationIteration={() => setShown(untrack(() => props.items))}
      >
        {/* Rendered twice so the loop is seamless. */}
        <For each={[...shown(), ...shown()]}>
          {(c) => (
            <button class="tick" classList={{ brk: c.brk }} onClick={() => props.onPick(c)} tabIndex={-1}>
              <span class="tick-pub">{c.pub}</span>
              {c.title}
              {c.n > 1 ? <span class="tick-n">{c.n}</span> : null}
            </button>
          )}
        </For>
      </div>
    </div>
  );
}
