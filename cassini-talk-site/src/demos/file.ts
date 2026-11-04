import type { Binds } from "./bind";
import { wrap, clamp01, tfm, blr } from "./util";

export const FILE_BARS = 56;
const fsplit = 0.45;
const you = "var(--color-primary)";
const sam = "var(--color-accent)";
const dim = "color-mix(in oklab, var(--color-base-content) 40%, transparent)";

export const fileLines = [
  { who: "You", c: you, text: "Morning, everyone. Three things on the agenda today.", from: 0.02, to: fsplit - 0.02 },
  { who: "Chris", c: sam, text: "The best ideas usually arrive in the space between things.", from: fsplit + 0.02, to: 0.98 },
].map(l => ({ ...l, words: l.text.split(" ") }));

export const fileChips = [
  { name: "You", dot: you, isSam: false },
  { name: "Chris", dot: sam, isSam: true },
];

export const fileBars = Array.from({ length: FILE_BARS }, (_, j) => {
  const x = j / 55;
  const v = Math.abs(Math.sin(j * 1.7) * Math.sin(j * 0.37 + 1.3));
  return { x, h: Math.abs(x - fsplit) < 0.025 ? 3 : Math.round(5 + 26 * v), c: x < fsplit ? you : sam };
});

/** The .opus file tree at tick `t`. */
export function file(t: number, still: boolean): Binds {
  const b: Binds = {};
  const fu = still ? 64 : wrap(t, 112);
  const fplay = still || (fu >= 6 && fu < 100);
  const fp = still ? 0.62 : clamp01((fu - 6) / 94);
  const samTalking = fp >= fsplit;

  fileBars.forEach((bar, j) => {
    b["fb" + j] = { s: { opacity: fu >= 6 && bar.x <= fp ? 1 : 0.32 } };
  });
  b.fph = { s: { transform: "translateZ(0) translateX(" + (fp * 100 - 100).toFixed(2) + "%)", transition: fu < 6 ? "transform 0.55s cubic-bezier(0.32, 0.72, 0, 1)" : "transform 0.12s linear" } };

  fileLines.forEach((ln, i) => {
    const on = i === 0 ? !samTalking : samTalking;
    b["fl" + i] = { s: { opacity: on ? 1 : 0, transform: tfm(on ? 0 : i === 0 ? -8 : 8), filter: blr(on ? 0 : 4) } };
    ln.words.forEach((_, j, ws) => {
      const a = ln.from + ((ln.to - ln.from) * j) / ws.length;
      const z = ln.from + ((ln.to - ln.from) * (j + 1)) / ws.length;
      b[`fw${i}_${j}`] = { s: { color: fp >= a && fp < z && fplay ? ln.c : fp >= z ? "var(--color-base-content)" : dim } };
    });
  });

  fileChips.forEach((c, i) => {
    const lit = fplay && c.isSam === samTalking;
    b["fc" + i] = { s: { "border-color": lit ? "color-mix(in oklab, " + c.dot + " 60%, transparent)" : "var(--color-base-300)", "background-color": lit ? "color-mix(in oklab, " + c.dot + " 14%, transparent)" : "transparent" } };
  });
  return b;
}
