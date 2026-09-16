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

// The served name, whatever version directory it sits in.
const SCRIPT_PATTERN = /\/viewer\.js(?:\?.*)?$/;

// currentScript is the reliable answer and is null under `defer`/`async` or
// when a bundler re-executes us, so fall back to the last matching script tag
// and then to the last script tag at all, the way Edible's embed does.
export function findEmbedScriptSrc(doc: Document): string {
  const current = doc.currentScript;
  if (current instanceof HTMLScriptElement && current.src) {
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

class CassiniMeetingElement extends HTMLElement {
  private app: Record<string, unknown> | null = null;

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

    const prefersDark =
      typeof window.matchMedia === "function" &&
      window.matchMedia("(prefers-color-scheme: dark)").matches;
    const root = document.createElement("div");
    root.className = "cassini-root";
    root.dataset.theme = resolveEmbedTheme(this.getAttribute("theme"), prefersDark);
    shadow.appendChild(root);

    const provider = new StaticCatalogProvider();
    const entry = singleMeetingEntry(src, this.getAttribute("title") ?? "");
    this.app = mount(MeetingView, {
      target: root,
      props: {
        dataProvider: provider,
        meeting: entry,
        bundled: false,
        isDesktop: this.clientWidth >= 720,
        prefersReducedMotion:
          typeof window.matchMedia === "function" &&
          window.matchMedia("(prefers-reduced-motion: reduce)").matches,
        // Read, and nowhere to write: the marks draw, and every control that
        // would change one is absent.
        loadAnnotations: () => provider.loadMeetingAnnotations(entry),
        applyAnnotations: null,
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

export function defineCassiniMeeting(): void {
  if (typeof customElements === "undefined" || customElements.get(ELEMENT_NAME)) {
    return;
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
