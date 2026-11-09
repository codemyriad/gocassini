<script lang="ts">
  // The Doctor's section for the recordedAtUtc file migration. Cassini found
  // past recordings whose delivered file lacks the recording time; writing it
  // in is a new Nextcloud version of each file and, with the Activity app, a
  // "file changed" entry for everyone it is shared with — so it only happens
  // when an administrator presses the button here, and can be paused here.
  import { onDestroy, onMount } from "svelte";
  import { CircleAlert, CircleCheck, Clock, Info, Pause, Play, TriangleAlert } from "@lucide/svelte";
  import type { OperatorClient } from "./operator/client";
  import { migrationActive, migrationButton, migrationCounting, migrationSummary, migrationVisible, notificationFacts, type RecordedAtMigration } from "./operator/recordedAtMigration";
  import { toneClasses } from "./operator/readiness";

  export let operatorClient: Pick<OperatorClient, "getRecordedAtMigration" | "startRecordedAtMigration" | "pauseRecordedAtMigration">;
  // How often the section re-reads while files are being written, and while
  // the first count of past recordings has not finished.
  export let pollMs = 10000;
  export let countingPollMs = 30000;
  // The timer the re-reads run on; tests fire it themselves.
  export let timers: { set: (run: () => void, ms: number) => unknown; clear: (handle: unknown) => void } = {
    set: (run, ms) => setTimeout(run, ms),
    clear: handle => clearTimeout(handle as ReturnType<typeof setTimeout>),
  };

  let migration: RecordedAtMigration | null = null;
  let busy = false;
  let error = "";
  let timer: unknown;
  let destroyed = false;
  // Bumped by every action, so a read that started before it cannot put the
  // state it answered (or its failure) over the action's newer one. No read
  // starts while an action is in flight: its answer could predate the action.
  let generation = 0;

  async function load(): Promise<void> {
    if (busy) return;
    const started = generation;
    try {
      const read = await operatorClient.getRecordedAtMigration();
      if (started !== generation) return;
      migration = read;
      error = "";
    } catch (cause) {
      if (started !== generation) return;
      // Said only once something was shown: a section that never loaded has
      // nothing to correct, and the checks above already report an operator
      // that does not answer.
      if (migration) error = `Couldn’t refresh: ${cause instanceof Error ? cause.message : String(cause)}`;
    }
    schedule();
  }

  function schedule(): void {
    timers.clear(timer);
    if (destroyed || !migration) return;
    if (migrationActive(migration)) timer = timers.set(load, pollMs);
    else if (migrationCounting(migration)) timer = timers.set(load, countingPollMs);
  }

  async function act(action: "start" | "pause"): Promise<void> {
    timers.clear(timer);
    busy = true;
    error = "";
    generation++;
    try {
      migration = action === "start" ? await operatorClient.startRecordedAtMigration() : await operatorClient.pauseRecordedAtMigration();
    } catch (cause) {
      error = cause instanceof Error ? cause.message : String(cause);
    } finally {
      busy = false;
    }
    schedule();
  }

  onMount(load);
  onDestroy(() => { destroyed = true; timers.clear(timer); });

  $: button = migration ? migrationButton(migration) : null;
  $: facts = notificationFacts(migration?.notifications);
  // Read before deciding, and out of the way once decided.
  $: noticesOpen = migration?.state === "needed";
</script>

{#if migrationVisible(migration)}
<div class="@container">
<section class="op-tint px-4 py-3.5 @md:px-5" aria-labelledby="recorded-at-title" data-migration-state={migration.state}>
  <div class="flex flex-wrap items-center justify-between gap-x-3 gap-y-2">
    <h2 id="recorded-at-title" class="flex min-w-0 flex-wrap items-baseline gap-x-2 py-1 font-medium">
      Recording times in past recordings
      {#if migration.state === "done"}
        <span class="inline-flex items-baseline gap-1 whitespace-nowrap text-xs font-normal {toneClasses.success}"><CircleCheck size={14} class="shrink-0 self-center" aria-hidden="true" />Done</span>
      {:else if migration.state === "blocked"}
        <span class="inline-flex items-baseline gap-1 whitespace-nowrap text-xs font-normal {toneClasses.warning}"><TriangleAlert size={14} class="shrink-0 self-center" aria-hidden="true" />Needs attention</span>
      {:else if migrationActive(migration)}
        <span class="inline-flex items-baseline gap-1 whitespace-nowrap text-xs font-normal text-base-content/65"><span class="loading loading-spinner loading-xs self-center" aria-hidden="true"></span>{migration.state === "pausing" ? "Pausing" : "Running"}</span>
      {:else if migration.state === "paused"}
        <span class="inline-flex items-baseline gap-1 whitespace-nowrap text-xs font-normal text-base-content/65"><Pause size={14} class="shrink-0 self-center" aria-hidden="true" />Paused</span>
      {:else if migration.state === "checking"}
        <span class="inline-flex items-baseline gap-1 whitespace-nowrap text-xs font-normal text-base-content/65"><span class="loading loading-spinner loading-xs self-center" aria-hidden="true"></span>Checking</span>
      {:else if migration.state === "needed"}
        <span class="inline-flex items-baseline gap-1 whitespace-nowrap text-xs font-normal text-base-content/65"><Clock size={14} class="shrink-0 self-center" aria-hidden="true" />Not started</span>
      {/if}
    </h2>
    {#if button}
      <button type="button" class="{button.action === 'start' ? 'op-btn' : 'btn btn-sm btn-outline btn-outline-quiet btn-outline-hover'} inline-flex h-auto min-h-8 items-center gap-1.5 py-1.5 text-left text-xs!" disabled={busy || button.disabled} on:click={() => act(button.action)}>
        <svelte:component this={button.action === "start" ? Play : Pause} size={15} class="shrink-0" aria-hidden="true" />{button.label}
      </button>
    {/if}
  </div>
  <p class="mt-1 text-sm text-base-content/70" role="status">{migrationSummary(migration)}</p>
  {#if error}<p role="alert" class="mt-2 text-sm {toneClasses.error}">{error}</p>{/if}

  {#if migration.blocked_items.length > 0}
    <ul class="mt-2 space-y-1 text-sm" aria-label="Blocked recordings">
      {#each migration.blocked_items as item}
        <li class="flex gap-1.5 text-base-content/70"><CircleAlert size={14} class="mt-0.5 shrink-0 {toneClasses.warning}" aria-hidden="true" /><span><span class="font-medium text-base-content">{item.title || item.meeting}</span>{#if item.date_label}<span class="text-base-content/60">{` · ${item.date_label}`}</span>{/if}: {item.reason}</span></li>
      {/each}
      {#if migration.blocked > migration.blocked_items.length}<li class="text-xs text-base-content/60">and {migration.blocked - migration.blocked_items.length} more</li>{/if}
    </ul>
  {/if}

  {#if migration.notifications && migration.state !== "done"}
    <details class="group mt-2" open={noticesOpen}>
      <summary class="tpl-toggle text-sm! group-open:text-base-content!"><span class="tpl-chev" aria-hidden="true"></span>What this does in Nextcloud</summary>
      <div class="mt-2 ml-px space-y-2 border-l-2 border-base-300 pl-3">
        {#if facts.length > 0}
          <dl class="grid grid-cols-[auto_1fr] gap-x-3 gap-y-0.5 text-sm" aria-label="What Nextcloud reports">
            {#each facts as fact}<dt class="text-base-content/60">{fact.label}</dt><dd class="text-base-content">{fact.value}</dd>{/each}
          </dl>
        {/if}
        {#each migration.notifications.details as line}<p class="text-sm text-base-content/70">{line}</p>{/each}
        <p class="flex gap-1.5 text-xs text-base-content/60"><Info size={12} class="mt-0.5 shrink-0" aria-hidden="true" />You can pause at any time. A file whose upload was already sent is finished first, and one not yet uploaded is left as it was, so none is left half-written.</p>
      </div>
    </details>
  {/if}
</section>
</div>
{/if}
