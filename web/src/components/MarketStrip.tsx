import { For, Show } from "solid-js";
import { pct, price } from "../format";
import type { Quote } from "../types";

function Spark(props: { values: number[]; up: boolean }) {
  const points = () => {
    const v = props.values;
    if (v.length < 2) return "";
    const lo = Math.min(...v);
    const hi = Math.max(...v);
    const span = hi - lo || 1;
    return v.map((y, i) => `${((i / (v.length - 1)) * 40).toFixed(1)},${(11 - ((y - lo) / span) * 10).toFixed(1)}`).join(" ");
  };
  return (
    <svg class="spark" classList={{ up: props.up, dn: !props.up }} viewBox="0 0 40 12" width="40" height="12" aria-hidden="true">
      <polyline points={points()} fill="none" stroke="currentColor" stroke-width="1" vector-effect="non-scaling-stroke" />
    </svg>
  );
}

export default function MarketStrip(props: { quotes: Quote[] }) {
  return (
    <div class="markets" aria-label="Markets">
      <Show when={props.quotes.length > 0} fallback={<span class="dim">Loading markets…</span>}>
        <For each={props.quotes}>
          {(q) => (
            <div class="quote" classList={{ up: q.change > 0, dn: q.change < 0 }} title={q.sym}>
              <span class="q-name">{q.name}</span>
              <span class="q-price">{price(q.price)}</span>
              <span class="q-pct">{pct(q.pct)}</span>
              <Show when={q.spark && q.spark.length > 1}>
                <Spark values={q.spark!} up={q.change >= 0} />
              </Show>
            </div>
          )}
        </For>
      </Show>
    </div>
  );
}
