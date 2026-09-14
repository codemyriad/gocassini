<script lang="ts">
  import { createEventDispatcher, onMount } from "svelte";
  import type { OperatorClient } from "./operator/client";
  import type { FirstRunPlan } from "./operator/firstRun";
  import { NcSetupError, runSetupPlan } from "./operator/ncSetup";
  import { notifySetupChanged } from "./operator/setupSignal";

  // The one dialog a fresh install shows (D-756).
  //
  // Cassini used to open on a Setup tab and a wizard, and refused to record
  // until somebody answered it. The operator now resolves the storage mode when
  // it is enabled and records from then on, so there is no question left — only
  // two things worth saying once: who will see recordings, and that Nextcloud
  // needs the administrator's own session to create the account they are kept
  // in.
  //
  // Every decision this renders was taken in operator/firstRun.ts, which is
  // where they are tested. This component performs them and nothing else.
  export let operatorClient: OperatorClient;
  export let plan: FirstRunPlan;

  // `done` closes the dialog; `settings` is the reader choosing to change the
  // audience before anything is created. The shell owns which surface is
  // showing, so this asks rather than acts.
  const dispatch = createEventDispatcher<{ done: void; settings: void }>();

  let busy = false;
  let progress = "";
  let actionError = "";
  let primary: HTMLButtonElement | null = null;
  let secondary: HTMLButtonElement | null = null;

  onMount(() => {
    // The dialog covers the app, so the focus has to be inside it. The primary
    // button unless there is none — the standalone build has only the other
    // one. No focus trap: it is two buttons, and a half-built trap is worse
    // than the browser's own behaviour.
    (primary ?? secondary)?.focus();
  });

  // openSettings leaves for Operator › Settings without creating anything and
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
        await operatorClient.recheckStorage();
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
  <div
    class="first-run-card w-full max-w-lg rounded-box border border-base-300 bg-base-100 p-5 shadow-lg sm:p-6"
    role="dialog"
    aria-modal="true"
    aria-labelledby="cassini-first-run-title"
  >
    <div class="grid gap-3">
      <!-- The title is a claim, so it is made only where it is true: an install
           whose account is missing with no way to make it here is not ready to
           record, and saying it was is what this dialog got wrong. -->
      <h2 id="cassini-first-run-title" class="text-lg font-bold">
        {plan.blocked ? "Cassini can't record yet" : "Cassini is ready to record"}
      </h2>

      <!-- Who can see recordings, before anything about how Cassini works, and
           the rooms alongside the recordings: the room name travels with every
           published recording, so an audience sentence that named only the
           recordings would be describing half of what is visible (11 September
           product decision). -->
      <p class="text-sm text-base-content/80">
        Recordings, and the names of the rooms they came from, will be visible to
        <strong>anyone with an account on this Nextcloud</strong>. You
        can limit them to the people in each call at any time, in Operator › Settings.
      </p>

      {#if plan.blocked}
        <!-- Nothing here can fix it: the operator says the account is missing
             and offered no step this page could run. The settings section is
             where the account row and the full diagnosis are. -->
        <p class="text-sm text-base-content/80">
          Cassini needs a Nextcloud account to keep recordings in, and this page has no way to
          create it. Open Operator › Settings to see what is missing.
        </p>
      {:else if plan.creates}
        <!-- Said only where it is going to happen: an install whose account the
             operator already made is not about to make another one. -->
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
        {#if plan.blocked}
          <!-- One way on, and it is the one that leads somewhere: a Start
               button here would acknowledge a first run that never happened. -->
          <button
            class="btn btn-sm btn-primary"
            type="button"
            bind:this={primary}
            on:click={openSettings}
          >
            Open Operator › Settings
          </button>
        {:else}
          <button
            class="btn btn-sm btn-ghost"
            type="button"
            disabled={busy}
            bind:this={secondary}
            on:click={openSettings}
          >
            Change who can see first
          </button>
          {#if plan.unavailable}
            <!-- The standalone build, or a page Nextcloud's own scripts did not
                 reach. Cassini cannot act as the administrator there, so the
                 sentence stands in for a button that would be refused. -->
            <p class="text-xs break-words text-warning">
              This page cannot make the changes itself. Open Cassini from Nextcloud's own menu.
            </p>
          {:else}
            <button
              class="btn btn-sm btn-primary"
              type="button"
              disabled={busy}
              bind:this={primary}
              on:click={start}
            >
              {plan.creates ? "Create the account and start" : "Start"}
            </button>
          {/if}
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
    background-color: color-mix(in oklch, black 45%, transparent);
  }

  .first-run-card {
    max-height: 100%;
    overflow-y: auto;
  }
</style>
