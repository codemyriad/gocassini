import { Binder, type Binds } from "../demos/bind";
import { hero, HERO_T, HERO_STILL, steps } from "../demos/hero";
import { nums } from "../demos/nums";
import { search } from "../demos/search";
import { voice } from "../demos/voice";
import { mark } from "../demos/mark";
import { file } from "../demos/file";
import { desk, wave, Conversation, type Sample } from "../demos/voices";
import { wrap } from "../demos/util";
import { run, snap, type Demo } from "./ticker";

const reduce = matchMedia("(prefers-reduced-motion: reduce)").matches;
const root = (name: string) => document.querySelector<HTMLElement>(`[data-demo="${name}"]`)!;
const demos: Demo[] = [];

/** Hero: three stages on one loop; the steps are tabs that jump to their stage. */
{
  const el = root("hero");
  const binder = new Binder(el);
  let t = 0;
  let offset = 0;
  const render = () => binder.apply(hero(wrap(t - offset, HERO_T), false));
  const stillFor = [40, 95, HERO_STILL];
  const jump = (i: number, to: number = steps[i].from) => {
    if (reduce) {
      binder.apply(hero(stillFor[i], true));
      return;
    }
    offset = t - to;
    render();
  };
  const tabs = [...el.querySelectorAll<HTMLButtonElement>("[data-step]")];
  tabs.forEach((tab, i) => tab.addEventListener("click", () => jump(i)));
  el.querySelector("[role=tablist]")?.addEventListener("keydown", e => {
    const key = (e as KeyboardEvent).key;
    const i = tabs.indexOf(document.activeElement as HTMLButtonElement);
    if (i < 0) return;
    const to = { ArrowRight: i + 1, ArrowDown: i + 1, ArrowLeft: i - 1, ArrowUp: i - 1, Home: 0, End: tabs.length - 1 }[key];
    if (to === undefined) return;
    e.preventDefault();
    const next = (to + tabs.length) % tabs.length;
    jump(next);
    tabs[next].focus();
  });
  el.querySelector("[data-record]")?.addEventListener("click", () => jump(0, 17));
  demos.push({
    el: el.querySelector(".lead-card")!,
    enter: () => (offset = 0),
    frame: n => {
      t = n / 3;
      render();
    },
  });
}

{
  const el = root("nums");
  const binder = new Binder(el);
  demos.push({ el, frame: n => binder.apply(nums(Math.floor(n / 3))) });
}

/** Search: the demo types until the visitor focuses the field, then it searches what they type. */
{
  const el = root("search");
  const binder = new Binder(el);
  const input = el.querySelector("input")!;
  let t = 0;
  let query: string | null = null;
  let focus = false;
  const render = () => binder.apply(search(t, reduce, query, focus));
  input.addEventListener("input", () => {
    query = input.value === "" ? null : input.value;
    render();
  });
  input.addEventListener("focus", () => {
    focus = true;
    render();
  });
  input.addEventListener("blur", () => {
    focus = false;
    render();
  });
  demos.push({
    el,
    frame: n => {
      t = n / 3;
      render();
    },
  });
}

for (const [name, fn] of [["voice", voice], ["mark", mark], ["file", file]] as const) {
  const el = root(name);
  const binder = new Binder(el);
  demos.push({ el, frame: n => binder.apply(fn(n / 3, false)) });
}

/** The two sample-driven demos: a new sample every third frame, as in the mock. */
const views: [string, (s: Sample[], n: number) => Binds][] = [
  ["desk", (s, n) => desk(s, n, false)],
  ["wave", s => wave(s)],
];
for (const [name, view] of views) {
  const el = root(name);
  const binder = new Binder(el);
  const talk = new Conversation();
  let last = 0;
  demos.push({
    el,
    enter: () => (last = 0),
    frame: n => {
      const due = Math.min(110, Math.floor(n / 3) - Math.floor(last / 3));
      for (let i = 0; i < due; i++) talk.push();
      last = n;
      binder.apply(view(talk.samples, n));
    },
  });
}

/** The language ring is CSS: restart its animations on entry, pause them off screen. */
{
  const ring = document.querySelector<HTMLElement>("[data-ring]");
  if (ring && !reduce) {
    new IntersectionObserver(([e]) => {
      if (e.isIntersecting) {
        snap(ring, () => ring.getAnimations({ subtree: true }).forEach(a => (a.currentTime = 0)));
        ring.classList.add("is-playing");
      } else {
        ring.classList.remove("is-playing");
      }
    }).observe(ring);
  }
}

if (!reduce) run(demos);
