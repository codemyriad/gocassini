import type { Binds } from "./bind";
import { wrap, tfm, blr } from "./util";

export const SEARCH_STILL = 40;
const Q = 64;
const queries = ["Thursday", "attention", "launch"];
const notes = [
  { title: "A quieter kind of productivity · today", text: "What if being productive meant paying closer attention, rather than doing more? The best ideas usually arrive in the space between things. A walk, a conversation, a moment to pause. Let’s protect an hour on Thursday for focused work." },
  { title: "Weekly planning call · today", text: "Morning, everyone. Three things on the agenda today: the launch date, the onboarding copy and hiring. Let’s start with the launch." },
  { title: "Garden plans for the spring · Monday", text: "We could move the tomatoes to the sunny corner and leave Thursday free for planting." },
  { title: "Bookmark · Weekly planning call · 12:44", text: "Launch date agreed", mark: true },
  { title: "Bookmark · Weekly planning call · 12:51", text: "Ask Giulia about the copy", mark: true },
];

interface Hit { title: string; mark: boolean; before: string; match: string; after: string }

const lookup = (q: string): Hit[] => {
  const needle = q.trim().toLowerCase();
  const out: Hit[] = [];
  if (!needle) return out;
  for (const nt of notes) {
    const i = nt.text.toLowerCase().indexOf(needle);
    if (i < 0) continue;
    let from = Math.max(0, i - 22);
    const sp = from > 0 ? nt.text.indexOf(" ", from) : -1;
    if (sp >= 0 && sp < i) from = sp + 1;
    const to = Math.min(nt.text.length, i + needle.length + 64);
    out.push({ title: nt.title, mark: !!nt.mark, before: (from > 0 ? "…" : "") + nt.text.slice(from, i), match: nt.text.slice(i, i + needle.length), after: nt.text.slice(i + needle.length, to) + (to < nt.text.length ? "…" : "") });
  }
  return out.slice(0, 2);
};

/** The search tile at tick `t`. `query` is what the visitor typed (null: the demo types). */
export function search(t: number, still: boolean, query: string | null, focus: boolean): Binds {
  const b: Binds = {};
  const sq = still ? SEARCH_STILL : wrap(t, Q * queries.length);
  const word = queries[Math.floor(sq / Q)];
  const ph = sq % Q;
  const jit = [0, 0.4, -0.2, 0.3, -0.3, 0.5, 0, -0.2, 0.3];
  const keyAt: number[] = [];
  let typedEnd = 3;
  for (let i = 0; i < word.length; i++) {
    keyAt.push(typedEnd);
    typedEnd += 1.1 + jit[i % jit.length];
  }
  const cleared = ph >= Q - 7;
  const selected = ph >= Q - 11 && !cleared;
  const typedN = cleared ? 0 : keyAt.filter(x => ph >= x).length;
  const since = cleared ? ph - (Q - 7) : ph < 3 ? ph + 7 : ph - typedEnd;
  const demo = query === null && !focus;
  const results = demo ? lookup(word) : lookup(query || "");
  const showing = demo ? ph >= typedEnd + 2.5 && !cleared : results.length > 0;
  const blank: Hit = { title: "", mark: false, before: "", match: "", after: "" };

  b.input = { a: { placeholder: demo && typedN > 0 ? "" : "Search what was said" } };
  b.ov = { s: { opacity: demo ? 1 : 0 } };
  b.sel = { t: word.slice(0, typedN), s: { "background-color": selected ? "color-mix(in oklab, var(--color-primary) 35%, transparent)" : "transparent" } };
  b.caret = { s: { "margin-left": (typedN === 0 ? -3 : 1) + "px", opacity: !demo || selected ? 0 : still || (ph >= 3 && ph < typedEnd) || Math.floor(since / 5.3) % 2 === 0 ? 1 : 0 } };

  [0, 1].forEach(i => {
    const r = results[i] || results[0] || blank;
    const on = showing && i < results.length;
    const hd = (0.3 + i * 0.07).toFixed(2);
    const dl = (on ? i * 0.07 : i * 0.04).toFixed(2);
    const fade = r.before.charAt(0) === "…" ? "linear-gradient(to right, transparent, #000 14px)" : "none";
    b["r" + i] = {
      s: {
        opacity: on ? 1 : 0,
        transform: tfm(on ? 0 : demo && cleared ? -6 : 8),
        filter: blr(on ? 0 : 4),
        transition: `opacity 0.4s ease ${dl}s, transform 0.6s cubic-bezier(0.32, 0.72, 0, 1) ${dl}s, filter 0.4s cubic-bezier(0.32, 0.72, 0, 1) ${dl}s`,
      },
    };
    b["rim" + i] = { s: { display: r.mark ? "inline" : "none" } };
    b["rit" + i] = { s: { display: r.mark ? "none" : "inline" } };
    b["rt" + i] = { t: r.title };
    b["rb" + i] = { t: r.before, s: { "-webkit-mask-image": fade, "mask-image": fade } };
    b["rm" + i] = { s: { color: on ? "var(--color-primary)" : "color-mix(in oklab, var(--color-base-content) 80%, transparent)", transition: `color 0.45s ease ${hd}s` } };
    b["rh" + i] = { s: { "clip-path": on ? "inset(0 0 0 0)" : "inset(0 100% 0 0)", transition: on ? "clip-path 0.55s cubic-bezier(0.65, 0, 0.35, 1) " + hd + "s" : "clip-path 0.3s ease" } };
    b["rmt" + i] = { t: r.match };
    b["ra" + i] = { t: r.after };
  });

  b.miss = { t: "Nothing matches “" + (query || "").trim() + "”.", s: { opacity: !demo && (query || "").trim() !== "" && results.length === 0 ? 1 : 0 } };
  return b;
}
