<script lang="ts">
  import { onDestroy, onMount } from "svelte";
  import ViewerApp from "cassini-viewer/App.svelte";
  import { AppDataProvider } from "./appDataProvider";
  import GenerateCard from "./GenerateCard.svelte";
  import NeedsSetupCard from "./NeedsSetupCard.svelte";
  import Operator from "./Operator.svelte";
  import SetupNotice from "./SetupNotice.svelte";
  import { OperatorClient } from "./operator/client";
  import { loadConfig } from "./operator/config";
  import { isLikelyAdminHint, probeOperatorAvailable } from "./operator/adminProbe";
  import {
    buildFeatureNotice,
    buildSetupNotice,
    fetchSetupHealth,
    readRecordingsAccess,
    shareableAppUrl,
    type SetupFeatures,
    type SetupNotice as SetupNoticeContent,
  } from "./operator/setupHealth";
  import { applySurface, readSurface, type OperatorPanel, type Surface } from "./surfaceRouting";

  // The Cassini in-Nextcloud shell (D-420). It hosts role-gated surfaces fed
  // through the DataProvider seam: everyone gets "browse" (cassini-viewer's App
  // = MeetingList + MeetingView); admins additionally get the "operator"
  // surface (recording control). V3 adds the top nav + the operator surface.
  //
  // There are exactly TWO surfaces, and configuration is not one of them
  // (D-723): it is a left nav inside Operator, so there is one admin boundary
  // to probe and one place an administrator looks for a knob.
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

  // The shell's provider is the static one plus the context bundle (D-626):
  // here, and only here, there is an operator behind the published archive that
  // can assemble one. A standalone export gets StaticCatalogProvider, which
  // cannot, and its browse surface offers no Prepare at all.
  const dataProvider = new AppDataProvider();

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

  // What this deployment's AI configuration allows (D-722), from the same
  // USER-level /setup call. Null until it answers, and null forever on an
  // operator too old to say — a third state, not a default: the app says
  // nothing at all rather than telling a working deployment it is unconfigured.
  //
  // It is fetched HERE, once, because it is a fact about the deployment rather
  // than about anything the browse surface is showing, and because the only
  // route that carries it is the one the shell already calls at mount.
  let setupFeatures: SetupFeatures | null = null;

  // The unconfigured state the browse surface can meet: a selection of meetings
  // on a deployment with no endpoint to ask. It rides into the viewing layer
  // through a slot rather than a prop because the sentence, the admin/non-admin
  // split and the deep link are all shell knowledge — the viewer has no idea
  // there is an operator surface, and a standalone export has no operator at
  // all.
  $: insightsNotice = buildFeatureNotice({
    features: setupFeatures,
    feature: "insights",
    // The same probe that decides whether there is an operator surface at all.
    // There is no second notion of admin here to drift from the first.
    isAdmin: operatorAvailable,
  });

  // The configured state, from the SAME field (D-700). insightsNotice is
  // non-null exactly when `insights` is false, so these two are mutually
  // exclusive by construction rather than by two conditions kept in step: the
  // readiness card OR the Generate card, and — while /setup has not answered,
  // or on a build with no operator to ask — neither. A standalone export must
  // not read absence as "not configured", the same three-state rule the
  // catalog's hasSummary follows.
  $: insightsReady = setupFeatures?.insights === true;

  // The operator API client the Generate card lists templates with, or null for
  // anyone the probe denied. `operator/settings/workflows` is ADMIN at the
  // proxy, so a non-admin's request for the template registry would 403 —
  // null is that fact, and the card offers the deployment's configured template
  // instead of a picker that fails when opened.
  //
  // Built from the probe RESULT and never from the optimistic admin hint: the
  // hint exists to avoid a tab flashing in, and the cost of being wrong here is
  // a control that 403s.
  let operatorClient: OperatorClient | null = null;

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
    // A non-admin deep-linking the operator surface falls back to browse (the
    // operator API would 403 anyway). Its settings panels are gated by the same
    // probe: everything they touch is an ADMIN route.
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
    const previous = surface;
    surface = next;
    // Fragment-only pushState — same mechanism the viewer uses; gives history /
    // back-forward + deep links without a pathname router (see surfaceRouting).
    // applySurface preserves the viewer's meeting/tx/t so switching surfaces
    // doesn't drop a meeting deep-link.
    window.history.pushState({}, "", locationWithHash(applySurface(window.location.hash, next)));
    refreshFeaturesOnLeavingOperator(previous);
  }

  function handlePopState(): void {
    const previous = surface;
    applySurfaceFromLocation();
    refreshFeaturesOnLeavingOperator(previous);
  }

  // Coming back from the operator surface is the return leg of the trip
  // NeedsSetupCard's own link sends an administrator on: browse -> "Open AI
  // providers" -> configure an endpoint -> Back. setupFeatures is otherwise
  // read once at mount and never again, so without this the card that sent them
  // still says "No AI endpoint is available" and still offers the link to the
  // panel they have just fixed — only a full page reload clears it. That is the
  // same staleness `cache: "no-store"` keeps out of the HTTP layer
  // (setupHealth.ts), one level up in app state (D-722).
  //
  // Deliberately only the features: setupNotice is derived from the ADMIN probe
  // as well, and re-running that on every surface switch would be a second,
  // heavier question asked for a fact that cannot change without a restart.
  function refreshFeaturesOnLeavingOperator(previous: Surface): void {
    if (previous !== "operator" || surface === "operator") {
      return;
    }
    void refreshSetupFeatures();
  }

  async function refreshSetupFeatures(): Promise<void> {
    try {
      const { operatorBasePath } = loadConfig();
      const health = await fetchSetupHealth(operatorBasePath);
      // Only an answer that arrived may change what the app claims: null is
      // "nobody said" — a failed re-check, or an operator too old to say — and
      // letting it through would retract what the mount-time call established
      // and accuse a working deployment of being unconfigured.
      if (health) {
        setupFeatures = health.features;
      }
    } catch (error) {
      // Degrade, but not silently — the same rule the mount path follows.
      console.warn("Cassini: the setup re-check failed.", error);
    }
  }

  // Following an unconfigured state's link to the panel that fixes it. The card
  // built the address from the CURRENT fragment, so the viewer's meeting/tx/t
  // survive the trip and the back button returns to exactly the meeting that
  // was open.
  function handleOpenPanel(event: CustomEvent<{ panel: OperatorPanel; href: string }>): void {
    // The card only offers the link to an administrator; this is the second
    // guard, because the cost of being wrong is a surface whose every request
    // 403s at the proxy.
    if (!operatorAvailable) {
      return;
    }
    window.history.pushState({}, "", event.detail.href);
    // pushState notifies nobody, and the surface, the operator's panel nav and
    // the viewer each read the fragment through popstate. Announcing it once
    // makes a deep link behave like a navigation, instead of three independent
    // updates that can disagree about where we are.
    window.dispatchEvent(new PopStateEvent("popstate"));
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
      operatorClient = probe.available ? new OperatorClient(operatorBasePath) : null;
      setupFeatures = health?.features ?? null;
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
      operatorClient = null;
      setupNotice = null;
      setupFeatures = null;
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
        <ViewerApp {ncMode} {dataProvider}>
          <NeedsSetupCard slot="prepare-readiness" notice={insightsNotice} on:open={handleOpenPanel} />
          <!-- Its opposite, driven by the same bit (D-700): the readiness card
               says a question cannot be asked here, this one asks it. The Prepare
               panel hands down the meetings it is describing; whether there is an
               endpoint to ask, and whether this reader may pick a template, are
               the shell's to know and neither is a fact the viewing layer has. -->
          <svelte:fragment slot="prepare-generate" let:entries>
            {#if insightsReady}
              <GenerateCard {entries} {operatorClient} on:open={handleOpenPanel} />
            {/if}
          </svelte:fragment>
        </ViewerApp>
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
      <ViewerApp {ncMode} {dataProvider}>
        <NeedsSetupCard slot="prepare-readiness" notice={insightsNotice} on:open={handleOpenPanel} />
        <svelte:fragment slot="prepare-generate" let:entries>
          {#if insightsReady}
            <GenerateCard {entries} {operatorClient} on:open={handleOpenPanel} />
          {/if}
        </svelte:fragment>
      </ViewerApp>
    </div>
  </div>
{:else}
  <ViewerApp {ncMode} {dataProvider}>
    <NeedsSetupCard slot="prepare-readiness" notice={insightsNotice} on:open={handleOpenPanel} />
    <svelte:fragment slot="prepare-generate" let:entries>
      {#if insightsReady}
        <GenerateCard {entries} {operatorClient} on:open={handleOpenPanel} />
      {/if}
    </svelte:fragment>
  </ViewerApp>
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
