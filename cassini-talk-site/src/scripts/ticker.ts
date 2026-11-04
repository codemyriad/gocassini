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

  const tick = (now: number) => {
    for (const [d, start] of started) {
      const n = Math.floor((now - start) / FRAME);
      if (n !== shown.get(d)) {
        shown.set(d, n);
        d.frame(n);
      }
    }
    raf = started.size ? requestAnimationFrame(tick) : 0;
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
        if (!raf) raf = requestAnimationFrame(tick);
      } else if (!e.isIntersecting && started.has(d)) {
        started.delete(d);
        d.leave?.();
      }
    }
  });
  demos.forEach(d => io.observe(d.el));
}
