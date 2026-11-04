import type { Binds } from "./bind";
import { clock } from "./util";

export interface Sample { a: number; sp: number }
export const SAMPLES = 110;

/** Two people taking turns: syllable envelopes, short gaps, pauses between turns. */
export class Conversation {
  private speaker = 0;
  private turnLeft = 30;
  private pause = 0;
  private gap = 0;
  private step = 3;
  private sylLen = 3;
  private peak = 0.6;
  samples: Sample[] = [];

  constructor(private r: () => number = Math.random) {
    for (let i = 0; i < SAMPLES; i++) this.samples.unshift(this.next());
  }

  push() {
    this.samples = [this.next()].concat(this.samples).slice(0, SAMPLES);
  }

  private next(): Sample {
    const r = this.r;
    if (this.pause > 0) {
      this.pause -= 1;
      if (this.pause === 0) {
        this.speaker = 1 - this.speaker;
        this.turnLeft = 20 + Math.floor(r() * 40);
      }
      return { a: 0.02 + r() * 0.04, sp: this.speaker };
    }
    if (this.turnLeft <= 0) {
      this.pause = 5 + Math.floor(r() * 8);
      return { a: 0.03, sp: this.speaker };
    }
    this.turnLeft -= 1;
    if (this.gap > 0) {
      this.gap -= 1;
      return { a: 0.05 + r() * 0.08, sp: this.speaker };
    }
    if (this.step >= this.sylLen) {
      this.step = 0;
      this.sylLen = 2 + Math.floor(r() * 3);
      this.peak = 0.3 + r() * 0.7;
      if (r() < 0.2) this.gap = 1 + Math.floor(r() * 2);
    }
    const env = Math.sin((Math.PI * (this.step + 0.5)) / this.sylLen);
    this.step += 1;
    return { a: Math.min(1, this.peak * env * (0.8 + r() * 0.4)), sp: this.speaker };
  }
}

/** "Record the call you're on": two lanes, frame `n` (30 a second). */
export function desk(samples: Sample[], n: number, still: boolean): Binds {
  const b: Binds = {};
  const t = n / 3;
  const fr = n % 3;
  b.dtime = { t: clock(1100 + Math.floor(t / 10)) };
  const dx = "translateZ(0) translateX(" + (still ? 0 : (2 - fr) * 2) + "px)";
  [0, 1].forEach(sp => {
    const talking = samples.slice(0, 8).some(x => x.sp === sp && x.a > 0.15);
    b["lane" + sp] = { s: { color: talking ? "var(--color-base-content)" : "color-mix(in oklab, var(--color-base-content) 55%, transparent)" } };
    b["strip" + sp] = { s: { transform: dx } };
    for (let k = 0; k < SAMPLES; k++) {
      const x = samples[SAMPLES - 1 - k];
      b[`d${sp}_${k}`] = { s: { height: (x && x.sp === sp ? Math.round(3 + 38 * x.a) : 3) + "px" } };
    }
  });
  return b;
}

export const WAVE_HALF = 36;
const colour = (sp: number) => (sp === 0 ? "var(--color-primary)" : "var(--color-accent)");

/** The closing band: the newest sample at the centre, spreading out both ways. */
export function wave(samples: Sample[]): Binds {
  const b: Binds = {};
  const bar = (j: number) => {
    const s = samples[j] || { a: 0.02, sp: 0 };
    return { height: Math.round(4 + 110 * s.a) + "px", "background-color": colour(s.sp), opacity: (0.95 - (0.7 * j) / (WAVE_HALF - 1)).toFixed(2) };
  };
  let k = 0;
  for (let j = WAVE_HALF - 1; j >= 0; j--) b["wv" + k++] = { s: bar(j) };
  for (let j = 0; j < WAVE_HALF; j++) b["wv" + k++] = { s: bar(j) };
  const now = (samples[0] || { sp: 0 }).sp;
  b.cta = { s: { "--btn-color": colour(now), "--btn-fg": now === 0 ? "var(--color-primary-content)" : "var(--color-accent-content)" } };
  return b;
}
