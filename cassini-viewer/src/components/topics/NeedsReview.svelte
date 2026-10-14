<script lang="ts">
  // The review queue across the meetings the caller can read
  // (docs/topic-suggestions.md): suggestions grouped by topic, decided one at a
  // time or several at once. Keyboard: arrows move between meetings, Space
  // selects, C confirms, R rejects.
  import { createEventDispatcher, tick } from "svelte";

  import type { DataProvider } from "../../viewer/dataProvider";
  import { colorFor } from "../../viewer/tagPalette";
  import { COVERAGE_LIMITS, MAX_BATCH, coverageLine, newRequestId, type TopicQueue, type TopicQueueGroup } from "../../viewer/topics";
  import TagChip from "../tags/TagChip.svelte";
  import CloseButton from "../ui/CloseButton.svelte";
  import { focusLayer } from "../ui/focusLayer";

  export let open = false;
  export let provider: Pick<DataProvider, "loadReviewQueue" | "decideSuggestionBatch">;
  // Limit the queue to one room's meetings: a room key from the rail.
  export let room = "";
  export let roomLabel = "";

  const dispatch = createEventDispatcher<{ close: void; openMeeting: string; changed: void }>();

  let queue: TopicQueue | null = null;
  let loading = false;
  let error = "";
  let busy = false;
  // Selected meeting ids per topic.
  let selected: Record<string, Set<string>> = {};
  let reports: Record<string, string> = {};
  let sheet: HTMLElement;

  // Opening, or another room, starts afresh: nothing selected out of sight.
  $: if (open) reset(room);
  else queue = null;
  function reset(_room: string) {
    selected = {};
    reports = {};
    void load();
  }

  async function load(cursor = "") {
    if (!provider.loadReviewQueue) return;
    loading = true;
    error = "";
    try {
      const page = await provider.loadReviewQueue({ room: room || undefined, cursor: cursor || undefined });
      if (page.enabled === false) {
        queue = null;
        error = "Topic suggestions are switched off.";
        return;
      }
      queue = cursor && queue ? merge(queue, page) : page;
    } catch {
      error = "The suggestions could not be loaded.";
    } finally {
      loading = false;
    }
  }

  function merge(current: TopicQueue, page: TopicQueue): TopicQueue {
    const groups = [...current.groups];
    for (const group of page.groups) {
      const at = groups.findIndex((g) => g.tagId === group.tagId);
      if (at >= 0) {
        const known = new Set(groups[at].items.map((item) => item.meetingId));
        groups[at] = { ...groups[at], items: [...groups[at].items, ...group.items.filter((item) => !known.has(item.meetingId))] };
      }
      else groups.push(group);
    }
    return { ...page, groups };
  }

  function toggle(tagId: string, meetingId: string) {
    const set = new Set(selected[tagId] ?? []);
    if (set.has(meetingId)) set.delete(meetingId);
    else if (set.size < MAX_BATCH) set.add(meetingId);
    selected = { ...selected, [tagId]: set };
  }

  async function decide(group: TopicQueueGroup, meetingIds: string[], decision: "confirm" | "reject") {
    if (!provider.decideSuggestionBatch || busy || meetingIds.length === 0) return;
    busy = true;
    try {
      const { results } = await provider.decideSuggestionBatch({ requestId: newRequestId(), tagId: group.tagId, meetingIds: meetingIds.slice(0, MAX_BATCH), decision });
      const done = results.filter((r) => r.status === "confirmed" || r.status === "rejected").length;
      const failed = results.filter((r) => r.status === "error" || r.status === "not-found");
      const verb = decision === "confirm" ? "Confirmed" : "Marked “not this meeting”";
      let report = `${verb} on ${done === 1 ? "1 meeting" : `${done} meetings`}.`;
      if (failed.some((r) => r.error === "marks-unresolved")) report += " Some meetings have old tags that need attention first.";
      else if (failed.length) report += ` ${failed.length} could not be changed; try them one by one.`;
      reports = { ...reports, [group.tagId]: report };
      // Only what was just decided leaves the selection.
      const remaining = new Set(selected[group.tagId] ?? []);
      for (const id of meetingIds) remaining.delete(id);
      selected = { ...selected, [group.tagId]: remaining };
      dispatch("changed");
      await load();
    } catch {
      reports = { ...reports, [group.tagId]: "That did not work. Try again." };
    } finally {
      busy = false;
    }
  }

  async function onRowKey(event: KeyboardEvent, group: TopicQueueGroup, meetingId: string) {
    // The row's own keys only: not a shortcut (Ctrl+C copies), not a key
    // meant for a button inside the row.
    if (event.ctrlKey || event.metaKey || event.altKey || event.target !== event.currentTarget) return;
    const rows = [...sheet.querySelectorAll<HTMLElement>("[data-review-row]")];
    const at = rows.indexOf(event.currentTarget as HTMLElement);
    const key = event.key.toLowerCase();
    if (key === "arrowdown" || key === "arrowup") {
      event.preventDefault();
      rows[at + (key === "arrowdown" ? 1 : -1)]?.focus();
    } else if (key === " ") {
      event.preventDefault();
      toggle(group.tagId, meetingId);
    } else if (key === "c" || key === "r") {
      event.preventDefault();
      await decide(group, [meetingId], key === "c" ? "confirm" : "reject");
      await tick();
      [...sheet.querySelectorAll<HTMLElement>("[data-review-row]")][at]?.focus();
    }
  }

  function onKeydown(event: KeyboardEvent) {
    if (event.key === "Escape" && !event.defaultPrevented) {
      event.preventDefault();
      dispatch("close");
    }
  }
</script>

{#if open}
  <button type="button" tabindex="-1" class="absolute inset-0 z-41 cursor-pointer bg-black/55 backdrop-blur-[3px]" aria-label="Close Needs review"
    on:click={() => dispatch("close")}></button>
  <div bind:this={sheet} use:focusLayer role="dialog" aria-modal="true" aria-labelledby="needs-review-title" tabindex="-1" on:keydown={onKeydown}
    class="absolute inset-y-0 right-0 z-42 flex w-[min(640px,100%)] flex-col border-base-300 bg-base-100 shadow-2xl min-[721px]:border-l max-[720px]:top-auto max-[720px]:h-[92%] max-[720px]:w-full max-[720px]:rounded-t-box max-[720px]:border-t">
    <header class="flex items-center gap-2.5 border-b border-base-300 px-5 pb-3 pt-4">
      <h2 id="needs-review-title" class="flex flex-1 items-center gap-2.5 text-[17px] font-semibold">
        Needs review{#if roomLabel}<span class="text-base-content/60"> · {roomLabel}</span>{/if} {#if queue}<span class="badge badge-outline badge-sm font-medium">{queue.total}</span>{/if}
      </h2>
      <CloseButton label="Close" on:click={() => dispatch("close")} />
    </header>
    <div class="min-h-0 flex-1 overflow-y-auto overscroll-contain px-4 pb-8 pt-2">
      {#if queue}
        <p class="text-xs text-base-content/70" data-testid="coverage-line">{coverageLine(queue.coverage)}</p>
        <p class="mt-0.5 text-xs text-base-content/50">{COVERAGE_LIMITS}</p>
        <p class="mt-1 text-xs text-base-content/50">Arrows move, Space selects, C confirms, R says “not this meeting”.</p>
      {/if}
      {#if error}<p class="mt-2 text-sm text-error" role="alert">{error}</p>{/if}
      {#if queue && queue.groups.length === 0}
        <p class="py-6 text-sm text-base-content/60">Nothing to review.</p>
      {/if}
      {#each queue?.groups ?? [] as group (group.tagId)}
        {@const shown = new Set(group.items.map((item) => item.meetingId))}
        {@const chosen = [...(selected[group.tagId] ?? [])].filter((id) => shown.has(id))}
        {@const full = chosen.length >= MAX_BATCH}
        <section class="mt-4 grid gap-1" aria-label={`Suggested: ${group.label}`}>
          <div class="flex items-center gap-2">
            <TagChip suggested label={group.label} color={colorFor({ tagId: group.tagId, color: group.color ?? "" })} icon={group.icon ?? ""} />
            <span class="text-xs text-base-content/60">{group.total === 1 ? "1 meeting" : `${group.total} meetings`}</span>
            <span class="flex-1"></span>
            <button type="button" class="btn btn-xs" disabled={busy || chosen.length === 0} on:click={() => decide(group, chosen, "confirm")}>Confirm selected{chosen.length ? ` (${chosen.length})` : ""}</button>
            <button type="button" class="btn btn-ghost btn-xs" disabled={busy || chosen.length === 0} on:click={() => decide(group, chosen, "reject")}>Not these</button>
          </div>
          {#if full}<p class="text-xs text-base-content/60">At most {MAX_BATCH} meetings at once.</p>{/if}
          {#if reports[group.tagId]}<p class="text-xs" role="status">{reports[group.tagId]}</p>{/if}
          <ul class="grid gap-0.5">
            {#each group.items as item (item.meetingId)}
              <li data-review-row tabindex="0" class="review-row grid grid-cols-[auto_1fr_auto] items-start gap-2 rounded-box px-2 py-1.5"
                aria-label={`${group.label} on ${item.title}`} on:keydown={(event) => onRowKey(event, group, item.meetingId)}>
                <input type="checkbox" class="checkbox checkbox-xs mt-0.5" tabindex="-1" aria-label={`Select ${item.title}`}
                  checked={chosen.includes(item.meetingId)} disabled={full && !chosen.includes(item.meetingId)}
                  on:change={() => toggle(group.tagId, item.meetingId)} />
                <div class="min-w-0">
                  <button type="button" class="link truncate text-left text-sm font-medium" on:click={() => dispatch("openMeeting", item.meetingId)}>{item.title}</button>
                  <p class="text-xs text-base-content/60">{[item.date, item.room].filter(Boolean).join(" · ")}{item.rule === "short-strong" ? " · one short, focused discussion" : ` · ${item.supportWords} words on it`}</p>
                  {#if item.quote}<q class="line-clamp-2 text-xs text-base-content/75">{item.quote}</q>{/if}
                </div>
                <div class="flex gap-1">
                  <button type="button" class="btn btn-xs" disabled={busy} aria-label={`Confirm ${group.label} on ${item.title}`} on:click={() => decide(group, [item.meetingId], "confirm")}>Confirm</button>
                  <button type="button" class="btn btn-ghost btn-xs" disabled={busy} aria-label={`Not ${group.label} on ${item.title}`} on:click={() => decide(group, [item.meetingId], "reject")}>Not this</button>
                </div>
              </li>
            {/each}
          </ul>
        </section>
      {/each}
      {#if queue?.next}
        <button type="button" class="btn btn-sm mt-4" disabled={loading} on:click={() => load(queue?.next)}>Show more</button>
      {/if}
    </div>
  </div>
{/if}

<style>
  .review-row:focus-visible {
    outline: 2px solid color-mix(in oklch, var(--color-base-content) 40%, transparent);
  }
  .review-row:hover {
    background: color-mix(in oklch, var(--color-base-content) 4%, transparent);
  }
</style>
