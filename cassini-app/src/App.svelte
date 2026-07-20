<script lang="ts">
  import { onDestroy, onMount } from "svelte";
  import ViewerApp from "cassini-viewer/App.svelte";
  import { StaticCatalogProvider } from "cassini-viewer/dataProvider";
  import Operator from "./Operator.svelte";
  import { loadConfig } from "./operator/config";
  import { isLikelyAdminHint, probeOperatorAvailable } from "./operator/adminProbe";
  import { applySurface, readSurface, type Surface } from "./surfaceRouting";

  // The Cassini in-Nextcloud shell (D-420). It hosts role-gated surfaces fed
  // through the DataProvider seam: everyone gets "browse" (cassini-viewer's App
  // = MeetingList + MeetingView); admins additionally get the "operator"
  // surface (recording control). V3 adds the top nav + the operator surface.
  //
  // The operator JSON API stays ADMIN in info.xml (the REAL boundary). The
  // shell only decides whether to *show* the operator by probing that boundary
  // (403 -> hide); a non-admin who forced the surface still 403s at the proxy.
  export let ncMode: boolean = false;

  const dataProvider = new StaticCatalogProvider();

  // Browse is always available; operator is added only when the boundary probe
  // confirms admin access. The tab bar renders only when the operator surface
  // is available, so a non-admin sees exactly today's browse-only experience —
  // no shell chrome, byte-identical output.
  let operatorAvailable = false;
  let surface: Surface = "browse";

  // The daisyUI theme tokens (colors AND --radius-box/--border etc.) are emitted
  // on [data-theme=…], not on :host — so any surface NOT inside a data-theme'd
  // element gets no theme in the embedded shadow build. The viewer's App carries
  // data-theme on its own .cassini-root; the operator surface has no such
  // ancestor, so we give it one here (reusing .cassini-root also pulls in the
  // :host([data-nc-theme]) .cassini-root NC-colour overrides, matching browse).
  let themeMode: "saturn-light" | "saturn-dark" = "saturn-light";

  // Same key cassini-viewer's App persists its theme under.
  const THEME_STORAGE_KEY = "cassini-theme";
  function resolveThemeMode(): "saturn-light" | "saturn-dark" {
    // Agree with the viewer's own theme resolution so the operator surface
    // matches browse: honour the stored preference first, else the OS
    // preference. NC still overrides colours via :host([data-nc-theme]).
    try {
      const stored = localStorage.getItem(THEME_STORAGE_KEY);
      if (stored === "saturn-light" || stored === "saturn-dark") {
        return stored;
      }
    } catch {
      // localStorage unavailable — fall through to the media query
    }
    return window.matchMedia?.("(prefers-color-scheme: dark)").matches ? "saturn-dark" : "saturn-light";
  }

  function applySurfaceFromLocation(): void {
    const next = readSurface(window.location.hash);
    // A non-admin deep-linking #surface=operator falls back to browse (the
    // operator API would 403 anyway).
    surface = next === "operator" && !operatorAvailable ? "browse" : next;
  }

  function locationWithHash(hash: string): string {
    const url = new URL(window.location.href);
    url.hash = hash.replace(/^#/, "");
    return url.toString();
  }

  function selectSurface(next: Surface): void {
    if (next === surface) {
      return;
    }
    surface = next;
    // Fragment-only pushState — same mechanism the viewer uses; gives history /
    // back-forward + deep links without a pathname router (see surfaceRouting).
    // applySurface preserves the viewer's meeting/tx/t so switching surfaces
    // doesn't drop a meeting deep-link.
    window.history.pushState({}, "", locationWithHash(applySurface(window.location.hash, next)));
  }

  function handlePopState(): void {
    applySurfaceFromLocation();
  }

  onMount(async () => {
    themeMode = resolveThemeMode();

    // Optimistic anti-flash hint: if Nextcloud already tells us the user is an
    // admin, show the operator tab immediately instead of waiting a round-trip.
    if (isLikelyAdminHint(window) === true) {
      operatorAvailable = true;
    }
    applySurfaceFromLocation();
    window.addEventListener("popstate", handlePopState);

    // Authoritative: probe the ADMIN-gated operator boundary (200 -> show).
    try {
      const { operatorBasePath } = loadConfig();
      const probe = await probeOperatorAvailable(operatorBasePath);
      operatorAvailable = probe.available;
      // A non-admin is expected to be denied — AppAPI answers 403, or 404 when
      // it hides the operator routes entirely — so stay quiet on those. Anything
      // else (a network failure or an unexpected status) shouldn't silently hide
      // the operator surface with no trace, so surface it.
      if (!probe.available && probe.status !== 403 && probe.status !== 404) {
        console.warn(
          `Cassini: operator surface hidden — probe returned ${probe.status ?? "a network error"}.`,
        );
      }
    } catch (error) {
      // Graceful degradation (browse must still work) but NOT silent: a thrown
      // loadConfig (e.g. a base it can't parse) previously vanished the operator
      // surface with zero trace, which is exactly what hid the embedded-page
      // base bug (D-420 V3).
      operatorAvailable = false;
      console.error("Cassini: operator availability check failed.", error);
    }
    // Reconcile the active surface with the probe result (e.g. an optimistic
    // hint the probe denied, or a stale #surface=operator we can't honour).
    applySurfaceFromLocation();
  });

  onDestroy(() => {
    if (typeof window !== "undefined") {
      window.removeEventListener("popstate", handlePopState);
    }
  });
</script>

{#if operatorAvailable}
  <div class="cassini-shell">
    <nav class="cassini-shell-nav" data-theme={themeMode} aria-label="Cassini surfaces">
      <button
        type="button"
        class="cassini-shell-tab"
        aria-current={surface === "browse" ? "page" : undefined}
        on:click={() => selectSurface("browse")}
      >
        Browser
      </button>
      <button
        type="button"
        class="cassini-shell-tab"
        aria-current={surface === "operator" ? "page" : undefined}
        on:click={() => selectSurface("operator")}
      >
        Operator
      </button>
    </nav>

    <!-- Browse stays mounted (preserves list/meeting/playback state) and is
         hidden while the operator surface is active; the operator mounts only
         when active so its SSE stream + polling don't run in the background. -->
    <div class="cassini-shell-surface" class:cassini-shell-hidden={surface === "operator"}>
      <ViewerApp {ncMode} {dataProvider} />
    </div>
    {#if surface === "operator"}
      <!-- Scroll pane (bounded flex child) is kept SEPARATE from the themed
           .cassini-root: putting .cassini-root's height:100% on the flex/scroll
           element fought the flex sizing. Here the outer div is a clean bounded
           scroller (flex:1 + overflow-y:auto); the operator was authored to
           scroll at the page level (min-h-screen), which the fixed-height shell
           removes. The inner .cassini-root + data-theme give it the daisyUI
           theme in the shadow build (tokens aren't on :host) and the NC-colour
           overrides, exactly like the viewer's browse surface. -->
      <div class="cassini-shell-surface cassini-shell-scroll">
        <div class="cassini-root" data-theme={themeMode}>
          <Operator />
        </div>
      </div>
    {/if}
  </div>
{:else}
  <ViewerApp {ncMode} {dataProvider} />
{/if}

<style>
  /* Plain CSS (not Tailwind utilities) so the nav renders regardless of content
     scanning; theme tokens come from app.css (:root / :host). */
  .cassini-shell {
    display: flex;
    flex-direction: column;
    /* A DEFINITE height (not just min-height) so the viewer's height:100% chain
       resolves through the shell wrapper — :host{height:100%} in the embedded
       shadow build. With only min-height the wrapper's height is indefinite and
       the viewer's `.cassini-root{height:100%}` (which carries the bg-base-200
       grid) collapses to content height, leaving the meeting view with no
       background / broken layout (D-420 V3). */
    height: 100%;
    min-height: 100%;
  }

  /* Deliberately compact: this bar is persistent chrome above a viewer that
     wants every pixel of height (the player sits at the bottom edge), and it
     switches between only two surfaces — so it is sized as a control, not as
     primary navigation. */
  /* Colours resolve through a three-step chain, outermost wins:
       1. Nextcloud's own vars (--color-main-background etc.) — these inherit
          through the shadow boundary from the host page :root (see the D-414
          block in the viewing layer's app.css), so they track NC's light/dark
          theme with no JS.
       2. the daisyUI token — reachable because this nav now carries its own
          [data-theme], the same trick the operator surface uses. Without it the
          nav sits outside every [data-theme] element, every token is undefined,
          and all three fall through to the hardcoded light values below — which
          is why the whole toolbar stayed light in dark mode.
       3. a hardcoded light default, for a build with neither. */
  .cassini-shell-nav {
    display: flex;
    gap: 0.5rem;
    padding: 0.5rem 1rem;
    border-bottom: 1px solid var(--color-border-dark, var(--color-base-300, #e5e7eb));
    background: var(--color-main-background, var(--color-base-100, #ffffff));
  }

  .cassini-shell-tab {
    appearance: none;
    border: 0;
    background: transparent;
    cursor: pointer;
    padding: 0.25rem 0.5rem;
    border-radius: 0.375rem;
    font: inherit;
    font-size: 0.8125rem;
    line-height: 1.35;
    font-weight: 600;
    color: var(--color-main-text, var(--color-base-content, #1f2937));
    opacity: 0.7;
  }

  .cassini-shell-tab:hover {
    background: var(--color-background-hover, var(--color-base-200, #f3f4f6));
    opacity: 1;
  }

  /* Neutral high-contrast highlight rather than the theme accent: in Nextcloud
     --color-primary is the SAME colour as the app header, so an accent-filled tab
     stacked two heavy accent blocks and read louder than the content it switches.

     Swapping the foreground and background tokens inverts the fill for free —
     near-black on white in light mode, near-white on black in dark — and it
     tracks whichever theme system is live (NC's or daisyUI's) instead of needing
     a hardcoded dark-mode branch. */
  .cassini-shell-tab[aria-current="page"] {
    background: var(--color-main-text, var(--color-base-content, #000000));
    color: var(--color-main-background, var(--color-base-100, #ffffff));
    opacity: 1;
  }

  .cassini-shell-surface {
    flex: 1 1 auto;
    min-height: 0;
  }

  /* The operator surface scrolls within its own pane (it was authored for
     page-level scroll via min-h-screen, which the fixed-height shell removes). */
  .cassini-shell-scroll {
    overflow-y: auto;
  }

  .cassini-shell-hidden {
    display: none;
  }
</style>
