<script lang="ts">
  import { onDestroy, onMount } from "svelte";
  import ViewerApp from "cassini-viewer/App.svelte";
  import { StaticCatalogProvider } from "cassini-viewer/dataProvider";
  import Operator from "./Operator.svelte";
  import Settings from "./Settings.svelte";
  import SetupNotice from "./SetupNotice.svelte";
  import { loadConfig } from "./operator/config";
  import { isLikelyAdminHint, probeOperatorAvailable } from "./operator/adminProbe";
  import {
    buildSetupNotice,
    fetchSetupHealth,
    readRecordingsAccess,
    shareableAppUrl,
    type SetupNotice as SetupNoticeContent,
  } from "./operator/setupHealth";
  import { applySurface, readSurface, type Surface } from "./surfaceRouting";

  // The Cassini in-Nextcloud shell (D-420). It hosts role-gated surfaces fed
  // through the DataProvider seam: everyone gets "browse" (cassini-viewer's App
  // = MeetingList + MeetingView); admins additionally get the "operator"
  // surface (recording control). V3 adds the top nav + the operator surface.
  //
  // The operator JSON API stays ADMIN in info.xml (the REAL boundary). The
  // shell only decides whether to *show* the operator by probing that boundary
  // (denied -> hide); a non-admin who forced the surface still 403s at the proxy.
  //
  // That one probe now decides two things, deliberately: whether there is an
  // operator surface, and — when the deployment is not set up — which of the two
  // explanations you get (setupHealth.ts). Being able to read the ADMIN-gated
  // /status IS being an administrator, so there is no second notion of admin
  // here to drift from the first.
  export let ncMode: boolean = false;

  const dataProvider = new StaticCatalogProvider();

  // Browse is always available; operator is added only when the boundary probe
  // confirms admin access. The tab bar renders only when the operator surface
  // is available, so a non-admin sees exactly today's browse-only experience —
  // no shell chrome, byte-identical output.
  let operatorAvailable = false;
  let surface: Surface = "browse";

  // setupNotice is non-null when this deployment's recordings substrate is not
  // proven (D-585). Where it renders depends on whether the archive can still be
  // READ, which is not the same question as whether setup completed:
  //
  //   blocking   the per-caller scan finds no mount and the catalog fails closed
  //              to empty, so the list underneath would be an error or a lie.
  //              The notice takes the browse slot.
  //   advisory   a restarted container that never re-ran setup. Publishing is
  //              refused, but every published recording still opens, so the list
  //              stays and the notice is a strip above it. Replacing it here
  //              would blank a working archive on every reboot.
  //
  // Either way an administrator keeps the operator surface — that is the one
  // place they can still act. Null (the normal case, and every case where the
  // check itself could not be made) leaves the shell exactly as it was.
  let setupNotice: SetupNoticeContent | null = null;

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
    // A non-admin deep-linking an admin surface falls back to browse (the
    // operator API would 403 anyway). Settings is gated by the same probe:
    // everything it touches is an ADMIN route.
    surface = next !== "browse" && !operatorAvailable ? "browse" : next;
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

    // Authoritative: probe the ADMIN-gated operator boundary (an operator that
    // answered -> show). Alongside it, ask the USER-level setup endpoint whether
    // this deployment can serve recordings at all — the two are independent, so
    // they go out together.
    try {
      const { operatorBasePath } = loadConfig();
      const [probe, health] = await Promise.all([
        probeOperatorAvailable(operatorBasePath),
        fetchSetupHealth(operatorBasePath),
      ]);
      operatorAvailable = probe.available;
      // Which setup message you get is decided by the SAME probe that decides
      // whether the operator surface exists — being able to read the ADMIN-gated
      // /status IS being an administrator, so there is no second notion of admin
      // here to drift from the first. The diagnosis comes from that same
      // response; a non-admin never had it and never will.
      setupNotice = buildSetupNotice({
        health,
        access: readRecordingsAccess(probe.body),
        isAdmin: probe.available,
        appUrl: shareableAppUrl(window.location.href),
      });
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
      setupNotice = null;
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
        Browse
      </button>
      <button
        type="button"
        class="cassini-shell-tab"
        aria-current={surface === "operator" ? "page" : undefined}
        on:click={() => selectSurface("operator")}
      >
        Operator
      </button>
      <button
        type="button"
        class="cassini-shell-tab"
        aria-current={surface === "settings" ? "page" : undefined}
        on:click={() => selectSurface("settings")}
      >
        Settings
      </button>
    </nav>

    {#if setupNotice && !setupNotice.blocking}
      <!-- Advisory: setup is unproven but the archive still reads, so this is a
           strip above the list, not a replacement for it. Kept beside the nav
           rather than inside the browse slot so it stays put while an
           administrator works on the operator surface — publishing is refused
           there too. -->
      <div class="cassini-shell-banner" data-theme={themeMode}>
        <div class="cassini-root" data-theme={themeMode}>
          <SetupNotice notice={setupNotice} />
        </div>
      </div>
    {/if}
    {#if setupNotice?.blocking}
      <!-- The browse slot, explaining itself. An administrator keeps the tab
           bar above and the operator surface below: nothing about a missing
           substrate stops them starting a recording or reading job history. -->
      <div
        class="cassini-shell-surface cassini-shell-scroll scroll-stable"
        class:cassini-shell-hidden={surface !== "browse"}
        data-theme={themeMode}
      >
        <div class="cassini-root" data-theme={themeMode}>
          <SetupNotice notice={setupNotice} />
        </div>
      </div>
    {:else}
      <!-- Browse stays mounted (preserves list/meeting/playback state) and is
           hidden while the operator surface is active; the operator mounts only
           when active so its SSE stream + polling don't run in the background. -->
      <div class="cassini-shell-surface" class:cassini-shell-hidden={surface !== "browse"}>
        <ViewerApp {ncMode} {dataProvider} />
      </div>
    {/if}
    {#if surface === "settings"}
      <!-- Same scroll/theming contract as the operator surface below. Mounted
           only while active, so its settings fetches happen on entry. -->
      <div class="cassini-shell-surface cassini-shell-scroll scroll-stable" data-theme={themeMode}>
        <div class="cassini-root" data-theme={themeMode}>
          <Settings />
        </div>
      </div>
    {/if}
    {#if surface === "operator"}
      <!-- Scroll pane (bounded flex child) is kept SEPARATE from the themed
           .cassini-root: putting .cassini-root's height:100% on the flex/scroll
           element fought the flex sizing. Here the outer div is a clean bounded
           scroller (flex:1 + overflow-y:auto); the operator was authored to
           scroll at the page level (min-h-screen), which the fixed-height shell
           removes. The inner .cassini-root + data-theme give it the daisyUI
           theme in the shadow build (tokens aren't on :host) and the NC-colour
           overrides, exactly like the viewer's browse surface. -->
      <div class="cassini-shell-surface cassini-shell-scroll scroll-stable" data-theme={themeMode}>
        <div class="cassini-root" data-theme={themeMode}>
          <Operator />
        </div>
      </div>
    {/if}
  </div>
{:else if setupNotice?.blocking}
  <!-- No operator surface to preserve, so the notice IS the app. It carries its
       own height and scroll: without the operator tab there is no .cassini-shell
       around it, and it is otherwise a direct child of the shadow :host (or #app
       in the standalone build), both of which are height:100%. -->
  <div class="cassini-setup-surface" data-theme={themeMode}>
    <div class="cassini-root" data-theme={themeMode}>
      <SetupNotice notice={setupNotice} />
    </div>
  </div>
{:else if setupNotice}
  <!-- Advisory, with no operator tab. .cassini-shell is reused verbatim: it is
       already the "fixed chrome above a full-height viewer" geometry the nav
       relies on, which is the same problem. -->
  <div class="cassini-shell">
    <div class="cassini-shell-banner" data-theme={themeMode}>
      <div class="cassini-root" data-theme={themeMode}>
        <SetupNotice notice={setupNotice} />
      </div>
    </div>
    <div class="cassini-shell-surface">
      <ViewerApp {ncMode} {dataProvider} />
    </div>
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
  /* The scroll container paints the surface background itself. The scrollbar
     gutter belongs to THIS element, not to the .cassini-root inside it, so with
     no background here the gutter is transparent and Nextcloud's page wallpaper
     showed through beside the scrollbar. --color-main-background is what NC maps
     base-200 to, which is the operator's own page background (bg-base-200), so
     the gutter blends into the surface. [data-theme] is on the element for the
     same reason as the nav: it sits outside .cassini-root, so the daisyUI token
     would otherwise be undefined here and fall through to the light default. */
  .cassini-shell-scroll {
    overflow-y: auto;
    background: var(--color-main-background, var(--color-base-200, #f3f4f6));
  }

  .cassini-shell-hidden {
    display: none;
  }

  /* The advisory strip: fixed chrome, like the nav, so the viewer below keeps a
     bounded flex height. flex:none is what stops it stretching or being squeezed
     when the meeting list grows. */
  .cassini-shell-banner {
    flex: none;
    background: var(--color-main-background, var(--color-base-100, #ffffff));
  }

  /* The notice standing in for the whole app (no operator tab). Same bounded
     scroller + background as .cassini-shell-scroll, but height:100% instead of
     flex:1 because there is no .cassini-shell flex column above it here. */
  .cassini-setup-surface {
    height: 100%;
    min-height: 100%;
    overflow-y: auto;
    background: var(--color-main-background, var(--color-base-200, #f3f4f6));
  }
</style>
