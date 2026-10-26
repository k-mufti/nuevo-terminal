import { For } from "solid-js";
import { PANELS } from "../panels";

const KEYS: [string, string][] = [
  ["1 – 7", "Switch panel"],
  ["h / l  ← →", "Previous / next panel"],
  ["j / k  ↓ ↑", "Next / previous story"],
  ["PgDn / PgUp", "Jump 20 stories"],
  ["g / G", "First / last story"],
  ["Enter  o", "Open story in a new tab"],
  ["f", "Full-screen the panel (again to go back)"],
  ["b", "Jump to the latest breaking story"],
  ["/  :  ⌘K", "Command bar"],
  ["Esc", "Clear filter / exit full screen / close"],
  ["?", "This help"],
];

export default function Help(props: { onClose: () => void }) {
  return (
    <div class="overlay" onClick={() => props.onClose()}>
      <div class="help" onClick={(e) => e.stopPropagation()} role="dialog" aria-label="Keyboard shortcuts">
        <h2>KEYBOARD</h2>
        <dl>
          <For each={KEYS}>
            {([k, v]) => (
              <>
                <dt>{k}</dt>
                <dd>{v}</dd>
              </>
            )}
          </For>
        </dl>
        <h2>COMMANDS</h2>
        <p class="dim">
          Type a command and press Enter (Bloomberg-style <span class="amber">&lt;GO&gt;</span> is optional).
        </p>
        <dl>
          <For each={PANELS}>
            {(p, i) => (
              <>
                <dt>{p.code}</dt>
                <dd>
                  {p.title} <span class="dim">({i() + 1})</span>
                </dd>
              </>
            )}
          </For>
          <dt>ALL</dt>
          <dd>Back to all panels</dd>
          <dt>BRK</dt>
          <dd>Latest breaking story</dd>
          <dt>anything else</dt>
          <dd>Filter every panel by those words</dd>
        </dl>
        <p class="dim center">Esc to close</p>
      </div>
    </div>
  );
}
