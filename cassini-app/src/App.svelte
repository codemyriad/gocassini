<script lang="ts">
  import { onDestroy, onMount } from "svelte";
  import ViewerApp from "cassini-viewer/App.svelte";
  import { StaticCatalogProvider } from "cassini-viewer/dataProvider";
  import Operator from "./Operator.svelte";
  import { loadConfig } from "./operator/config";
  import { isLikelyAdminHint, probeOperatorAvailable } from "./operator/adminProbe";
  import { readSurface, surfaceHash, type Surface } from "./surfaceRouting";

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
    window.history.pushState({}, "", locationWithHash(surfaceHash(next)));
  }

  function handlePopState(): void {
    applySurfaceFromLocation();
  }

  onMount(async () => {
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
      // 403 is the expected "not an admin" answer — stay quiet. Anything else (a
      // network failure or an unexpected status) shouldn't silently hide the
      // operator surface with no trace, so surface it.
      if (!probe.available && probe.status !== 403) {
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
    <nav class="cassini-shell-nav" aria-label="Cassini surfaces">
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

    <!-- Browse stays mounted (preserves list/meeting/playback state) and is
         hidden while the operator surface is active; the operator mounts only
         when active so its SSE stream + polling don't run in the background. -->
    <div class="cassini-shell-surface" class:cassini-shell-hidden={surface === "operator"}>
      <ViewerApp {ncMode} {dataProvider} />
    </div>
    {#if surface === "operator"}
      <div class="cassini-shell-surface">
        <Operator />
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
    min-height: 100%;
  }

  .cassini-shell-nav {
    display: flex;
    gap: 0.25rem;
    padding: 0.5rem 0.75rem;
    border-bottom: 1px solid var(--color-base-300, #e5e7eb);
    background: var(--color-base-100, #ffffff);
  }

  .cassini-shell-tab {
    appearance: none;
    border: 0;
    background: transparent;
    cursor: pointer;
    padding: 0.375rem 0.875rem;
    border-radius: 0.5rem;
    font: inherit;
    font-weight: 600;
    color: var(--color-base-content, #1f2937);
    opacity: 0.7;
  }

  .cassini-shell-tab:hover {
    background: var(--color-base-200, #f3f4f6);
    opacity: 1;
  }

  .cassini-shell-tab[aria-current="page"] {
    background: var(--color-primary, #2563eb);
    color: var(--color-primary-content, #ffffff);
    opacity: 1;
  }

  .cassini-shell-surface {
    flex: 1 1 auto;
    min-height: 0;
  }

  .cassini-shell-hidden {
    display: none;
  }
</style>
