<script lang="ts">
  import { createEventDispatcher } from "svelte";
  import { TriangleAlert } from "@lucide/svelte";
  import type { FeatureNotice } from "./operator/setupHealth";
  import { applyPanel, applySurface, type OperatorPanel } from "./surfaceRouting";

  // The unconfigured state, in one component (D-722). Every place the product
  // has to say "this deployment cannot do that yet" says it here, so the four
  // gaps the design prototype models cannot end up describing themselves four
  // different ways.
  //
  // It renders a decision it did not make: buildFeatureNotice (setupHealth.ts)
  // owns every word, including whether there is a link at all — which is the
  // part that matters, because the panel behind that link is ADMIN at the proxy
  // and its PUT would 403. A non-admin gets the fact and who can act on it,
  // never a control that fails when pressed.
  //
  // A null notice renders NOTHING, and that is load-bearing rather than
  // defensive: null covers both "configured" and "nobody answered", and the
  // standalone export — which has no operator to ask — must stay silent instead
  // of accusing a working deployment of being unconfigured.
  export let notice: FeatureNotice | null = null;

  const dispatch = createEventDispatcher<{ open: { panel: OperatorPanel; href: string } }>();

  // Route-preserving, and rebuilt from the CURRENT fragment at the moment the
  // link is followed: the viewing layer rewrites the whole fragment on every
  // navigation, so a URL frozen when this card rendered would carry a meeting
  // that has since been closed — or drop the one that is open now (the bug
  // applySurface and appendKeepingTimeLast exist to prevent).
  //
  // The href attribute is a render-time snapshot of the same call, refreshed
  // when the notice changes and not on navigation: the viewer moves by
  // history.pushState, which fires neither popstate nor hashchange, so there is
  // no event to recompute it on. That snapshot is only what a middle-click or a
  // "copy link address" gets; every ordinary click and keyboard activation goes
  // through handleOpen, which asks panelUrl again.
  function panelUrl(panel: OperatorPanel): string {
    const url = new URL(window.location.href);
    url.hash = applyPanel(applySurface(window.location.hash, "operator"), panel).replace(/^#/, "");
    return url.toString();
  }

  function handleOpen(event: MouseEvent) {
    if (!notice?.panel) {
      return;
    }
    // A modified click is a request for a new tab or window, and this IS a real
    // link — so let the browser have it rather than swallowing it into an
    // in-page surface switch.
    if (event.metaKey || event.ctrlKey || event.shiftKey || event.altKey) {
      return;
    }
    event.preventDefault();
    dispatch("open", { panel: notice.panel, href: panelUrl(notice.panel) });
  }
</script>

{#if notice}
  <section class="rounded-box border border-base-300 bg-base-200 p-3" role="status">
    <div class="flex items-start gap-2">
      <TriangleAlert size={16} class="mt-0.5 shrink-0 text-warning" aria-hidden="true" />
      <div class="flex min-w-0 flex-col gap-1.5">
        <p class="text-sm font-medium">{notice.title}</p>
        <p class="text-xs leading-relaxed text-base-content/70">{notice.summary}</p>
        {#if notice.panel}
          <!-- An anchor, not a button: it has a real address, so it can be
               copied and opened in a tab like any other link. The click handler
               only takes over the plain case, where the shell can switch
               surfaces without reloading the page. -->
          <a class="link link-primary text-xs" href={panelUrl(notice.panel)} on:click={handleOpen}>
            {notice.actionLabel}
          </a>
        {/if}
      </div>
    </div>
  </section>
{/if}
