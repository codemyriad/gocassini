<script lang="ts">
  import { createEventDispatcher } from "svelte";
  import { ChevronDown, Info, TriangleAlert } from "@lucide/svelte";
  import { LL, operatorPlace } from "cassini-viewer/i18n";
  import type { SetupNotice, SetupNoticeTone } from "./operator/setupHealth";

  // What the Cassini shell shows when recordings cannot be saved. It renders a
  // decision it did not make: every word and the tone it is drawn in come from
  // buildSetupNotice (setupHealth.ts), which is
  // where the copy is tested. What belongs here is presentation only — an
  // administrator and everyone else get different content through the same
  // component, never a different one.
  //
  // One layout: a strip above the meeting list, which stays readable whatever
  // the check says (D-849). The band stays one row tall until someone opens
  // the details (#500).
  //
  // Inside (D-759): the consequence, the cause, two
  // buttons, and every technical thing behind one disclosure. The enum name,
  // the `occ` recipe and the address of the full report are all still here —
  // one level down, after a plain account of what is wrong, rather than as the
  // first thing an administrator reads.
  export let notice: SetupNotice;

  // The tone is the notice's, not the component's: a warning triangle over "a
  // check has not run yet" is the shell shouting about a state that is ordinary
  // after every container restart. The default is the loud one, so a caller
  // that says nothing gets the safe answer rather than a quiet failure.
  export let tone: SetupNoticeTone = "warning";

  // True while the re-check the Try again button asked for is still running.
  // The shell owns that request — it is the one holding the operator client —
  // so it also owns saying when it is finished.
  export let busy: boolean = false;

  // A step carrying `action: "settings"` is something to PRESS, not something
  // to find. Operator › Settings is where "Who can see recordings" and the
  // service-account controls live (D-757), and "open Operator › Settings" as
  // prose is a navigation the reader has to perform on the app's behalf.
  //
  // The shell owns which surface is showing, so this asks rather than acts. It
  // owns the operator client too, so Try again asks as well: the button
  // re-runs the same check a restart runs, which is a request to the operator
  // and not something a presentation component should be making.
  const dispatch = createEventDispatcher<{ navigate: "settings"; retry: void }>();

  // Open state of the disclosure. One control opens it: there used to be a
  // "Show details" button AND a disclosure triangle under it, two rows of
  // chrome for the same thing.
  let detailsOpen = false;
  const detailsId = `setup-notice-details-${Math.random().toString(36).slice(2, 8)}`;

  // Whether there is a diagnosis behind the disclosure — which is also what
  // says which audience this is drawing for. Only an administrator's notice
  // carries one; everyone else gets the consequence and the link to hand on,
  // and no buttons: Try again would re-run a check whose answer they are not
  // shown and could not act on.
  $: hasDetails = Boolean(
    notice.detail || notice.note || notice.reference || notice.steps.length > 0,
  );
</script>

<div class="px-3 py-2">
  <section
    class="alert items-start gap-3 py-2 grid-cols-[auto_minmax(0,1fr)] {tone === 'warning' ? 'alert-warning' : ''}"
    role="status"
  >
    {#if tone === "warning"}
      <TriangleAlert size={16} class="mt-0.5 shrink-0" aria-hidden="true" />
    {:else}
      <Info size={16} class="mt-0.5 shrink-0 opacity-70" aria-hidden="true" />
    {/if}
    <div class="flex w-full min-w-0 flex-col gap-1">
      <!-- One row while closed: the account on the left, the two actions on
           the right, wrapping under it only when the strip is too narrow. -->
      <div class="flex flex-wrap items-center gap-x-3 gap-y-1">
        <p class="min-w-0 flex-1 basis-80 text-sm">
          <span class="font-semibold">{notice.title}.</span>
          {notice.summary}
          {#if notice.cause}{notice.cause}{/if}
        </p>
        {#if hasDetails}
          <div class="flex shrink-0 gap-2">
            <button class="btn btn-xs" type="button" disabled={busy} on:click={() => dispatch("retry")}>
              {busy ? $LL.app.shell.setup_notice.checking() : $LL.common.action.try_again()}
            </button>
            <button
              class="btn btn-xs btn-ghost"
              type="button"
              aria-expanded={detailsOpen}
              aria-controls={detailsId}
              on:click={() => (detailsOpen = !detailsOpen)}
            >
              {$LL.app.shell.setup_notice.show_details()}
              <ChevronDown size={12} class="transition-transform {detailsOpen ? 'rotate-180' : ''}" aria-hidden="true" />
            </button>
          </div>
        {/if}
      </div>

      {#if hasDetails && detailsOpen}
        <div id={detailsId} class="mt-1 flex flex-col gap-3 text-sm">
          {#if notice.detail}
            <p class="font-mono text-xs break-words opacity-80">{notice.detail}</p>
          {/if}
          {#each notice.steps as step, index (step.label)}
            <div class="flex flex-col gap-1">
              <p class="text-sm">
                <span class="opacity-60">{index + 1}.</span>
                {step.label}
              </p>
              {#if step.action === "settings"}
                <button
                  class="btn btn-xs w-fit"
                  type="button"
                  on:click={() => dispatch("navigate", "settings")}
                >
                  {$LL.app.shell.setup_notice.open_publish_pipeline({ place: operatorPlace("publish_pipeline") })}
                </button>
              {/if}
              {#if step.commands.length > 0}
                <pre
                  class="m-0 overflow-x-auto rounded-box bg-base-200 p-2 font-mono text-xs leading-relaxed text-base-content">{step.commands.join(
                    "\n",
                  )}</pre>
              {/if}
            </div>
          {/each}
          {#if notice.note}
            <p class="text-xs opacity-70">{notice.note}</p>
          {/if}
          {#if notice.reference}
            <p class="text-xs opacity-70">{notice.reference}</p>
          {/if}
        </div>
      {/if}

      {#if notice.shareUrl}
        <!-- The label and the address on one line; the address is cut short to
             fit, and the whole of it is still what the link copies or opens. -->
        <div class="flex min-w-0 items-baseline gap-x-2 text-sm">
          <span>{notice.shareLabel}</span>
          <a class="link min-w-24 flex-1 truncate" href={notice.shareUrl} title={notice.shareUrl}>{notice.shareUrl}</a>
        </div>
      {/if}
    </div>
  </section>
</div>
