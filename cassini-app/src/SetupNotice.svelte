<script lang="ts">
  import { createEventDispatcher } from "svelte";
  import { Info, TriangleAlert } from "@lucide/svelte";
  import type { SetupNotice } from "./operator/setupHealth";

  export let notice: SetupNotice;
  const dispatch = createEventDispatcher<{ navigate: "setup" }>();

  // Keep the browser action visible. Server diagnostics and manual recovery
  // are optional, for administrators investigating a setup failure.
  $: technicalSteps = notice.steps.filter((step) => !step.action);
</script>

<div class={notice.blocking ? "grid min-h-full place-items-center p-4 sm:p-6" : "px-3 py-2"}>
  <section
    class="w-full rounded-box border {notice.blocking ? 'max-w-2xl shadow-sm' : ''} {notice.tone === 'info' ? 'border-base-300 bg-base-100' : 'border-warning/40 bg-base-100'}"
    role="status"
  >
    <div class="flex items-start gap-3 p-4 sm:p-5">
      {#if notice.tone === "info"}
        <Info size={22} class="mt-0.5 shrink-0 text-primary" aria-hidden="true" />
      {:else}
        <TriangleAlert size={22} class="mt-0.5 shrink-0 text-warning" aria-hidden="true" />
      {/if}
      <div class="flex min-w-0 flex-1 flex-col gap-3">
        <div class="flex flex-col gap-1">
          <h2 class="text-base font-semibold">{notice.title}</h2>
          <p class="max-w-prose text-sm text-base-content/80">{notice.summary}</p>
        </div>

        {#if notice.actionLabel}
          <button
            class="btn btn-sm btn-primary w-fit"
            type="button"
            on:click={() => dispatch("navigate", "setup")}
          >
            {notice.actionLabel}
          </button>
        {/if}

        {#if notice.shareUrl}
          <div class="flex flex-col gap-1 text-sm">
            <p>{notice.shareLabel}</p>
            <a class="link link-primary break-all" href={notice.shareUrl}>{notice.shareUrl}</a>
          </div>
        {/if}

        {#if notice.detail || technicalSteps.length > 0 || notice.reference}
          <details class="text-sm text-base-content/70">
            <summary class="w-fit cursor-pointer">Technical details</summary>
            <div class="mt-3 flex flex-col gap-3">
              {#if notice.detail}
                <p class="rounded-box bg-base-200 p-3 font-mono text-xs break-words">{notice.detail}</p>
              {/if}
              {#if technicalSteps.length > 0}
                <p class="font-medium">Manual recovery for server administrators</p>
                <ol class="flex list-inside list-decimal flex-col gap-3">
                  {#each technicalSteps as step (step.label)}
                    <li>
                      {step.label}
                      {#if step.commands.length > 0}
                        <pre class="mt-2 overflow-x-auto rounded-box bg-base-200 p-3 font-mono text-xs leading-relaxed">{step.commands.join("\n")}</pre>
                      {/if}
                    </li>
                  {/each}
                </ol>
              {/if}
              {#if notice.note}
                <p class="text-xs">{notice.note}</p>
              {/if}
              {#if notice.reference}
                <p class="text-xs">{notice.reference}</p>
              {/if}
            </div>
          </details>
        {/if}
      </div>
    </div>
  </section>
</div>
