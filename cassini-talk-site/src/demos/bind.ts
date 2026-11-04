export type Style = Record<string, string | number>;

/** One bound element: inline style properties, text, attributes. */
export interface Bind {
  s?: Style;
  t?: string;
  a?: Record<string, string>;
}

/** Keyed by the element's data-k inside its demo root. */
export type Binds = Record<string, Bind>;

export const css = (s?: Style) => (s ? Object.entries(s).map(([k, v]) => `${k}: ${v}`).join("; ") : "");

/** For Astro: an element's static style plus its bound still-frame style. */
export const styler = (b: Binds) => (key: string, base = "") => [base, css(b[key]?.s)].filter(Boolean).join("; ");

/** For the client: applies binds to elements under `root`, writing only what changed. */
export class Binder {
  private els = new Map<string, HTMLElement>();
  private seen = new Map<string, string>();

  constructor(root: Element) {
    root.querySelectorAll<HTMLElement>("[data-k]").forEach(el => this.els.set(el.dataset.k!, el));
  }

  apply(binds: Binds) {
    for (const key in binds) {
      const el = this.els.get(key);
      if (!el) continue;
      const { s, t, a } = binds[key];
      if (s) {
        for (const prop in s) {
          const v = String(s[prop]);
          const id = key + "|" + prop;
          if (this.seen.get(id) === v) continue;
          this.seen.set(id, v);
          el.style.setProperty(prop, v);
        }
      }
      if (t !== undefined && this.seen.get(key + "|#") !== t) {
        this.seen.set(key + "|#", t);
        el.textContent = t;
      }
      if (a) {
        for (const name in a) {
          const id = key + "|@" + name;
          if (this.seen.get(id) === a[name]) continue;
          this.seen.set(id, a[name]);
          el.setAttribute(name, a[name]);
        }
      }
    }
  }
}
