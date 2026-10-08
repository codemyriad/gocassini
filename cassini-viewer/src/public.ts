// Public embed entry (D-775).
//
// One recording, in any page, from a URL:
//
//   <script src="https://gocassini.com/embed/v1/viewer.js"></script>
//   <cassini-meeting src="https://gocassini.com/talk/talk.opus"></cassini-meeting>
//
// The attribute contract is ATTRIBUTES.md, which is the published surface; this
// file is its implementation.
//
// Three things make this different from the Nextcloud embed (src/embedded.ts),
// and they are the whole reason it is a second entry rather than a flag:
//
//  - It mounts MeetingView, not App. There is no catalog, no rooms rail and no
//    meeting list, because the page embedding a recording has already chosen
//    which recording. App's hash routing would also rewrite the HOST page's
//    fragment, which is not ours to touch.
//  - It finds itself from `document.currentScript` rather than from a
//    Nextcloud proxy path, so the stylesheet is resolved as a sibling of
//    whatever URL served the script — which is what makes the versioned
//    `/embed/v1/` and `/embed/v1.2.3/` paths work without being compiled in.
//  - It hands MeetingView a loader and no writer, so the marks in the file are
//    drawn and nothing offers to change them (D-775). A page on the open web
//    has no operator behind it, and the read-only surface is not a degraded
//    mode — it is the correct one.
//
// The stylesheet goes INSIDE the shadow root, as in the Nextcloud embed, so
// Tailwind Preflight and daisyUI are scoped to the viewer: we do not restyle
// someone else's page, and their CSS does not reach into ours.

import { mount, unmount } from "svelte";

import MeetingView from "./components/MeetingView.svelte";
import { StaticCatalogProvider } from "./viewer/dataProvider";
import { resolveEmbedTheme, singleMeetingEntry } from "./viewer/singleMeeting";
import "./app.css";

export const ELEMENT_NAME = "cassini-meeting";

// Where the badge points. An embed is the one artifact that travels to people
// who have never heard of Cassini, on pages we do not control, so it says what
// produced what they are reading.
export const BADGE_HREF = "https://gocassini.com";
export const BADGE_TEXT = "Recorded with Cassini";

// Dispatched on the element when the recording's audio will not play (D-838).
// The viewer shows no error of its own for this, so it is the page's to say.
export const PLAYBACK_ERROR_EVENT = "playbackerror";

// Attributes that take effect when changed after the element is on the page.
// The rest are read once, on arrival (ATTRIBUTES.md).
export const LIVE_ATTRIBUTES = ["theme"] as const;

// The embed's own chrome. Deliberately NOT in app.css: this is the wrapper the
// embed puts around MeetingView, and MeetingView must not grow a footer that
// only one of its two surfaces ever shows. Colours come from the daisyUI theme
// tokens so the badge follows `theme` with everything else.
export const EMBED_CSS = `
:host { display: block; }
.cassini-embed { display: flex; flex-direction: column; height: 100%; min-height: 0; }
.cassini-embed-view { flex: 1 1 auto; min-height: 0; }
.cassini-embed-badge {
  flex: none;
  display: flex;
  align-items: center;
  justify-content: flex-end;
  gap: 0.35em;
  padding: 0.35em 0.75em;
  font-family: ui-sans-serif, system-ui, sans-serif;
  font-size: 0.6875rem;
  line-height: 1.4;
  letter-spacing: 0.01em;
  text-decoration: none;
  background: var(--color-base-200);
  border-top: 1px solid var(--color-base-300);
  color: var(--color-base-content);
  opacity: 0.6;
  transition: opacity 120ms ease;
}
.cassini-embed-badge:hover, .cassini-embed-badge:focus-visible { opacity: 1; text-decoration: underline; }

/* Palette hooks (ATTRIBUTES.md, "Styling"). A page sets --cassini-color-* or
   --cassini-font-sans on the element, and they inherit into this shadow root.
   The theme's own value is first copied to a private property on the themed
   wrapper, so anything the page does not set keeps the theme's value. */
.cassini-embed {
  --cassini-theme-base-100: var(--color-base-100);
  --cassini-theme-base-200: var(--color-base-200);
  --cassini-theme-base-300: var(--color-base-300);
  --cassini-theme-base-content: var(--color-base-content);
  --cassini-theme-primary: var(--color-primary);
  --cassini-theme-primary-content: var(--color-primary-content);
  --cassini-theme-font-sans: var(--font-sans);
}
.cassini-embed-view, .cassini-embed-badge {
  --color-base-100: var(--cassini-color-base-100, var(--cassini-theme-base-100));
  --color-base-200: var(--cassini-color-base-200, var(--cassini-theme-base-200));
  --color-base-300: var(--cassini-color-base-300, var(--cassini-theme-base-300));
  --color-base-content: var(--cassini-color-base-content, var(--cassini-theme-base-content));
  --color-primary: var(--cassini-color-primary, var(--cassini-theme-primary));
  --color-primary-content: var(--cassini-color-primary-content, var(--cassini-theme-primary-content));
  --font-sans: var(--cassini-font-sans, var(--cassini-theme-font-sans));
}
/* font-family is resolved once, at the host, so re-point it where the
   overridden --font-sans is in scope. */
.cassini-embed-view { font-family: var(--font-sans); }

/* layout="inline": the page around the viewer already names the recording and
   shows its details, so the viewer keeps to the transcript and the player,
   and the player sits above the transcript instead of floating over it.
   The mv-* classes are MeetingView's hooks for exactly this. Unlayered, so
   these rules beat Tailwind's layered utilities. The !important overrides
   the inline right: the player sets to clear the scrollbar. */
.cassini-embed[data-layout="inline"] .mv-title,
.cassini-embed[data-layout="inline"] .mv-meta { display: none; }
.cassini-embed[data-layout="inline"] .mv-scroll { padding-bottom: 0; }
.cassini-embed[data-layout="inline"] .mv-player {
  position: relative;
  order: -1;
  right: auto !important;
  padding: 0;
  border-bottom: 1px solid var(--color-base-300);
}
.cassini-embed[data-layout="inline"] .mv-player > .card {
  border: 0;
  border-radius: 0;
  box-shadow: none;
}
`;

// The values layout accepts. Anything else is the default layout.
export const LAYOUTS = ["inline"] as const;

// The palette a page may set from outside (ATTRIBUTES.md, "Styling").
export const PALETTE_PROPERTIES = [
  "--cassini-color-base-100",
  "--cassini-color-base-200",
  "--cassini-color-base-300",
  "--cassini-color-base-content",
  "--cassini-color-primary",
  "--cassini-color-primary-content",
  "--cassini-font-sans",
] as const;

function prefersDarkScheme(): boolean {
  return typeof window.matchMedia === "function" && window.matchMedia("(prefers-color-scheme: dark)").matches;
}

// The served name, whatever version directory it sits in.
const SCRIPT_PATTERN = /\/viewer\.js(?:\?.*)?$/;

// currentScript is the reliable answer and is null under `defer`/`async` or
// when a bundler re-executes us, so fall back to the last matching script tag
// and then to the last script tag at all, the way Edible's embed does.
export function findEmbedScriptSrc(doc: Document): string {
  // Duck-typed rather than `instanceof HTMLScriptElement`: currentScript is
  // always a script element when it is not null, and the constructor does not
  // exist in node, which would put this helper out of reach of a unit test.
  const current = doc.currentScript as { src?: string } | null;
  if (current && typeof current.src === "string" && current.src) {
    return current.src;
  }
  const scripts = [...doc.querySelectorAll<HTMLScriptElement>("script[src]")];
  const named = scripts.filter((script) => SCRIPT_PATTERN.test(script.src));
  return (named.at(-1) ?? scripts.at(-1))?.src ?? "";
}

// The stylesheet is published beside the script, so one version directory holds
// a matching pair and a page can never load this build's JS against another
// build's CSS. An empty src means we could not find ourselves; a relative href
// is the best remaining guess and only styling degrades.
export function embedStylesheetHref(scriptSrc: string): string {
  if (!scriptSrc) {
    return "viewer.css";
  }
  try {
    return new URL("viewer.css" + queryOf(scriptSrc), scriptSrc).toString();
  } catch {
    return "viewer.css";
  }
}

// Carried over so the CSS expires from the browser cache with the JS.
function queryOf(src: string): string {
  const index = src.lastIndexOf("?");
  return index >= 0 ? src.slice(index) : "";
}

let scriptSrc = "";

// The element class is built here rather than at module scope because
// `extends HTMLElement` is evaluated where it is written, and there is no
// HTMLElement in node — which would make this module unimportable by a unit
// test of its own pure helpers.
export function defineCassiniMeeting(): void {
  if (typeof customElements === "undefined" || customElements.get(ELEMENT_NAME)) {
    return;
  }

  class CassiniMeetingElement extends HTMLElement {
    static observedAttributes = [...LIVE_ATTRIBUTES];

    private app: Record<string, unknown> | null = null;
    private wrapper: HTMLElement | null = null;

    // A page with its own light/dark switch sets theme again when the reader
    // flips it, so the viewer follows without being rebuilt (D-838).
    attributeChangedCallback(name: string): void {
      if (name === "theme" && this.wrapper) {
        this.wrapper.dataset.theme = resolveEmbedTheme(this.getAttribute("theme"), prefersDarkScheme());
      }
    }

    connectedCallback(): void {
      if (this.app) {
        return;
      }
      const src = (this.getAttribute("src") ?? "").trim();
      if (!src) {
        // Said once, plainly: an embed with no recording is a paste that went
        // wrong, and a silent empty box is the least useful way to report it.
        console.error(`<${ELEMENT_NAME}> needs a src attribute: the URL of a Cassini .opus recording.`);
        return;
      }

      const shadow = this.shadowRoot ?? this.attachShadow({ mode: "open" });
      if (!shadow.querySelector("link[data-cassini-embed]")) {
        const link = document.createElement("link");
        link.rel = "stylesheet";
        link.href = embedStylesheetHref(scriptSrc);
        link.dataset.cassiniEmbed = "";
        shadow.appendChild(link);
      }

      if (!shadow.querySelector("style[data-cassini-embed]")) {
        const style = document.createElement("style");
        style.dataset.cassiniEmbed = "";
        style.textContent = EMBED_CSS;
        shadow.appendChild(style);
      }

      // data-theme sits on the wrapper rather than the mount, so the badge under
      // the viewer is themed with it rather than falling back to the default.
      const wrapper = document.createElement("div");
      wrapper.className = "cassini-embed";
      wrapper.dataset.theme = resolveEmbedTheme(this.getAttribute("theme"), prefersDarkScheme());
      const layout = (this.getAttribute("layout") ?? "").trim().toLowerCase();
      if ((LAYOUTS as readonly string[]).includes(layout)) {
        wrapper.dataset.layout = layout;
      }
      this.wrapper = wrapper;

      const root = document.createElement("div");
      root.className = "cassini-root cassini-embed-view";
      wrapper.appendChild(root);

      // Shown unless the page asks otherwise. Built here rather than inside
      // MeetingView because it belongs to the embed, not to the meeting.
      if (!this.hasAttribute("hide-badge")) {
        const badge = document.createElement("a");
        badge.className = "cassini-embed-badge";
        badge.href = BADGE_HREF;
        badge.target = "_blank";
        badge.rel = "noopener noreferrer";
        badge.textContent = BADGE_TEXT;
        wrapper.appendChild(badge);
      }

      shadow.appendChild(wrapper);

      const provider = new StaticCatalogProvider();
      const entry = singleMeetingEntry(src, this.getAttribute("title") ?? "");
      this.app = mount(MeetingView, {
        target: root,
        props: {
          dataProvider: provider,
          meeting: entry,
          bundled: false,
          // Not an attribute: being an embed is what makes this true, and it is
          // not the embedding page's to choose.
          surface: "embed",
          isDesktop: this.clientWidth >= 720,
          prefersReducedMotion:
            typeof window.matchMedia === "function" &&
            window.matchMedia("(prefers-reduced-motion: reduce)").matches,
          // Read, and nowhere to write: the marks draw, and every control that
          // would change one is absent.
          loadAnnotations: () => provider.loadMeetingAnnotations(entry),
          applyAnnotations: null,
        },
        events: {
          playbackerror: (event: CustomEvent<string>) => {
            this.dispatchEvent(
              new CustomEvent(PLAYBACK_ERROR_EVENT, { detail: { message: event.detail }, bubbles: true }),
            );
          },
        },
      }) as Record<string, unknown>;
    }

    disconnectedCallback(): void {
      if (!this.app) {
        return;
      }
      void unmount(this.app);
      this.app = null;
    }
  }

  customElements.define(ELEMENT_NAME, CassiniMeetingElement);
}

// Capture the script URL at synchronous eval, while currentScript still answers,
// and register the element. Elements already parsed are upgraded by the browser;
// ones written later run connectedCallback on arrival, so a page may add a
// recording at any time.
if (typeof document !== "undefined" && !import.meta.env?.VITEST) {
  scriptSrc = findEmbedScriptSrc(document);
  defineCassiniMeeting();
}
