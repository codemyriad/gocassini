import type { Binds } from "./bind";
import { wrap, tfm, blr, EASE } from "./util";

export const MARK_STILL = 76;
const M = 106;
const recFrom = 3;
const recTo = 72;
const recMax = 90;
const recP = (x: number) => (x < recFrom ? 0 : Math.min(recMax, ((x - recFrom) / (recTo - recFrom)) * recMax));
export const pinLefts = [28, 62];
const pinAt = pinLefts.map(p => recFrom + (p / recMax) * (recTo - recFrom));
const jumpAt = 80;
const mOut = 98;
export const markRows = [["12:44", "Launch date agreed"], ["12:51", "Ask Giulia about the copy"]] as const;

/** The "Mark a moment" tile at tick `t`. */
export function mark(t: number, still: boolean): Binds {
  const b: Binds = {};
  const mk = still ? MARK_STILL : wrap(t - 30, M);
  const playhead = mk >= mOut ? 0 : mk >= jumpAt + 8 ? 28 + (mk - jumpAt - 8) * 0.3 : mk >= jumpAt ? 28 : recP(mk);
  const tapping = !still && pinAt.some(x => mk >= x - 1.4 && mk < x);
  const pt = (mk >= jumpAt && mk < jumpAt + 8) || mk >= mOut ? `transform 0.75s ${EASE}` : "transform 0.12s linear";
  const fill = "translateZ(0) translateX(" + (playhead - 100).toFixed(2) + "%)";

  b.mbtn = { s: { "background-color": tapping ? "color-mix(in oklab, var(--color-base-content) 24%, var(--color-base-100))" : "var(--color-base-100)", transition: `background-color ${tapping ? 0.06 : 0.3}s ease-out` } };
  b.mring = { s: { animation: !still && pinAt.some(x => mk >= x && mk < x + 8) ? "mark-ring 0.8s cubic-bezier(0.22, 1, 0.36, 1)" : "none" } };
  b.mrec = { s: { transform: "translateZ(0) translateX(" + ((mk >= mOut ? 0 : recP(mk)) - 100).toFixed(2) + "%)", transition: pt } };
  b.mfill = { s: { transform: fill, transition: pt } };
  b.mhead = { s: { transform: fill, transition: pt } };

  pinLefts.forEach((_, i) => {
    const on = mk >= pinAt[i] && mk < mOut;
    b["pin" + i] = { s: { opacity: on ? 1 : 0, transform: "translateZ(0) scale(" + (on ? 1 : 0.4) + ")" } };
    b["pinr" + i] = { s: { animation: !still && ((mk >= pinAt[i] && mk < pinAt[i] + 9) || (i === 0 && mk >= jumpAt + 5 && mk < jumpAt + 14)) ? "pin-ring 0.9s cubic-bezier(0.22, 1, 0.36, 1)" : "none" } };
  });

  markRows.forEach((_, i) => {
    const on = mk >= pinAt[i] + 1 && mk < mOut - 2;
    const lit = i === 0 && mk >= jumpAt - 3 && mk < mOut - 2;
    const dl = (on ? 0 : i * 0.05).toFixed(2);
    b["row" + i] = {
      s: {
        "background-color": lit ? "color-mix(in oklab, var(--color-primary) 12%, transparent)" : "transparent",
        opacity: on ? 1 : 0,
        transform: tfm(on ? 0 : mk < pinAt[i] + 1 ? 8 : -6),
        filter: blr(on ? 0 : 4),
        transition: `background-color 0.35s ease, opacity 0.4s ease ${dl}s, transform 0.6s ${EASE} ${dl}s, filter 0.4s ${EASE} ${dl}s`,
      },
    };
    b["rowp" + i] = { s: { opacity: lit ? 1 : 0 } };
  });
  return b;
}
