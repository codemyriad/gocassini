<script lang="ts">
  import { createEventDispatcher } from "svelte";
  import { TriangleAlert, X } from "@lucide/svelte";
  import type { PendingRecording, RecordingNotice, SharingStatus } from "./operator/setupHealth";
  import { LL } from "cassini-viewer/i18n";
  import {
    acceptAllLabel,
    acceptLabel,
    adminFixParts,
    adminFixPlain,
    dismissAllLabel,
    dismissLabel,
    hasSharingNews,
    hideThem,
    listedJobIds,
    pendingLine,
    pendingRowKey,
    pendingRowMeta,
    pendingRowTitle,
    pendingTitle,
    refusedLine,
    refusedTitle,
    showThem,
    starterRefusedLine,
    starterRefusedTitle,
    NOTICE_GROUP_AFTER,
    noticeRowLabel,
    refusedNoticeTitle,
    unpublishedTitle,
    whyAccept,
    type AcceptSharesResult,
    type ListedMeeting,
  } from "./operator/sharingNotice";

  // What Browse tells a reader about their own recording shares (D-885): the
  // recordings waiting for them to accept, and the ones a Nextcloud sharing
  // setting kept from them. One component for both of the viewer's slots —
  // the list's empty state (`card`) and the strip above a list that has
  // meetings — drawing the same lines in both; only the padding differs.
  //
  // Each block is one line where the width allows: the fact in bold, then
  // what to do about it, with the link to hand on inside the sentence. The
  // pending block keeps its rows behind a disclosure so a person can accept
  // one at a time, and the primary button on its first line accepts them all.
  // A recording a sharing setting kept from the reader, and one they started
  // that could not be published, each have a notice of their own, naming the
  // room and time, which they may dismiss for good; past two of a kind, they
  // are one line with the count and the rows behind a disclosure. Anything —
  // refused, unpublished or waiting — that turns up in the reader's list has
  // reached them since, and goes without a reload.
  //
  // Every sentence comes from sharingNotice.ts, where it is tested. What is
  // here is presentation, and the one piece of state that belongs beside the
  // rows: which of them is being accepted, and what the operator said about
  // the ones it would not accept.
  export let sharing: SharingStatus | null = null;
  export let variant: "card" | "strip" = "card";
  // The address handed on to an administrator for a refused share, without
  // the fragment. The shell owns it; without one there is no link.
  export let appUrl = "";
  // The request, owned by the shell like every other operator call: it knows
  // the operator's base path and this component does not. A row's spinner
  // and its failure reason are this component's, because they belong to the
  // row.
  export let accept: (shareIds: string[]) => Promise<AcceptSharesResult>;
  // Dismissing a refused or unpublished recording's notice, for good: the
  // shell's request too. The button's spinner and its failure are this
  // component's.
  export let dismiss: (jobIds: string[]) => Promise<void>;
  // The meetings the reader's list shows, as the viewer hands them down. A
  // recording among them reached the reader since — published, re-shared or
  // accepted in Nextcloud — and nothing about it is news.
  export let meetings: readonly ListedMeeting[] = [];

  // `accepted` whenever the operator answered an acceptance: the detail
  // carries what it accepted and the refreshed shares, and the shell re-reads
  // the list from there. `dismissed` once the operator recorded a dismissal:
  // the shell stops passing those recordings down.
  const dispatch = createEventDispatcher<{ accepted: AcceptSharesResult; dismissed: string[] }>();

  // Nothing is drawn unless there is news: an unchecked answer and zero counts
  // both render nothing, by the same rule the shell fills the empty state by.
  $: news = hasSharingNews(sharing) ? sharing : null;
  $: listed = listedJobIds(meetings);
  $: pending = (news?.pending ?? []).filter((entry) => !entry.jobId || !listed.has(entry.jobId));
  $: notListed = (entries: RecordingNotice[]) => entries.filter((entry) => !listed.has(entry.jobId));
  // The two kinds told one meeting at a time, in this order, each by the same
  // rules: a notice each, dismissable, grouped past NOTICE_GROUP_AFTER.
  $: kinds = [
    {
      key: "refused",
      label: $LL.app.shell.sharing_notice.refused_group_label(),
      entries: notListed(news?.refused ?? []),
      title: refusedNoticeTitle,
      groupTitle: refusedTitle,
      line: () => refusedLine(),
    },
    {
      key: "unpublished",
      label: $LL.app.shell.sharing_notice.unpublished_group_label(),
      entries: notListed(news?.unpublished ?? []),
      title: unpublishedTitle,
      groupTitle: starterRefusedTitle,
      line: starterRefusedLine,
    },
  ].filter((kind) => kind.entries.length > 0);
  $: shown = pending.length > 0 || kinds.length > 0;
  // Read again when the language changes; the link sits inside the sentence.
  $: fix = $LL && adminFixParts();

  // Which kinds' disclosures are open, when they are grouped.
  let groupOpen: Record<string, boolean> = {};
  // Job ids whose dismissal is in flight, and why one failed, by job id.
  let dismissing: ReadonlySet<string> = new Set();
  let dismissFailures: ReadonlyMap<string, string> = new Map();

  async function dismissRows(jobIds: string[]): Promise<void> {
    if (dismissing.size > 0 || jobIds.length === 0) return;
    dismissing = new Set(jobIds);
    const cleared = new Map(dismissFailures);
    for (const id of jobIds) cleared.delete(id);
    dismissFailures = cleared;
    try {
      await dismiss(jobIds);
      dispatch("dismissed", jobIds);
    } catch (error) {
      const message = error instanceof Error ? error.message : String(error);
      const next = new Map(dismissFailures);
      for (const id of jobIds) next.set(id, message);
      dismissFailures = next;
    } finally {
      dismissing = new Set();
    }
  }

  // The rows' disclosure, collapsed until "Show them".
  let expanded = false;
  // Share ids in flight. One request at a time: a second click while the
  // first is answered would race the refreshed list it is about to cause.
  let inFlight: ReadonlySet<string> = new Set();
  let acceptingAll = false;
  // Why a row could not be accepted, by row, in the operator's words. A row
  // keeps its reason until it is tried again or the shell takes it away.
  let failures: ReadonlyMap<string, string> = new Map();
  $: busy = inFlight.size > 0;

  function rowBusy(entry: PendingRecording, current: ReadonlySet<string>): boolean {
    return entry.shareIds.some((id) => current.has(id));
  }

  async function acceptRows(entries: PendingRecording[], all = false): Promise<void> {
    if (busy) return;
    const ids = entries.flatMap((entry) => entry.shareIds);
    if (ids.length === 0) return;
    inFlight = new Set(ids);
    acceptingAll = all;
    const cleared = new Map(failures);
    for (const entry of entries) cleared.delete(pendingRowKey(entry));
    failures = cleared;
    try {
      const result = await accept(ids);
      const next = new Map(failures);
      for (const entry of entries) {
        // Only a share the operator names as failed was refused. One in
        // neither list was never attempted — the operator works through the
        // pending list in bounded rounds and can stop short — so it is still
        // waiting, and the refreshed shares say so by keeping its row.
        const refusals = result.failed.filter((failure) => entry.shareIds.includes(failure.shareId));
        if (refusals.length === 0) continue;
        const reasons = refusals.map((failure) => failure.reason).filter((reason) => reason !== "");
        next.set(pendingRowKey(entry), reasons.join(" ") || $LL.app.shell.sharing_notice.accept_refused());
      }
      failures = next;
      // A refusal is something to read, and the rows are where it is said.
      if (next.size > 0) expanded = true;
      dispatch("accepted", result);
    } catch (error) {
      const message = error instanceof Error ? error.message : String(error);
      const next = new Map(failures);
      for (const entry of entries) next.set(pendingRowKey(entry), message);
      failures = next;
      expanded = true;
    } finally {
      inFlight = new Set();
      acceptingAll = false;
    }
  }
</script>

{#if news && shown}
  <div class="sharing-notice" class:sharing-card={variant === "card"} class:sharing-strip={variant === "strip"}>
    {#if pending.length > 0}
      <section class="sharing-block" role="status" aria-label={$LL.app.shell.sharing_notice.pending_label()}>
        <!-- No mark in front of the pending line: the count is in the sentence
             and on the button, and this is an invitation rather than a
             warning. The refused lines below keep the shell's warning mark, as
             the advisory strip has it. -->
        <div class="sharing-line">
          <strong class="text-sm font-semibold">{pendingTitle(pending.length)}</strong>
          <button type="button" class="btn btn-sm btn-primary ml-auto" disabled={busy} on:click={() => acceptRows(pending, true)}>
            {#if acceptingAll}
              <span class="loading loading-spinner loading-xs" aria-hidden="true"></span>
            {/if}
            {acceptAllLabel(pending.length)}
          </button>
        </div>
        <details class="sharing-details" bind:open={expanded}>
          <summary class="sharing-summary text-xs text-base-content/70">
            {pendingLine()}
            <span class="link link-primary">{expanded ? hideThem() : showThem()}</span>
          </summary>
          <ul class="sharing-rows">
            {#each pending as entry (pendingRowKey(entry))}
              <li class="sharing-row">
                <div class="min-w-0 flex-1">
                  <p class="truncate text-sm">
                    <strong class="font-medium">{pendingRowTitle(entry)}</strong>{#if pendingRowMeta(entry)}<span class="text-base-content/65">{" · "}{pendingRowMeta(entry)}</span>{/if}
                  </p>
                  {#if failures.get(pendingRowKey(entry))}
                    <p class="text-xs text-error" role="alert">{failures.get(pendingRowKey(entry))}</p>
                  {/if}
                </div>
                <button
                  type="button"
                  class="btn btn-sm"
                  disabled={busy}
                  aria-label={$LL.app.shell.sharing_notice.accept_row_aria({ title: pendingRowTitle(entry) })}
                  on:click={() => acceptRows([entry])}
                >
                  {#if rowBusy(entry, inFlight) && !acceptingAll}
                    <span class="loading loading-spinner loading-xs" aria-hidden="true"></span>
                  {/if}
                  {acceptLabel()}
                </button>
              </li>
            {/each}
          </ul>
          <p class="mt-2 text-xs text-base-content/65">{whyAccept()}</p>
        </details>
      </section>
    {/if}

    {#each kinds as kind (kind.key)}
      {#if kind.entries.length > NOTICE_GROUP_AFTER}
        <section class="sharing-block sharing-refused" role="status" aria-label={kind.label}>
          <details class="sharing-details" bind:open={groupOpen[kind.key]}>
            <summary class="sharing-summary sharing-line">
              <TriangleAlert size={16} class="shrink-0 text-warning" aria-hidden="true" />
              <p class="text-sm">
                <strong class="font-semibold">{kind.groupTitle(kind.entries.length)}</strong>
                {#if variant === "card"}{kind.line(kind.entries.length)}{/if}
              </p>
              <span class="link link-primary text-xs">{groupOpen[kind.key] ? hideThem() : showThem()}</span>
            </summary>
            <ul class="sharing-rows">
              {#each kind.entries as entry (entry.jobId)}
                <li class="sharing-row">
                  <div class="min-w-0 flex-1">
                    <p class="truncate text-sm">{noticeRowLabel(entry)}</p>
                    {#if dismissFailures.get(entry.jobId)}
                      <p class="text-xs text-error" role="alert">{dismissFailures.get(entry.jobId)}</p>
                    {/if}
                  </div>
                  <button
                    type="button"
                    class="btn btn-sm"
                    disabled={dismissing.size > 0}
                    aria-label={$LL.app.shell.sharing_notice.dismiss_row_aria({ label: noticeRowLabel(entry) })}
                    on:click={() => dismissRows([entry.jobId])}
                  >
                    {#if dismissing.has(entry.jobId) && dismissing.size === 1}
                      <span class="loading loading-spinner loading-xs" aria-hidden="true"></span>
                    {/if}
                    {dismissLabel()}
                  </button>
                </li>
              {/each}
            </ul>
            <div class="sharing-line mt-2">
              <p class="text-xs text-base-content/75">
                {#if appUrl}{fix.before}<a class="link link-primary" href={appUrl} title={fix.title}>{fix.link}</a>{fix.after}{:else}{adminFixPlain()}{/if}
              </p>
              <button type="button" class="btn btn-sm btn-ghost" disabled={dismissing.size > 0} on:click={() => dismissRows(kind.entries.map((entry) => entry.jobId))}>
                {#if dismissing.size > 1 && kind.entries.some((entry) => dismissing.has(entry.jobId))}
                  <span class="loading loading-spinner loading-xs" aria-hidden="true"></span>
                {/if}
                {dismissAllLabel()}
              </button>
            </div>
          </details>
        </section>
      {:else}
        {#each kind.entries as entry (entry.jobId)}
          <section class="sharing-block sharing-refused" role="status">
            <div class="sharing-line">
              <TriangleAlert size={16} class="shrink-0 text-warning" aria-hidden="true" />
              <!-- The strip keeps to the fact and the fix: the cause is the card's. -->
              <p class="text-sm">
                <strong class="font-semibold">{kind.title(entry)}</strong>
                {#if variant === "card"}{kind.line(1)}{/if}
                {#if appUrl}{fix.before}<a class="link link-primary" href={appUrl} title={fix.title}>{fix.link}</a>{fix.after}{:else}{adminFixPlain()}{/if}
              </p>
              <button
                type="button"
                class="btn btn-ghost btn-xs btn-square"
                title={dismissLabel()}
                aria-label={$LL.app.shell.sharing_notice.dismiss_notice_aria({ title: kind.title(entry) })}
                disabled={dismissing.size > 0}
                on:click={() => dismissRows([entry.jobId])}
              >
                {#if dismissing.has(entry.jobId)}
                  <span class="loading loading-spinner loading-xs" aria-hidden="true"></span>
                {:else}
                  <X size={14} aria-hidden="true" />
                {/if}
              </button>
            </div>
            {#if dismissFailures.get(entry.jobId)}
              <p class="text-xs text-error" role="alert">{dismissFailures.get(entry.jobId)}</p>
            {/if}
          </section>
        {/each}
      {/if}
    {/each}
  </div>
{/if}

<style>
  /* Inside the viewer's list, which sets --list-x for its own side padding;
     the fallback is for a host that does not. */
  .sharing-notice {
    display: flex;
    flex-direction: column;
    gap: 0.625rem;
  }
  /* The empty state: the same lines, with a card's room around them. */
  .sharing-card {
    max-width: 36rem;
    margin: 2rem auto;
    padding: 0 var(--list-x, 1rem);
  }
  .sharing-strip {
    margin: 1rem var(--list-x, 1rem) 0;
  }
  .sharing-block {
    display: flex;
    flex-direction: column;
    gap: 0.375rem;
    padding: 0.625rem 0.75rem;
    background-color: var(--color-base-100);
    border: 1px solid var(--color-base-300);
    border-radius: var(--radius-box, 0.75rem);
  }
  .sharing-card .sharing-block {
    padding: 0.875rem 1rem;
  }
  /* The run notice's warning tint (Operator.svelte), for a share Nextcloud
     refused: a fact about the deployment rather than something to do. */
  .sharing-refused {
    background-color: color-mix(in oklch, var(--color-warning) 10%, var(--color-base-100));
    border-color: color-mix(in oklch, var(--color-warning) 50%, transparent);
  }
  /* One line where the width allows: the icon, the sentence, and (for the
     pending block) the button at the right end. */
  .sharing-line {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: 0.375rem 0.5rem;
  }
  .sharing-line p {
    margin: 0;
    min-width: 0;
    flex: 1 1 16rem;
  }
  .sharing-summary {
    cursor: pointer;
    list-style: none;
  }
  .sharing-summary::-webkit-details-marker {
    display: none;
  }
  .sharing-rows {
    display: flex;
    flex-direction: column;
    margin: 0.5rem 0 0;
    padding: 0;
    list-style: none;
    border: 1px solid var(--color-base-300);
    border-radius: var(--radius-field, 0.5rem);
  }
  .sharing-row {
    display: flex;
    align-items: center;
    gap: 0.75rem;
    padding: 0.5rem 0.75rem;
  }
  .sharing-row + .sharing-row {
    border-top: 1px solid var(--color-base-300);
  }
</style>
