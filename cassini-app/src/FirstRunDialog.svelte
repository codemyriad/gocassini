<script lang="ts">
  import { createEventDispatcher, onMount } from "svelte";
  import { TriangleAlert } from "@lucide/svelte";
  import type { OperatorClient } from "./operator/client";
  import { firstRunReady, type FirstRunPlan } from "./operator/firstRun";
  import { NcSetupError, runSetupPlan } from "./operator/ncSetup";
  import { notifySetupChanged } from "./operator/setupSignal";

  // The one dialog a fresh install shows (D-756).
  //
  // Cassini used to open on a Setup tab and a wizard, and refused to record
  // until somebody answered it. The operator now checks direct sharing when
  // it is enabled and records from then on, so there is no question left — only
  // two things worth saying once: who will see recordings, and that Nextcloud
  // needs the administrator's own session to create the account they are kept
  // in.
  //
  // Every decision this renders was taken in operator/firstRun.ts, which is
  // where they are tested. This component performs them and nothing else.
  export let operatorClient: OperatorClient;
  export let plan: FirstRunPlan;

  // `done` closes the dialog; `settings` opens setup when the account cannot
  // be created here. The shell owns which surface is showing.
  const dispatch = createEventDispatcher<{ done: void; settings: void }>();

  let busy = false;
  let progress = "";
  let actionError = "";
  let primary: HTMLButtonElement | null = null;

  onMount(() => {
    // The dialog covers the app, so the focus has to be inside it. The primary
    // button unless there is none — the standalone build has only the other
    // one. No focus trap: it is two buttons, and a half-built trap is worse
    // than the browser's own behaviour.
    primary?.focus();
  });

  // openSettings leaves for Operator › Publish pipeline without creating anything and
  // WITHOUT acknowledging (D-756 review).
  //
  // It used to acknowledge on the way out, which made this the one dialog a
  // fresh install ever gets and spent it on a button that changed nothing: the
  // `cassini` account was still missing, so the install still could not record
  // and nothing would ever say so again. The flag is answered where the account
  // is made — here by `start`, and in the settings section by its own "Create
  // the account" row — so an administrator who leaves to look at the audience
  // first meets this dialog again until there is an account to keep recordings
  // in.
  //
  // The shell hides the dialog for the rest of this page's life, so nobody is
  // shown it twice over the page they are working on.
  function openSettings(): void {
    dispatch("settings");
  }

  // start does the whole of the first run, in the order the pieces depend on:
  //
  //	1. the account, from THIS page. Nextcloud's password-confirmation
  //	   middleware refuses these writes to the operator on every shipping
  //	   release, and accepts them from the administrator's own session.
  //	2. a re-check, so the operator sees the account that did not exist when it
  //	   last looked. Without it the storage record still reports the account
  //	   missing and the app still says it is not set up.
  //	3. the acknowledgement, which is what makes this dialog once per install.
  //	   It happens HERE and in the settings section's own account row, and in
  //	   both places only after the account exists: a flag answered by anything
  //	   else leaves an install that cannot record with nothing left to say so.
  //
  // The password runSetupPlan mints is deliberately dropped on the floor. The
  // operator authenticates as the account through AppAPI's act-as-user header
  // and needs no password; showing one made a credential the administrator was
  // asked to keep safe out of a value nothing uses. "Set a password" is a row
  // under Details for administrators for the rare day somebody needs to sign in
  // as it.
  async function start(): Promise<void> {
    if (busy) {
      return;
    }
    busy = true;
    actionError = "";
    progress = "";
    try {
      if (plan.creates) {
        await runSetupPlan(plan.steps, {
          onProgress: ({ step, index, total }) => (progress = `${index + 1}/${total} — ${step.title}`),
        });
        progress = "Checking…";
        const checked = await operatorClient.recheckStorage();
        if (!checked.service_account.exists) {
          throw new Error("Nextcloud has not confirmed the recordings account yet. Check its setup and try again.");
        }
      }
      await operatorClient.acknowledgeFirstRun();
      // This Nextcloud is no longer the one the shell looked at when it
      // mounted: the account exists and the flag is answered.
      notifySetupChanged();
      dispatch("done");
    } catch (error) {
      actionError = asMessage(error);
    } finally {
      busy = false;
      progress = "";
    }
  }

  function asMessage(error: unknown): string {
    if (error instanceof NcSetupError) {
      switch (error.reason) {
        case "cancelled":
          return "Nextcloud needs you to confirm your password before it will create the account.";
        case "unavailable":
          return `${error.message} Open Cassini from Nextcloud's own menu.`;
        default:
          return error.step ? `${error.message} (at: ${error.step})` : error.message;
      }
    }
    return error instanceof Error ? error.message : String(error);
  }
</script>

<!-- An inline role="dialog" over a scrim, not a <dialog>: the whole app runs
     inside a shadow root on Nextcloud's embedded page, where the top layer is
     the one place whose styling and focus behaviour do not reliably follow it.
     RecordingAccessPanel.svelte's confirmations are inline for the same
     reason. -->
<div class="first-run-scrim">
  <div class="fr-backdrop" aria-hidden="true">
    <div class="fr-sk-tabs">
      <span class="fr-sk-tab current"><span class="fr-sk" style="width: 44px"></span></span>
      <span class="fr-sk-tab"><span class="fr-sk" style="width: 52px"></span></span>
    </div>
    <div class="fr-sk-body">
      <div class="fr-sk-rail">
        <span class="fr-sk fr-sk-label"></span>
        {#each [96, 40, 118, 50, 128, 36, 76, 44] as width, index}
          <span class="fr-sk-room" class:current={index === 0}>
            <span class="fr-sk" style="width: {width}px"></span>
            <span class="fr-sk fr-sk-count"></span>
          </span>
        {/each}
        <span class="fr-sk fr-sk-label fr-sk-gap"></span>
        {#each [52, 40] as width}
          <span class="fr-sk-room">
            <span class="fr-sk fr-sk-check"></span>
            <span class="fr-sk" style="width: {width}px"></span>
          </span>
        {/each}
        <span class="fr-sk fr-sk-label fr-sk-gap"></span>
        {#each [60, 48] as width}
          <span class="fr-sk-room">
            <span class="fr-sk fr-sk-check"></span>
            <span class="fr-sk" style="width: {width}px"></span>
          </span>
        {/each}
      </div>
      <div class="fr-sk-main">
        <span class="fr-sk-search"></span>
        <span class="fr-sk fr-sk-result"></span>
        <span class="fr-sk fr-sk-month"></span>
        {#each [150, 150, 150, 128, 150, 150, 60, 150, 150, 150] as width}
          <div class="fr-sk-row">
            <span class="fr-sk fr-sk-check"></span>
            <span class="fr-sk-row-text">
              <span class="fr-sk fr-sk-title" style="width: {width}px"></span>
              <span class="fr-sk fr-sk-meta"></span>
            </span>
            <span class="fr-sk fr-sk-duration"></span>
          </div>
        {/each}
      </div>
    </div>
  </div>
  <div
    class="first-run-card w-full max-w-[1000px] rounded-box border border-base-300 bg-base-100 p-5 shadow-lg sm:p-6"
    role="dialog"
    aria-modal="true"
    aria-labelledby="cassini-first-run-title"
  >
    <div class="grid gap-3">
      <h2 id="cassini-first-run-title" class="text-lg font-bold">
        {firstRunReady(plan) ? "Cassini is ready to record" : "Cassini can't record yet"}
      </h2>
      <p class="text-sm text-base-content/80">
        Recordings use Nextcloud's built-in file sharing. Cassini keeps them in its own account and shares each meeting with its participants.
      </p>

      <div class="fr-opt">
        <span class="fr-opt-title">Room participants</span>
        <span class="fr-opt-desc">Cassini shares each recording with people who belonged to the Talk room. Public meeting participants can share it onward when this Nextcloud allows resharing.</span>
      </div>

      {#if plan.blocked}
        <p class="fr-problem" role="status">
          <TriangleAlert size={18} class="fr-problem-icon" aria-hidden="true" />
          <span>Cassini needs a Nextcloud account to keep recordings in, and this page has no way to
          create it. Open <strong>Operator › Publish pipeline</strong> to see what is missing.</span>
        </p>
      {:else if plan.unavailable}
        <p class="fr-problem" role="status">
          <TriangleAlert size={18} class="fr-problem-icon" aria-hidden="true" />
          <span>Cassini needs a Nextcloud account to keep recordings in. This page can't create it
          itself. Open Cassini from Nextcloud's own menu, or run the commands under
          <strong>Details for administrators</strong> in <strong>Operator › Publish pipeline</strong>
          on the server.</span>
        </p>
      {:else if plan.creates}
        <p class="text-sm text-base-content/80">
          To start, Cassini creates a Nextcloud account called <code>cassini</code> to keep recordings
          in. Nextcloud may ask for your password.
        </p>
      {/if}

      {#if busy && progress}
        <p class="text-xs text-base-content/70" aria-live="polite">
          <span class="loading loading-spinner loading-xs align-middle" aria-hidden="true"></span>
          {progress}
        </p>
      {/if}

      {#if actionError}
        <p class="text-xs break-words text-error" role="alert">{actionError}</p>
      {/if}

      <div class="mt-1 flex flex-wrap items-center justify-end gap-2">
        {#if plan.blocked || plan.unavailable}
          <button
            class="btn btn-primary"
            type="button"
            bind:this={primary}
            on:click={openSettings}
          >
            Open Operator › Publish pipeline
          </button>
        {:else}
          <button
            class="btn btn-primary"
            type="button"
            disabled={busy}
            bind:this={primary}
            on:click={start}
          >
            {plan.creates ? "Create the account and start" : "Start recording"}
          </button>
        {/if}
      </div>
    </div>
  </div>
</div>

<style>
  /* Absolute, not fixed: the shell is the frame this belongs to. A fixed scrim
     would cover Nextcloud's own header and sidebar as well, which this dialog
     has no business dimming — the same reason the viewer's meeting sheet is
     positioned against the shell rather than the viewport. .cassini-shell
     carries position: relative for this. */
  .first-run-scrim {
    position: absolute;
    inset: 0;
    z-index: 40;
    display: grid;
    place-items: center;
    padding: 1rem;
    overflow: hidden;
    background-color: var(--color-base-200);
  }
  .first-run-scrim::after {
    content: "";
    position: absolute;
    inset: 0;
    background-color: color-mix(in oklch, black 30%, transparent);
  }
  .fr-backdrop {
    position: absolute;
    inset: 0;
    display: flex;
    flex-direction: column;
    filter: blur(5px);
    pointer-events: none;
    --sk-line: color-mix(in oklch, var(--color-base-content) 12%, transparent);
  }
  .fr-sk {
    display: block;
    height: 9px;
    border-radius: 3px;
    background-color: color-mix(in oklch, var(--color-base-content) 22%, transparent);
  }
  .fr-sk-tabs {
    display: flex;
    flex: none;
    height: 34px;
    background-color: var(--color-base-100);
    border-bottom: 1px solid var(--sk-line);
  }
  .fr-sk-tab {
    display: flex;
    align-items: center;
    padding: 0 14px;
  }
  .fr-sk-tab.current {
    background-color: var(--color-base-200);
  }
  .fr-sk-body {
    display: flex;
    flex: 1;
    min-height: 0;
  }
  .fr-sk-rail {
    display: flex;
    flex-direction: column;
    width: 268px;
    flex: none;
    padding: 16px 0;
    background-color: var(--color-base-100);
  }
  .fr-sk-label {
    width: 44px;
    height: 7px;
    margin: 0 16px 12px;
  }
  .fr-sk-gap {
    margin-top: 22px;
  }
  .fr-sk-room {
    display: flex;
    align-items: center;
    gap: 8px;
    height: 32px;
    padding: 0 16px;
  }
  .fr-sk-room.current {
    background-color: color-mix(in oklch, var(--color-primary) 35%, transparent);
  }
  .fr-sk-count {
    width: 10px;
    margin-left: auto;
  }
  .fr-sk-check {
    width: 13px;
    height: 13px;
    flex: none;
  }
  .fr-sk-main {
    display: flex;
    flex-direction: column;
    flex: 1;
    min-width: 0;
    background-color: var(--color-base-200);
  }
  .fr-sk-search {
    display: block;
    height: 34px;
    margin: 12px 20px 0;
    border: 1px solid color-mix(in oklch, var(--color-base-content) 25%, transparent);
    border-radius: 5px;
  }
  .fr-sk-result {
    width: 200px;
    height: 7px;
    margin: 14px 20px 0;
  }
  .fr-sk-month {
    width: 110px;
    height: 7px;
    margin: 32px 20px 14px;
  }
  .fr-sk-row {
    display: flex;
    align-items: center;
    gap: 14px;
    height: 60px;
    margin: 0 20px;
    border-bottom: 1px solid var(--sk-line);
  }
  .fr-sk-row-text {
    display: grid;
    gap: 8px;
  }
  .fr-sk-title {
    height: 10px;
  }
  .fr-sk-meta {
    width: 110px;
    height: 7px;
  }
  .fr-sk-duration {
    width: 22px;
    margin-left: auto;
  }
  @media (max-width: 640px) {
    .fr-sk-rail {
      display: none;
    }
  }

  .fr-problem {
    display: flex;
    align-items: flex-start;
    gap: 10px;
    padding: 10px 14px;
    font-size: 14px;
    line-height: 1.5;
    color: var(--color-base-content);
    background-color: color-mix(in oklch, var(--color-warning) 12%, var(--color-base-100));
    border: 1px solid color-mix(in oklch, var(--color-warning) 45%, var(--color-base-100));
    border-radius: var(--radius-box, 0.5rem);
  }
  .fr-problem :global(.fr-problem-icon) {
    flex: none;
    margin-top: 2px;
    color: var(--color-warning);
  }

  .fr-opt {
    display: grid;
    gap: 4px;
    padding: 10px 12px;
    text-align: left;
    color: var(--color-base-content);
    background-color: var(--color-base-200);
    border: 1px solid color-mix(in oklch, var(--color-base-content) 16%, var(--color-base-200));
    border-radius: var(--radius-box, 0.5rem);
  }
  .fr-opt-title {
    font-size: 13.5px;
    font-weight: 600;
  }
  .fr-opt-desc {
    font-size: 12.5px;
    line-height: 1.5;
    color: color-mix(in oklch, var(--color-base-content) 65%, transparent);
  }

  .first-run-card {
    position: relative;
    z-index: 1;
    container-type: inline-size;
    max-height: 100%;
    overflow-y: auto;
  }
</style>
