<script lang="ts">
  import { createEventDispatcher } from "svelte";
  import { Info, TriangleAlert } from "@lucide/svelte";
  import type { SetupNotice, SetupNoticeTone } from "./operator/setupHealth";

  // What the Cassini shell shows when recordings cannot be saved. It renders a
  // decision it did not make: every word, whether this is blocking, and which
  // tone it is drawn in come from buildSetupNotice (setupHealth.ts), which is
  // where the copy is tested. What belongs here is presentation only — an
  // administrator and everyone else get different content through the same
  // component, never a different one.
  //
  // Two layouts, from notice.blocking:
  //
  //   blocking   the archive cannot be read, so this stands in for the meeting
  //              list: a centred card.
  //   advisory   the archive reads fine and the list is still below, so this is
  //              a strip. The band stays one line tall until someone wants more.
  //
  // Both have the same shape inside (D-759): the consequence, the cause, two
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

  // Open state of the disclosure, so the "Show details" button and the
  // <details> element cannot disagree about whether it is open.
  let detailsOpen = false;

  // Whether there is a diagnosis behind the disclosure — which is also what
  // says which audience this is drawing for. Only an administrator's notice
  // carries one; everyone else gets the consequence and the link to hand on,
  // and no buttons: Try again would re-run a check whose answer they are not
  // shown and could not act on.
  $: hasDetails = Boolean(
    notice.detail || notice.note || notice.reference || notice.steps.length > 0,
  );
</script>

{#if notice.blocking}
  <div class="grid min-h-full place-items-center p-4 sm:p-6">
    <section class="card w-full max-w-2xl border border-base-300 bg-base-100 shadow-sm" role="status">
      <div class="card-body gap-4">
        <header class="flex items-start gap-3">
          {#if tone === "warning"}
            <TriangleAlert size={22} class="mt-0.5 shrink-0 text-warning" aria-hidden="true" />
          {:else}
            <Info size={22} class="mt-0.5 shrink-0 text-base-content/60" aria-hidden="true" />
          {/if}
          <div class="flex flex-col gap-2">
            <h2 class="text-lg font-bold">{notice.title}</h2>
            <p class="text-base-content/80">{notice.summary}</p>
            {#if notice.cause}
              <p class="text-base-content/80">{notice.cause}</p>
            {/if}
          </div>
        </header>

        {#if hasDetails}
          <div class="flex flex-wrap gap-2">
            <button class="btn btn-sm btn-primary" type="button" disabled={busy} on:click={() => dispatch("retry")}>
              {busy ? "Checking…" : "Try again"}
            </button>
            <button
              class="btn btn-sm btn-ghost"
              type="button"
              aria-expanded={detailsOpen}
              on:click={() => (detailsOpen = !detailsOpen)}
            >
              Show details
            </button>
          </div>

          <details class="rounded-box bg-base-200 p-3 text-sm" bind:open={detailsOpen}>
            <summary class="cursor-pointer font-medium">Details for administrators</summary>
            <div class="mt-3 flex flex-col gap-4">
              {#if notice.detail}
                <!-- The operator's own sentence, verbatim, so this panel and the
                     container log read the same. -->
                <p class="font-mono text-xs break-words text-base-content/70">{notice.detail}</p>
              {/if}

              {#if notice.steps.length > 0}
                <ol class="flex list-none flex-col gap-3">
                  {#each notice.steps as step, index (step.label)}
                    <li class="flex flex-col gap-2">
                      <p class="text-sm font-medium">
                        <span class="text-base-content/50">{index + 1}.</span>
                        {step.label}
                      </p>
                      {#if step.action === "settings"}
                        <button
                          class="btn btn-sm w-fit"
                          type="button"
                          on:click={() => dispatch("navigate", "settings")}
                        >
                          Open Operator › Settings
                        </button>
                      {/if}
                      {#if step.commands.length > 0}
                        <pre
                          class="m-0 overflow-x-auto rounded-box bg-base-300 p-3 font-mono text-xs leading-relaxed">{step.commands.join(
                            "\n",
                          )}</pre>
                      {/if}
                    </li>
                  {/each}
                </ol>
              {/if}

              {#if notice.note}
                <p class="text-xs text-base-content/60">{notice.note}</p>
              {/if}
              {#if notice.reference}
                <p class="text-xs text-base-content/60">{notice.reference}</p>
              {/if}
            </div>
          </details>
        {/if}

        {#if notice.shareUrl}
          <div class="flex flex-col gap-2 border-t border-base-300 pt-4">
            <p class="text-sm">{notice.shareLabel}</p>
            <a class="link link-primary text-sm break-all" href={notice.shareUrl}>{notice.shareUrl}</a>
          </div>
        {/if}
      </div>
    </section>
  </div>
{:else}
  <div class="px-3 py-2">
    <section
      class="alert items-start gap-3 py-2 {tone === 'warning' ? 'alert-warning' : ''}"
      role="status"
    >
      {#if tone === "warning"}
        <TriangleAlert size={16} class="mt-0.5 shrink-0" aria-hidden="true" />
      {:else}
        <Info size={16} class="mt-0.5 shrink-0 opacity-70" aria-hidden="true" />
      {/if}
      <div class="flex min-w-0 flex-col gap-1">
        <p class="text-sm">
          <span class="font-semibold">{notice.title}.</span>
          {notice.summary}
          {#if notice.cause}{notice.cause}{/if}
        </p>

        {#if hasDetails}
          <div class="flex flex-wrap gap-2 py-1">
            <button class="btn btn-xs" type="button" disabled={busy} on:click={() => dispatch("retry")}>
              {busy ? "Checking…" : "Try again"}
            </button>
            <button
              class="btn btn-xs btn-ghost"
              type="button"
              aria-expanded={detailsOpen}
              on:click={() => (detailsOpen = !detailsOpen)}
            >
              Show details
            </button>
          </div>

          <details class="text-sm" bind:open={detailsOpen}>
            <summary class="cursor-pointer font-medium">Details for administrators</summary>
            <div class="mt-2 flex flex-col gap-3">
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
                      Open Operator › Settings
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
          </details>
        {/if}

        {#if notice.shareUrl}
          <div class="flex flex-col gap-1">
            <p class="text-sm">{notice.shareLabel}</p>
            <a class="link text-sm break-all" href={notice.shareUrl}>{notice.shareUrl}</a>
          </div>
        {/if}
      </div>
    </section>
  </div>
{/if}
