export const pad = (n: number) => String(n).padStart(2, "0");
export const clock = (sec: number) => pad(Math.floor(sec / 60)) + ":" + pad(sec % 60);
export const wrap = (n: number, m: number) => ((n % m) + m) % m;
export const clamp01 = (v: number) => Math.max(0, Math.min(1, v));
export const tfm = (y: number, k?: number) =>
  "translateZ(0)" + (y ? " translateY(" + y + "px)" : "") + (k !== undefined && k !== 1 ? " scale(" + k + ")" : "");
export const blr = (b: number) => (b ? "blur(" + b + "px)" : "none");

export const EASE = "cubic-bezier(0.32, 0.72, 0, 1)";
export const POP = "cubic-bezier(0.34, 1.56, 0.64, 1)";

/** Seeded PRNG, so the build's still frames are the same on every build. */
export const mulberry32 = (seed: number) => () => {
  seed |= 0;
  seed = (seed + 0x6d2b79f5) | 0;
  let t = Math.imul(seed ^ (seed >>> 15), 1 | seed);
  t = (t + Math.imul(t ^ (t >>> 7), 61 | t)) ^ t;
  return ((t ^ (t >>> 14)) >>> 0) / 4294967296;
};
