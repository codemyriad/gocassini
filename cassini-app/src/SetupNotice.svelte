<script lang="ts">
  import { TriangleAlert } from "@lucide/svelte";
  import type { SetupNotice } from "./operator/setupHealth";

  // What the Cassini shell shows when the app is not set up. It renders a
  // decision it did not make: every word, and whether this is blocking, comes
  // from buildSetupNotice (setupHealth.ts), which is where the copy is tested.
  // What belongs here is presentation only — an administrator and everyone else
  // get different content through the same component, never a different one.
  //
  // Two layouts, from notice.blocking:
  //
  //   blocking   the archive cannot be read, so this stands in for the meeting
  //              list: a centred card with room for the commands.
  //   advisory   the archive reads fine and the list is still below, so this is
  //              a strip. The instructions are there, behind a disclosure, so
  //              the band stays one line tall until someone wants them.
  export let notice: SetupNotice;
</script>

{#if notice.blocking}
  <div class="grid min-h-full place-items-center p-4 sm:p-6">
    <section class="card w-full max-w-2xl border border-base-300 bg-base-100 shadow-sm" role="status">
      <div class="card-body gap-5">
        <header class="flex items-start gap-3">
          <TriangleAlert size={22} class="mt-0.5 shrink-0 text-warning" aria-hidden="true" />
          <div class="flex flex-col gap-2">
            <h2 class="text-lg font-bold">{notice.title}</h2>
            <p class="text-base-content/80">{notice.summary}</p>
          </div>
        </header>

        {#if notice.detail}
          <!-- The operator's own sentence, verbatim, so this panel and the
               container log read the same. Administrators only. -->
          <p class="rounded-box bg-base-200 p-3 font-mono text-xs break-words text-base-content/70">
            {notice.detail}
          </p>
        {/if}

        {#if notice.steps.length > 0}
          <ol class="flex list-none flex-col gap-4">
            {#each notice.steps as step, index (step.label)}
              <li class="flex flex-col gap-2">
                <p class="text-sm font-medium">
                  <span class="text-base-content/50">{index + 1}.</span>
                  {step.label}
                </p>
                {#if step.commands.length > 0}
                  <pre
                    class="m-0 overflow-x-auto rounded-box bg-base-200 p-3 font-mono text-xs leading-relaxed">{step.commands.join(
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

        {#if notice.shareUrl}
          <div class="flex flex-col gap-2 border-t border-base-300 pt-4">
            <p class="text-sm">{notice.shareLabel}</p>
            <a class="link link-primary text-sm break-all" href={notice.shareUrl}>{notice.shareUrl}</a>
          </div>
        {/if}

        {#if notice.reference}
          <p class="text-xs text-base-content/60">{notice.reference}</p>
        {/if}
      </div>
    </section>
  </div>
{:else}
  <div class="px-3 py-2">
    <section class="alert alert-warning items-start gap-3 py-2" role="status">
      <TriangleAlert size={16} class="mt-0.5 shrink-0" aria-hidden="true" />
      <div class="flex min-w-0 flex-col gap-1">
        <p class="text-sm">
          <span class="font-semibold">{notice.title}.</span>
          {notice.summary}
        </p>

        {#if notice.steps.length > 0 || notice.shareUrl}
          <details class="text-sm">
            <summary class="cursor-pointer font-medium">
              {notice.steps.length > 0 ? "How to fix it" : "What to do"}
            </summary>
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
              {#if notice.shareUrl}
                <div class="flex flex-col gap-1">
                  <p class="text-sm">{notice.shareLabel}</p>
                  <a class="link text-sm break-all" href={notice.shareUrl}>{notice.shareUrl}</a>
                </div>
              {/if}
              {#if notice.reference}
                <p class="text-xs opacity-70">{notice.reference}</p>
              {/if}
            </div>
          </details>
        {/if}
      </div>
    </section>
  </div>
{/if}
