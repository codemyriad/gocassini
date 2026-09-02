<script lang="ts">
  import { onMount } from "svelte";
  import { HardDrive, Lock, RefreshCw, TriangleAlert } from "@lucide/svelte";
  import { OperatorClient, OperatorHttpError } from "./operator/client";
  import type { StorageModeOption, StorageStatus } from "./operator/types";

  // The storage-mode switch (D-616 first pass). Deliberately simple: two
  // buttons, a confirmation, and the operator's own words for everything else.
  //
  // Every sentence rendered here — what a mode means, what switching to it
  // would do, what is missing and which command fixes it — comes from GET
  // /storage. That is not laziness: the operator is the layer that knows the
  // Team folder's id, the group names and which prerequisite is actually
  // absent, and a wrong instruction is a worse failure than a missing one. This
  // component decides only when to ask and what to show.
  //
  //   idle ──click the inactive mode──▶ confirming ──confirm──▶ switching
  //     ▲                                   │                       │
  //     └──────────cancel───────────────────┘                       │
  //     └──────────error (mode unchanged, message shown)◀───────────┘

  export let operatorClient: OperatorClient | null = null;

  let status: StorageStatus | null = null;
  let loading = true;
  let loadError = "";
  let switchError = "";
  // pending is the mode the confirmation prompt is asking about, or null.
  let pending: StorageModeOption | null = null;
  let switching = false;

  onMount(() => {
    void loadStorage();
  });

  async function loadStorage() {
    if (!operatorClient) {
      return;
    }
    loading = true;
    loadError = "";
    switchError = "";
    try {
      status = await operatorClient.getStorage();
    } catch (error) {
      loadError = asMessage(error);
    } finally {
      loading = false;
    }
  }

  function requestSwitch(option: StorageModeOption) {
    // Clicking the mode already in force is a no-op, and an unavailable mode
    // cannot be switched to — its blocker is already on screen.
    if (option.active || !option.available || switching) {
      return;
    }
    switchError = "";
    pending = option;
  }

  function cancelSwitch() {
    pending = null;
  }

  async function confirmSwitch() {
    if (!operatorClient || !pending || switching) {
      return;
    }
    const target = pending;
    switching = true;
    switchError = "";
    try {
      status = await operatorClient.putStorage(target.mode === "access_controlled");
      pending = null;
    } catch (error) {
      switchError = asMessage(error);
      pending = null;
      // Re-read rather than trusting the pre-switch snapshot. Most failures
      // change nothing — the operator refuses before it touches anything — but
      // one does not: a transition that fails AFTER moving the archive has
      // already changed the mode this operator is using, and its message says
      // so. Showing the state from before the attempt would contradict it.
      try {
        status = await operatorClient.getStorage();
      } catch {
        // Keep what we had; the switch error is the thing worth showing.
      }
    } finally {
      switching = false;
    }
  }

  function asMessage(error: unknown): string {
    if (error instanceof OperatorHttpError) {
      return error.message;
    }
    return error instanceof Error ? error.message : String(error);
  }

  function modeSourceLabel(source: string): string {
    if (source === "configured") return "Chosen";
    if (source === "derived") return "Detected from this Nextcloud";
    return source || "—";
  }

  $: transition = status?.transition ?? null;
  $: unresolved = status !== null && status.mode === "";
</script>

<section class="rounded-box border border-base-300 bg-base-100 shadow-sm">
  <header class="flex items-center justify-between gap-3 px-4 py-3">
    <div class="min-w-0">
      <h2 class="font-semibold">Recording storage</h2>
      <p class="text-xs text-base-content/60">
        Where Cassini keeps published recordings, and who can read them.
      </p>
    </div>
    <button
      class="btn btn-ghost btn-sm btn-square"
      type="button"
      on:click={loadStorage}
      aria-label="Reload storage settings"
    >
      <RefreshCw size={16} aria-hidden="true" />
    </button>
  </header>

  {#if loadError}
    <div class="px-4 py-4">
      <div class="alert alert-error text-sm">{loadError}</div>
    </div>
  {:else if loading}
    <div class="flex items-center justify-center p-6 text-sm text-base-content/60">
      Loading storage settings…
    </div>
  {:else if !status}
    <div class="flex items-center justify-center p-6 text-sm text-base-content/60">
      No storage settings available.
    </div>
  {:else}
    <div class="grid gap-4 p-4">
      {#if unresolved}
        <!-- Not "default": nothing has looked at this Nextcloud yet, and an
             unchecked instance is not evidence that either mode would work. -->
        <div class="alert alert-warning items-start gap-3 text-sm" role="status">
          <TriangleAlert size={16} class="mt-0.5 shrink-0" aria-hidden="true" />
          <span>
            Cassini has not checked this Nextcloud since it started. Setup runs when the app is
            enabled, so disable and re-enable it to see which storage mode is in force.
          </span>
        </div>
      {/if}

      <div
        class="grid gap-3 lg:grid-cols-2"
        role="radiogroup"
        aria-label="Recording storage mode"
      >
        {#each status.modes as option (option.mode)}
          <div
            class="grid content-start gap-2 rounded-box border p-3 transition {option.active
              ? 'border-primary bg-primary/15 ring-1 ring-inset ring-primary'
              : 'border-base-300 bg-base-200'}"
          >
            <div class="flex items-center gap-2">
              {#if option.mode === "access_controlled"}
                <Lock size={16} class="shrink-0 text-base-content/60" aria-hidden="true" />
              {:else}
                <HardDrive size={16} class="shrink-0 text-base-content/60" aria-hidden="true" />
              {/if}
              <h3 class="text-sm font-semibold">{option.label}</h3>
              {#if option.active}
                <span class="badge badge-primary badge-sm">Current</span>
              {/if}
            </div>

            <p class="text-xs text-base-content/70">{option.summary}</p>

            {#if option.blocker}
              <div class="grid gap-2 rounded-box border border-warning/50 bg-warning/10 p-2">
                <p class="text-xs break-words">{option.blocker}</p>
                {#if option.instructions.length > 0}
                  <!-- The first pass sets nothing up for you, so these commands
                       ARE the setup path. Shown in full rather than behind a
                       disclosure: they are the only way to reach this mode. -->
                  <pre
                    class="m-0 overflow-x-auto rounded-box bg-base-200 p-2 font-mono text-xs leading-relaxed">{option.instructions.join(
                      "\n",
                    )}</pre>
                {/if}
              </div>
            {/if}

            <button
              class="btn btn-sm mt-1 w-full {option.active ? 'btn-disabled' : 'btn-outline'}"
              type="button"
              role="radio"
              aria-checked={option.active}
              disabled={option.active || !option.available || switching}
              on:click={() => requestSwitch(option)}
            >
              {#if option.active}
                In use
              {:else if !option.available}
                Not available yet
              {:else}
                Switch to {option.label.toLowerCase()}
              {/if}
            </button>
          </div>
        {/each}
      </div>

      {#if pending}
        <!-- An inline confirmation rather than a modal dialog: the whole app
             runs inside a shadow root on Nextcloud's embedded page, where a
             top-layer <dialog> is the one element whose styling and focus
             behaviour do not reliably follow it. -->
        <!-- A generic <div>, not a <section>: an interactive role on a
             semantic sectioning element is an a11y error, and this genuinely is
             a dialog rather than a region of the page. -->
        <div
          class="grid gap-3 rounded-box border border-warning bg-warning/10 p-3"
          role="alertdialog"
          aria-label="Confirm storage mode change"
        >
          <div class="flex items-start gap-2">
            <TriangleAlert size={18} class="mt-0.5 shrink-0 text-warning" aria-hidden="true" />
            <div class="grid gap-1">
              <p class="text-sm font-semibold">Switch to {pending.label.toLowerCase()}?</p>
              <p class="text-xs break-words text-base-content/80">{pending.consequence}</p>
            </div>
          </div>
          <div class="flex flex-wrap gap-2">
            <button class="btn btn-sm btn-warning" type="button" disabled={switching} on:click={confirmSwitch}>
              {#if switching}
                <span class="loading loading-spinner loading-xs" aria-hidden="true"></span>
                Moving recordings…
              {:else}
                Yes, switch
              {/if}
            </button>
            <button class="btn btn-sm btn-ghost" type="button" disabled={switching} on:click={cancelSwitch}>
              Cancel
            </button>
          </div>
        </div>
      {/if}

      {#if switchError}
        <div class="alert alert-error items-start gap-3 text-sm" role="alert">
          <TriangleAlert size={16} class="mt-0.5 shrink-0" aria-hidden="true" />
          <div class="grid gap-1">
            <!-- Deliberately not "the mode was not changed": that is true of
                 almost every failure but not of all of them, and the operator's
                 own sentence below says which happened. The cards above are
                 re-read after a failure, so they show the mode really in force. -->
            <p class="font-semibold">Switching the storage mode failed.</p>
            <p class="text-xs break-words">{switchError}</p>
          </div>
        </div>
      {/if}

      {#if transition}
        <div class="alert alert-success items-start gap-3 text-sm" role="status">
          <div class="grid gap-1">
            <p class="font-semibold">
              Storage is now {status.mode === "access_controlled" ? "access controlled" : "default"}.
            </p>
            <p class="text-xs">
              {transition.meetings_moved}
              {transition.meetings_moved === 1 ? "recording was" : "recordings were"} moved
              {#if transition.destination_root}into {transition.destination_root}{/if}.
            </p>
            {#if transition.leftover_source}
              <p class="text-xs break-words">
                Some files were left in {transition.leftover_source} and can be removed by hand once
                you have checked them.
              </p>
            {/if}
          </div>
        </div>
      {/if}

      <dl class="grid gap-x-6 gap-y-3 border-t border-base-300 pt-3 sm:grid-cols-3">
        <div>
          <dt class="mb-1 text-xs uppercase tracking-wide text-base-content/45">Mode</dt>
          <dd class="text-sm">{status.mode === "" ? "Not checked yet" : status.mode}</dd>
        </div>
        <div>
          <dt class="mb-1 text-xs uppercase tracking-wide text-base-content/45">Source</dt>
          <dd class="text-sm">{modeSourceLabel(status.mode_source)}</dd>
        </div>
        <div>
          <dt class="mb-1 text-xs uppercase tracking-wide text-base-content/45">Storage health</dt>
          <dd class="text-sm {status.ok ? 'text-success' : 'text-warning'}">
            {status.state || "unknown"}
          </dd>
        </div>
      </dl>

      {#if !status.ok && status.detail}
        <!-- The operator's own sentence, verbatim, so this panel and the
             container log read the same. -->
        <p class="rounded-box bg-base-200 p-3 font-mono text-xs break-words text-base-content/70">
          {status.detail}
        </p>
      {/if}
    </div>
  {/if}
</section>
