import type { Binds } from "./bind";
import { clock, clamp01, tfm, blr, EASE } from "./util";

/** Loop length in ticks (10 ticks a second). */
export const HERO_T = 158;
/** The reduced-motion still: the Search stage, "attention" found. */
export const HERO_STILL = 130;

export const steps = [
  { id: "rec", label: "Record", from: 0, len: 52, long: "Record the conversation." },
  { id: "trx", label: "Transcribe", from: 52, len: 52, long: "Read it back, word for word." },
  { id: "find", label: "Search", from: 104, len: 54, long: "Find the part that mattered." },
] as const;

export const BAR_COUNT = 44;
export const sentence = "What if being productive meant paying closer attention, rather than doing more?".split(" ");
const target = "attention";

let cue = 54;
const cues = sentence.map(w => {
  const at = cue;
  cue += Math.round((160 + 18 * w.replace(/[^A-Za-z]/g, "").length) / 100) + (/,$/.test(w) ? 3 : 0);
  return at;
});
const cueEnd = cue;
const said = (at: number) => clock(Math.max(0, Math.floor((Math.min(at, cueEnd) - cues[0]) / 10)));
export const chipAt = said(cues[sentence.findIndex(w => w.toLowerCase().indexOf(target) === 0)]);

const typedEdge = [100, 84.2, 76.5, 69.2, 56.8, 44, 36.3, 31.1, 18.3, 5.5];

/** Every bound value of the hero at loop position `h`. */
export function hero(h: number, still: boolean): Binds {
  const b: Binds = {};
  const between = (from: number, to: number) => h >= from && h < to;
  const stage = h < 52 ? "rec" : h < 104 ? "trx" : "find";

  steps.forEach((x, i) => {
    const on = x.id === stage;
    b["step" + i] = {
      s: { color: on ? "var(--color-base-content)" : "color-mix(in oklab, var(--color-base-content) 40%, transparent)" },
      a: { "aria-selected": on ? "true" : "false", tabindex: on ? "0" : "-1" },
    };
    b["fill" + i] = {
      s: {
        "--p": on ? clamp01((h - x.from) / x.len).toFixed(4) : 0,
        opacity: on ? 1 : 0,
        transition: on ? "transform 0.15s linear, opacity 0.3s ease" : "transform 0s linear 0.45s, opacity 0.45s ease",
      },
    };
  });

  const scene = (from: number, to: number) => {
    const on = between(from, to);
    return { opacity: on ? 1 : 0, transform: "translateZ(0)", filter: on ? "none" : "blur(5px)", "pointer-events": on ? "auto" : "none" };
  };
  b.s1 = { s: scene(0, 52) };
  b.s2 = { s: scene(52, 156) };

  const live = between(17, 64);
  const bloom = clamp01((h - 17) / 5);
  for (let i = 0; i < BAR_COUNT; i++) {
    const d = Math.abs(i - 21.5) / 21.5;
    const grow = clamp01((bloom - d * 0.6) / 0.4);
    const v = Math.abs(Math.sin((i + h) * 0.5) * Math.sin(i * 0.31 + h * 0.17));
    b["bar" + i] = { s: { height: (live ? Math.round(3 + (5 + v * 70) * grow) : 3) + "px", opacity: live ? 1 : 0 } };
  }

  const hovering = !still && between(11, 17);
  const pressing = !still && between(13, 14.33);
  const btnOn = between(2, 17);
  const k = pressing ? 0.95 : between(17, 52) ? 0.85 : 1;
  const y = h < 2 || h >= 52 ? 10 : 0;
  const td = pressing ? "0.09s cubic-bezier(0.3, 0, 0.2, 1)" : "0.5s cubic-bezier(0.34, 1.56, 0.64, 1)";
  const bd = pressing ? 0.06 : 0.2;
  b.btnw = { s: { opacity: btnOn ? 1 : 0, transform: tfm(y, k), "pointer-events": btnOn ? "auto" : "none", transition: `opacity 0.35s ease, transform ${td}` } };
  b.btn = {
    s: {
      "--btn-color": pressing ? "color-mix(in oklab, var(--color-primary), #000 16%)" : hovering ? "color-mix(in oklab, var(--color-primary), #000 7%)" : "var(--color-primary)",
      transition: `background-color ${bd}s ease-out, border-color ${bd}s ease-out`,
    },
  };
  b.ring = { s: { animation: between(14.33, 23) ? "click-ring 0.8s cubic-bezier(0.22, 1, 0.36, 1)" : "none" } };

  b.timer = {
    t: clock(Math.max(0, Math.floor((Math.min(h, 52) - 17) / 10))),
    s: {
      color: h < 17 ? "color-mix(in oklab, var(--color-base-content) 50%, transparent)" : "var(--color-base-content)",
      opacity: between(3, 52) ? 1 : 0,
      transform: tfm(h < 3 ? 8 : h < 52 ? 0 : -8),
    },
  };

  const showCursor = !still && between(5, 18);
  const cx = h < 6 ? 170 : h < 18 ? 4 : 50;
  const cy = h < 6 ? 100 : h < 18 ? -42 : 10;
  const dur = h < 6 ? 0 : h < 18 ? 0.8 : 0.6;
  const cs = !showCursor ? (h < 5 ? 0.5 : 0.6) : pressing ? 0.85 : 1;
  const sh = pressing ? "0 2px 3px rgba(0, 0, 0, 0.5)" : "0 6px 14px rgba(0, 0, 0, 0.45)";
  const sd = pressing ? "0.08s cubic-bezier(0.3, 0, 0.2, 1)" : "0.4s cubic-bezier(0.34, 1.56, 0.64, 1)";
  b.cur = { s: { transform: `translate3d(${cx}px, ${cy}px, 0)`, transition: `transform ${dur}s ${EASE}` } };
  b.curi = { s: { opacity: showCursor ? 1 : 0, transform: `translateZ(0) scale(${cs})`, filter: `drop-shadow(${sh})`, transition: `opacity 0.3s ease, transform ${sd}, filter ${sd}` } };

  const typed = Math.max(0, Math.min(target.length, Math.floor(h - 107)));
  const finding = h >= 104;
  const swept = h >= 118;
  const leaving = h >= 145;
  sentence.forEach((w, i) => {
    const shown = h >= cues[i];
    const fresh = shown && h - cues[i] < 2 && !finding;
    const hit = w.toLowerCase().indexOf(target) === 0;
    const od = leaving ? 0.35 : swept ? 0.6 : 0.3;
    const dl = leaving ? (i * 0.035).toFixed(3) : 0;
    b["w" + i] = {
      s: {
        opacity: !shown || leaving ? 0 : swept && !hit ? 0.32 : 1,
        transform: tfm(!shown ? 10 : leaving ? -10 : 0),
        filter: blr(shown && !leaving ? 0 : leaving ? 4 : 6),
        transition: `opacity ${od}s ease ${dl}s, transform 0.5s ${EASE} ${dl}s, filter 0.4s ${EASE} ${dl}s`,
      },
    };
    const clip = !hit || !typed ? "inset(0 100% 0 0 round 3px)" : "inset(0 " + typedEdge[typed] + "% 0 " + (h < 143 ? 0 : 100) + "% round 3px)";
    const ct = !hit || !typed || h < 143 ? "none" : "clip-path 0.6s cubic-bezier(0.65, 0, 0.35, 1)";
    b["wbg" + i] = { s: { "clip-path": clip, transition: ct } };
    b["wov" + i] = { s: { "clip-path": clip, transition: ct } };
    b["wt" + i] = { s: { color: fresh ? "var(--color-primary)" : "var(--color-base-content)" } };
  });

  b.label = { s: { opacity: between(53, 104) ? 1 : 0, transform: tfm(finding ? -10 : 0) } };
  b.ltime = { t: said(h) };
  b.pill = { s: { opacity: between(104, 144) ? 1 : 0, transform: tfm(h < 104 ? 10 : h < 144 ? 0 : -8), filter: blr(h < 144 ? 0 : 4) } };
  b.query = { t: target.slice(0, typed) };
  b.caret = { s: { opacity: !between(104, 144) ? 0 : h < 116 || Math.floor((h - 116) / 5.3) % 2 === 0 ? 1 : 0 } };
  b.chip = { s: { opacity: between(118, 142) ? 1 : 0, transform: tfm(h < 118 ? 12 : h < 142 ? 0 : -6), filter: blr(between(118, 142) ? 0 : 4) } };

  return b;
}
