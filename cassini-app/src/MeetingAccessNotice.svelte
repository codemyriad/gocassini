<script lang="ts">
  import { createEventDispatcher } from "svelte";
  import { TriangleAlert } from "@lucide/svelte";
  import {
    ACCEPT,
    accessLine,
    accessTitle,
    ADMIN_FIX_LEAD,
    ADMIN_FIX_PLAIN,
    pendingRowMeta,
    pendingRowTitle,
    SEND_LINK,
    SEND_LINK_TITLE,
    WHY_ACCEPT,
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
        failure = refusals.map((entry) => entry.reason).filter((reason) => reason !== "").join(" ") || "Nextcloud did not accept this share.";
      }
      dispatch("accepted", result);
    } catch (error) {
      failure = error instanceof Error ? error.message : String(error);
    } finally {
      busy = false;
    }
  }
</script>

<div class="card bg-base-100 w-full max-w-md border border-base-300" class:access-refused={access.kind !== "pending"} role="status">
  <div class="card-body items-center text-center gap-3">
    {#if access.kind === "pending"}
      <h2 class="text-lg font-bold">{accessTitle(access)}</h2>
      <p class="text-base-content">
        <strong class="font-medium">{pendingRowTitle(access.entry)}</strong>{#if pendingRowMeta(access.entry)}<span class="text-base-content/65">{" · "}{pendingRowMeta(access.entry)}</span>{/if}
      </p>
      <p class="text-sm text-base-content/75">{accessLine(access)}</p>
      <button type="button" class="btn btn-primary" disabled={busy} on:click={acceptIt}>
        {#if busy}
          <span class="loading loading-spinner loading-xs" aria-hidden="true"></span>
        {/if}
        {ACCEPT}
      </button>
      {#if failure}
        <p class="text-sm text-error" role="alert">{failure}</p>
      {/if}
      <p class="text-xs text-base-content/65">{WHY_ACCEPT}</p>
    {:else}
      <TriangleAlert size={20} class="text-warning" aria-hidden="true" />
      <h2 class="text-lg font-bold">{accessTitle(access)}</h2>
      <p class="text-base-content">
        {accessLine(access)}
        {#if appUrl}{ADMIN_FIX_LEAD} <a class="link link-primary" href={appUrl} title={SEND_LINK_TITLE}>{SEND_LINK}</a>.{:else}{ADMIN_FIX_PLAIN}{/if}
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
