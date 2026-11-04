import type { Binds } from "./bind";
import { wrap, tfm, blr, EASE } from "./util";

export const VOICE_STILL = 70;
const K = 100;
export const quote = "The best ideas usually arrive in the space between things.".split(" ");
let vEnd = 5;
const vCues = quote.map(w => {
  const at = vEnd;
  vEnd += Math.round((160 + 18 * w.replace(/[^A-Za-z]/g, "").length) / 100);
  return at;
});
const suggestAt = vEnd + 6;
const curAt = suggestAt + 7;
const pressAt = curAt + 8;
const releaseAt = pressAt + 1.33;
const confirmAt = pressAt + 2;

/** The "Put a name to every voice" tile at tick `t`. */
export function voice(t: number, still: boolean): Binds {
  const b: Binds = {};
  const vk = still ? VOICE_STILL : wrap(t - 12, K);
  const vOut = vk >= 88;
  const named = vk >= confirmAt && !vOut;
  const talking = !still && vk >= vCues[0] && vk < vEnd + 1;
  const swap = (on: boolean, before: boolean) => ({ opacity: on ? 1 : 0, transform: tfm(on ? 0 : before ? 6 : -6), filter: blr(on ? 0 : 3) });
  const okOn = vk >= suggestAt + 2 && vk < confirmAt && !vOut;
  const vHover = !still && vk >= curAt + 6 && vk < confirmAt;
  const vPress = !still && vk >= pressAt && vk < releaseAt;
  const curOn = !still && vk >= curAt && vk < confirmAt + 3;

  b.va = { s: { position: vk < suggestAt ? "relative" : "absolute", ...swap(vk >= 1 && vk < suggestAt && !vOut, vk < 1) } };
  b.vb = { s: { position: vk < suggestAt ? "absolute" : "relative", color: named ? "var(--color-accent)" : "color-mix(in oklab, var(--color-accent) 70%, transparent)", ...swap(vk >= suggestAt && !vOut, vk < suggestAt) } };
  b.vq = { s: { opacity: vk < confirmAt ? 1 : 0 } };

  const td = vPress ? "0.09s cubic-bezier(0.3, 0, 0.2, 1)" : vk >= releaseAt && vk < confirmAt ? "0.5s cubic-bezier(0.34, 1.56, 0.64, 1)" : `0.55s ${EASE}`;
  const bd = vPress ? 0.06 : 0.2;
  b.okw = {
    s: {
      opacity: okOn ? 1 : 0,
      transform: "translateZ(0)" + (vPress ? " scale(0.95)" : vk >= confirmAt ? " scale(0.92)" : okOn ? "" : " translateX(-6px)"),
      filter: blr(okOn ? 0 : 3),
      transition: `opacity 0.35s ease, transform ${td}, filter 0.35s ${EASE}`,
    },
  };
  b.ok = { s: { "background-color": vPress ? "color-mix(in oklab, var(--color-accent), #000 16%)" : vHover ? "color-mix(in oklab, var(--color-accent), #000 7%)" : "var(--color-accent)", transition: `background-color ${bd}s ease-out` } };
  b.okr = { s: { animation: !still && vk >= releaseAt && vk < releaseAt + 9 ? "click-ring-accent 0.8s cubic-bezier(0.22, 1, 0.36, 1)" : "none" } };

  const cx = vk < curAt + 1 ? -64 : vk < confirmAt + 3 ? 0 : -30;
  const cy = vk < curAt + 1 ? 70 : vk < confirmAt + 3 ? -4 : 30;
  const dur = vk < curAt + 1 ? 0 : vk < confirmAt + 3 ? 0.8 : 0.6;
  const cs = !curOn ? (vk < curAt ? 0.5 : 0.6) : vPress ? 0.85 : 1;
  const sh = vPress ? "0 2px 3px rgba(0, 0, 0, 0.5)" : "0 6px 14px rgba(0, 0, 0, 0.45)";
  const sd = vPress ? "0.08s cubic-bezier(0.3, 0, 0.2, 1)" : "0.4s cubic-bezier(0.34, 1.56, 0.64, 1)";
  b.vcur = { s: { transform: `translate3d(${cx}px, ${cy}px, 0)`, transition: `transform ${dur}s ${EASE}` } };
  b.vcuri = { s: { opacity: curOn ? 1 : 0, transform: `translateZ(0) scale(${cs})`, filter: `drop-shadow(${sh})`, transition: `opacity 0.3s ease, transform ${sd}, filter ${sd}` } };

  b.ck = { s: { opacity: named ? 1 : 0 } };
  b.ckp = { s: { "stroke-dashoffset": vk >= confirmAt ? 0 : 24, transition: vk >= confirmAt ? "stroke-dashoffset 0.5s cubic-bezier(0.65, 0, 0.35, 1) 0.12s" : "none" } };

  for (let i = 0; i < 4; i++) {
    b["eq" + i] = { s: { height: (talking ? Math.round(4 + 12 * Math.abs(Math.sin(t * (0.55 + i * 0.21) + i * 1.7))) : 4) + "px", opacity: vOut || vk < 1 ? 0 : talking ? 1 : 0.35 } };
  }
  quote.forEach((_, i) => {
    const shown = vk >= vCues[i];
    const dl = vOut ? (i * 0.03).toFixed(2) : 0;
    b["vw" + i] = {
      s: {
        color: shown && vk - vCues[i] < 3 ? "var(--color-accent)" : "var(--color-base-content)",
        opacity: !shown || vOut ? 0 : 1,
        transform: tfm(!shown ? 8 : vOut ? -8 : 0),
        filter: blr(shown && !vOut ? 0 : vOut ? 4 : 6),
        transition: `color 0.5s ease, opacity 0.35s ease ${dl}s, transform 0.5s ${EASE} ${dl}s, filter 0.4s ${EASE} ${dl}s`,
      },
    };
  });
  return b;
}
