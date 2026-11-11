<script lang="ts">
  import { createEventDispatcher } from "svelte";
  import { TriangleAlert } from "@lucide/svelte";
  import { LL } from "cassini-viewer/i18n";
  import {
    acceptLabel,
    accessLine,
    accessTitle,
    adminFixParts,
    adminFixPlain,
    pendingRowMeta,
    pendingRowTitle,
    whyAccept,
    type AcceptSharesResult,
    type MeetingAccess,
  } from "./operator/sharingNotice";

  // What a meeting opened from a link says in place of "Meeting not found"
  // when the reader cannot open it yet (D-885): a share waiting for them,
  // with the one button that accepts it, or a share a Nextcloud sharing
  // setting refused, with who can fix that. The viewer's meeting view puts
  // this where its not-found card would be; the sentences are
  // sharingNotice.ts's, the same ones Browse's list uses.
  export let access: MeetingAccess;
  // The address handed on to an administrator, without the fragment.
  export let appUrl = "";
  // The shell's request, as SharingNotice takes it.
  export let accept: (shareIds: string[]) => Promise<AcceptSharesResult>;

  // `accepted` whenever the operator answered: the shell takes the refreshed
  // shares and re-reads the list, and the meeting opens once it is there.
  const dispatch = createEventDispatcher<{ accepted: AcceptSharesResult }>();

  let busy = false;
  let failure = "";

  async function acceptIt(): Promise<void> {
    if (busy || access.kind !== "pending") return;
    const ids = access.entry.shareIds;
    busy = true;
    failure = "";
    try {
      const result = await accept(ids);
      const refusals = result.failed.filter((entry) => ids.includes(entry.shareId));
      if (refusals.length > 0) {
        failure = refusals.map((entry) => entry.reason).filter((reason) => reason !== "").join(" ") || $LL.app.shell.meeting_access.share_refused();
      }
      dispatch("accepted", result);
    } catch (error) {
      failure = error instanceof Error ? error.message : String(error);
    } finally {
      busy = false;
    }
  }

  // Read through $LL, so every sentence follows the reader's language,
  // including a change after the notice is drawn.
  $: title = $LL && accessTitle(access);
  $: line = $LL && accessLine(access);
  $: fix = $LL && adminFixParts();
  $: rowTitle = access.kind === "pending" ? $LL && pendingRowTitle(access.entry) : "";
  $: rowMeta = access.kind === "pending" ? $LL && pendingRowMeta(access.entry) : "";
</script>

<div class="card bg-base-100 w-full max-w-md border border-base-300" class:access-refused={access.kind !== "pending"} role="status">
  <div class="card-body items-center text-center gap-3">
    {#if access.kind === "pending"}
      <h2 class="text-lg font-bold">{title}</h2>
      <p class="text-base-content">
        <strong class="font-medium">{rowTitle}</strong>{#if rowMeta}<span class="text-base-content/65">{" · "}{rowMeta}</span>{/if}
      </p>
      <p class="text-sm text-base-content/75">{line}</p>
      <button type="button" class="btn btn-primary" disabled={busy} on:click={acceptIt}>
        {#if busy}
          <span class="loading loading-spinner loading-xs" aria-hidden="true"></span>
        {/if}
        {$LL && acceptLabel()}
      </button>
      {#if failure}
        <p class="text-sm text-error" role="alert">{failure}</p>
      {/if}
      <p class="text-xs text-base-content/65">{$LL && whyAccept()}</p>
    {:else}
      <TriangleAlert size={20} class="text-warning" aria-hidden="true" />
      <h2 class="text-lg font-bold">{title}</h2>
      <p class="text-base-content">
        {line}
        {#if appUrl}{fix.before}<a class="link link-primary" href={appUrl} title={fix.title}>{fix.link}</a>{fix.after}{:else}{$LL && adminFixPlain()}{/if}
      </p>
    {/if}
  </div>
</div>

<style>
  /* The list notice's warning tint (SharingNotice.svelte). */
  .access-refused {
    background-color: color-mix(in oklch, var(--color-warning) 10%, var(--color-base-100));
    border-color: color-mix(in oklch, var(--color-warning) 50%, transparent);
  }
</style>
