/**
 * One shared 30fps clock for every looping demo. A demo ticks only while it
 * is in view, and each time it enters it restarts from frame 0.
 */
export interface Demo {
  /** The element whose visibility starts and pauses the demo. */
  el: Element;
  /** Render frame `n` (30 a second) since the demo last entered the viewport. */
  frame(n: number): void;
  enter?(): void;
  leave?(): void;
}

const FRAME = 1000 / 30;

/** Apply a state change with transitions off, so a restart snaps instead of animating back. */
export const snap = (el: Element, fn: () => void) => {
  el.classList.add("demo-snap");
  fn();
  void (el as HTMLElement).offsetWidth;
  el.classList.remove("demo-snap");
};

export function run(demos: Demo[]) {
  const started = new Map<Demo, number>();
  const shown = new Map<Demo, number>();
  const byEl = new Map(demos.map(d => [d.el, d]));
  let raf = 0;

  let timer = 0;
  const tick = (now: number) => {
    raf = 0;
    let wait = FRAME;
    for (const [d, start] of started) {
      const at = (now - start) / FRAME;
      const n = Math.floor(at);
      if (n !== shown.get(d)) {
        shown.set(d, n);
        d.frame(n);
      }
      wait = Math.min(wait, (n + 1 - at) * FRAME);
    }
    if (!started.size) return;
    // Sleep until just before the next 30fps frame instead of waking on every display refresh.
    timer = window.setTimeout(() => {
      timer = 0;
      raf = requestAnimationFrame(tick);
    }, Math.max(0, wait - 6));
  };
  const wake = () => {
    if (!raf && !timer) raf = requestAnimationFrame(tick);
  };

  const io = new IntersectionObserver(entries => {
    for (const e of entries) {
      const d = byEl.get(e.target);
      if (!d) continue;
      if (e.isIntersecting && !started.has(d)) {
        snap(d.el, () => {
          d.enter?.();
          d.frame(0);
        });
        shown.set(d, 0);
        started.set(d, performance.now());
        wake();
      } else if (!e.isIntersecting && started.has(d)) {
        started.delete(d);
        d.leave?.();
      }
    }
  });
  demos.forEach(d => io.observe(d.el));
}
