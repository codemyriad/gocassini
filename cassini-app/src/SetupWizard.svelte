<script lang="ts">
  import { createEventDispatcher, onMount } from "svelte";
  import { HardDrive, Info, Lock, RefreshCw, TriangleAlert } from "@lucide/svelte";
  import PasswordReveal from "./PasswordReveal.svelte";
  import { OperatorClient, OperatorHttpError } from "./operator/client";
  import { NcSetupError, isSetupAvailable, nextcloudUrl } from "./operator/ncSetup";
  import { runModeSetup } from "./operator/runModeSetup";
  import { notifySetupChanged } from "./operator/setupSignal";
  import {
    migrationFacts,
    modeCards,
    recordingAccessLabel,
  } from "./operator/storageWizard";
  import type {
    StorageModeOption,
    StorageStatus,
    StorageTransitionPreview,
  } from "./operator/types";

  // Choosing where Cassini keeps recordings (D-708).
  //
  // Cassini used to answer this itself: an install that recorded nothing fell
  // back to the deps-free model and wrote that down, permanently, on its first
  // healthy enable. `default` is the model in which every account can read every
  // recording, and nobody had asked for it — so the fallback is gone and this
  // is what replaces it.
  //
  //   1 Review     what is on this Nextcloud, for BOTH models. The counts are
  //                the fact the choice actually turns on.
  //   2 Choose     available -> use it; blocked -> scaffold it first, which is
  //                the spec's rule about prerequisites.
  //   3 Carry over ONLY when the answer would differ.
  //   4 Confirm    the operator's own numbers, then one click.
  //
  // Every sentence here that describes this instance comes from the operator:
  // it is the layer that knows the folder id, the group names, which
  // prerequisite is absent and what is in each root. This component decides when
  // to ask, and nothing else.

  export let operatorClient: OperatorClient | null = null;

  // `decided` is what tears this component down: the Setup surface re-reads the
  // verdict and mounts the settled panel, rather than leaving a wizard on screen
  // asking a question that has been answered.
  const dispatch = createEventDispatcher<{ decided: void }>();

  let status: StorageStatus | null = null;
  let loading = true;
  let loadError = "";
  let actionError = "";
  let progress = "";
  let busy = false;

  // The mode being decided about, once one is picked.
  let target: StorageModeOption | null = null;
  let preview: StorageTransitionPreview | null = null;
  let previewing = false;
  let previewError = "";
  // previewToken orders the in-flight previews. Changing a control fires a new
  // one, and without this the slower of two answers wins — leaving numbers on
  // screen that describe a policy the button will not send.
  let previewToken = 0;

  // The service account's credential, when a scaffold run just created it. It is
  // component state and nothing else: it exists nowhere on disk, at either end.
  let credential: { user: string; password: string } | null = null;
  // decidedPending holds the hand-over to the settled panel until the password
  // above has been acknowledged, because that hand-over unmounts it.
  let decidedPending = false;

  // rememberCredential pulls the service account's password off a FAILED setup
  // run. runSetupPlan attaches whatever it produced before it threw, because a
  // run that created the account and then failed on a later step has minted a
  // credential that exists nowhere else — not in the operator, not on disk, not
  // in Nextcloud in any readable form.
  function rememberCredential(error: unknown): void {
    if (error instanceof NcSetupError && error.outcome?.password) {
      credential = { user: error.outcome.createdAccount, password: error.outcome.password };
    }
  }

  function acknowledgeCredential(): void {
    credential = null;
    if (decidedPending) {
      decidedPending = false;
      dispatch("decided");
    }
  }

  onMount(() => {
    void load();
  });

  // load reads the operator's record, and asks it to LOOK when it has not.
  //
  // The operator probes on the AppAPI enabled edge, so an install that has been
  // restarted — or simply opened before that edge fired — has a record with no
  // probe in it, and every mode reads as unavailable. That is exactly the state
  // this wizard exists for, so it re-checks rather than reporting "disable and
  // re-enable the app" to somebody who has just installed it.
  async function load(): Promise<void> {
    if (!operatorClient) {
      return;
    }
    loading = true;
    loadError = "";
    actionError = "";
    try {
      status = await operatorClient.getStorage();
      if (needsProbe(status)) {
        progress = "Looking at this Nextcloud…";
        status = await operatorClient.recheckStorage();
      }
    } catch (error) {
      loadError = asMessage(error);
    } finally {
      progress = "";
      loading = false;
    }
  }

  // needsProbe is "the operator has never looked", which it reports as both
  // modes unavailable with an unprobed archive on each. It is deliberately not
  // keyed on `mode === ""`: an install can have a mode and no probe.
  function needsProbe(next: StorageStatus): boolean {
    return next.modes.length > 0 && next.modes.every((option) => !option.archive.probed);
  }

  async function choose(option: StorageModeOption): Promise<void> {
    if (busy) {
      return;
    }
    actionError = "";
    target = option;
    preview = null;
    previewError = "";
    await loadPreview(option);
  }

  // loadPreview asks what choosing this mode would do, including whether the
  // destination contains artefacts that require overwrite confirmation.
  async function loadPreview(option: StorageModeOption): Promise<void> {
    if (!operatorClient) {
      return;
    }
    previewing = true;
    previewError = "";
    const asked = option.mode;
    previewToken += 1;
    const token = previewToken;
    try {
      const next = await operatorClient.previewStorageSwitch(option.mode === "access_controlled");
      if (token === previewToken && target?.mode === asked) {
        preview = next.preview;
      }
    } catch (error) {
      if (token === previewToken && target?.mode === asked) {
        previewError = asMessage(error);
      }
    } finally {
      if (token === previewToken) {
        previewing = false;
      }
    }
  }

  function cancel(): void {
    target = null;
    preview = null;
    previewError = "";
  }

  async function scaffold(option: StorageModeOption): Promise<void> {
    if (!operatorClient || busy) {
      return;
    }
    busy = true;
    actionError = "";
    progress = "";
    try {
      const result = await runModeSetup(operatorClient, option, (message) => (progress = message));
      status = result.status;
      if (result.createdAccount && result.password) {
        credential = { user: result.createdAccount, password: result.password };
      }
      // The shell re-reads its own setup verdict: this instance is not the one
      // it looked at when it mounted.
      notifySetupChanged();
      if (!result.finished) {
        return;
      }
      // The plan is stale the moment it succeeds, and the mode this run built
      // may now be available — so the cards re-render from the refreshed record
      // rather than from what was on screen.
      target = null;
    } catch (error) {
      actionError = asMessage(error);
      // A run that failed AFTER creating the account still minted a password,
      // and it exists nowhere else. Show it with the error rather than losing it
      // to a Team folder that 404'd three steps later.
      rememberCredential(error);
      try {
        status = await operatorClient.getStorage();
      } catch {
        // Keep what we had; the error is the thing worth showing.
      }
    } finally {
      busy = false;
      progress = "";
    }
  }

  async function confirm(): Promise<void> {
    if (!operatorClient || !target || busy) {
      return;
    }
    busy = true;
    actionError = "";
    progress = "Applying…";
    try {
      status = await operatorClient.putStorage(
        target.mode === "access_controlled",
        // Sent only when the administrator was asked. The operator refuses a
        // policy-free switch that finds a choice under its own lock, and a
        // request carrying an answer is taken to have been answered by a person.
        preview?.overwrite_required === true,
      );
      target = null;
      preview = null;
      notifySetupChanged();
      // The Setup surface swaps this component for the settled panel, which
      // unmounts the one-time password with it — so the hand-over waits until
      // the administrator has acknowledged it. There is no second chance at
      // that string.
      if (!credential) {
        dispatch("decided");
      } else {
        decidedPending = true;
      }
    } catch (error) {
      actionError = asMessage(error);
      // A 409 here is the operator saying a choice appeared between the preview
      // and the switch. Re-previewing is what puts the controls on screen,
      // rather than leaving the administrator with a refusal and no way to
      // answer it. `target` is still set: the success path is what clears it.
      try {
        status = await operatorClient.getStorage();
      } catch {
        // Keep what we had.
      }
      if (target) {
        await loadPreview(target);
      }
    } finally {
      busy = false;
      progress = "";
    }
  }

  function asMessage(error: unknown): string {
    if (error instanceof NcSetupError) {
      switch (error.reason) {
        case "cancelled":
          return "Setup was cancelled — Nextcloud needs you to confirm your password before it will make these changes.";
        case "unavailable":
          return `${error.message} Open Cassini from Nextcloud's own menu, or run the commands below instead.`;
        default:
          return error.step ? `${error.message} (at: ${error.step})` : error.message;
      }
    }
    if (error instanceof OperatorHttpError) {
      if (error.status === 404) {
        // AppAPI learns an ExApp's routes from the manifest it was REGISTERED
        // with, so an installation still on an older manifest 404s every request
        // this tab makes — and the symptom says nothing about the cause.
        return (
          "Cassini’s setup service could not be found. If you just updated Cassini, check that " +
          "the update has finished in Nextcloud’s External Apps page, then reload Setup."
        );
      }
      return error.message;
    }
    return error instanceof Error ? error.message : String(error);
  }

  $: cards = modeCards(status);
  $: setupAvailable = isSetupAvailable();
  $: facts = migrationFacts(preview);
  $: needsOverwriteConfirmation = preview?.overwrite_required === true;
</script>

<section class="rounded-box border border-base-300 bg-base-100 shadow-sm">
  <header class="flex items-start justify-between gap-3 px-4 py-3">
    <div class="min-w-0">
      <h2 class="font-semibold">Who should be able to see recordings?</h2>
      <p class="text-xs text-base-content/60">
        Recordings are saved in Nextcloud. Choose who can see them, and we’ll help you finish setup.
        You can change this later.
      </p>
    </div>
    <button
      class="btn btn-ghost btn-sm btn-square"
      type="button"
      disabled={busy}
      on:click={load}
      aria-label="Look at this Nextcloud again"
    >
      <RefreshCw size={16} aria-hidden="true" />
    </button>
  </header>

  {#if loadError}
    <div class="px-4 py-4"><div class="alert alert-error text-sm">{loadError}</div></div>
  {:else if loading}
    <div class="flex items-center justify-center gap-2 p-6 text-sm text-base-content/60">
      <span class="loading loading-spinner loading-xs" aria-hidden="true"></span>
      {progress || "Loading…"}
    </div>
  {:else if !status}
    <div class="p-6 text-center text-sm text-base-content/60">No storage settings available.</div>
  {:else}
    <div class="grid gap-4 p-4">
      {#if credential}
        <!-- Shown before anything else, and it does not go away on its own:
             there is no second chance at this string. -->
        <PasswordReveal
          user={credential.user}
          password={credential.password}
          resetOcc={status.service_account.reset_occ}
          on:acknowledge={acknowledgeCredential}
        />
      {/if}

      {#if !status.awaiting_choice}
        <!-- A mode is in force that nobody chose: a previous build recorded it,
             or a first decision was interrupted. The archive is where it says
             and reads work; what is missing is somebody's agreement. -->
        <div class="flex items-start gap-3 rounded-box border border-base-300 bg-base-200 p-3 text-sm" role="status">
          <Info size={16} class="mt-0.5 shrink-0 text-primary" aria-hidden="true" />
          <span class="break-words">
            Please review your recording access settings.
            The current setting is <strong>{recordingAccessLabel(status.mode)}</strong>.
            Confirm it below, or choose another option. Existing recordings remain available.
          </span>
        </div>
      {/if}

      <!-- Step 1. What is actually on this Nextcloud, for BOTH models. This is
           the screen the whole wizard exists for: the choice turns on what is
           already in each folder, and until now those counts never left the
           operator. -->
      <div class="grid gap-3 lg:grid-cols-2" role="radiogroup" aria-label="Recording storage mode">
        {#each cards as card (card.mode)}
          <div
            class="grid content-start gap-2 rounded-box border p-3 transition {target?.mode === card.mode
              ? 'border-primary bg-primary/15 ring-1 ring-inset ring-primary'
              : 'border-base-300 bg-base-200'}"
          >
            <div class="flex items-center gap-2">
              {#if card.mode === "access_controlled"}
                <Lock size={16} class="shrink-0 text-base-content/60" aria-hidden="true" />
              {:else}
                <HardDrive size={16} class="shrink-0 text-base-content/60" aria-hidden="true" />
              {/if}
              <h3 class="text-sm font-semibold">{card.label}</h3>
              {#if card.active}
                <span class="badge badge-ghost badge-sm">In use, unconfirmed</span>
              {/if}
            </div>

            <p class="text-xs text-base-content/70">{card.summary}</p>

            <p class="text-xs text-base-content/60">Recordings in this location: {card.contents}</p>
            <details class="text-xs text-base-content/60">
              <summary class="w-fit cursor-pointer">Storage location</summary>
              <code class="mt-1 block break-all">{card.root}</code>
            </details>

            {#if card.blocker}
              <div class="grid gap-2 rounded-box border border-warning/50 bg-warning/10 p-2">
                <p class="text-xs break-words">{card.blocker}</p>
                {#if card.action === "scaffold"}
                  <ul class="grid gap-1 text-xs">
                    {#each status.modes.find((entry) => entry.mode === card.mode)?.setup ?? [] as step (step.id)}
                      <li class="flex items-start gap-1.5">
                        <span class="mt-0.5 shrink-0 text-base-content/40" aria-hidden="true">•</span>
                        <span class="break-words">
                          {step.title}
                          {#if !step.browser}
                            <span class="text-base-content/60"
                              >— Cassini will try; Nextcloud may ask you to do this one yourself</span
                            >
                          {/if}
                        </span>
                      </li>
                    {/each}
                  </ul>
                {/if}
              </div>
            {/if}

            <button
              class="btn btn-sm mt-1 w-full {card.action === 'use' ? 'btn-primary' : 'btn-outline'}"
              type="button"
              role="radio"
              aria-checked={target?.mode === card.mode}
              disabled={busy || credential !== null || card.action === "blocked"}
              on:click={() => {
                const option = status?.modes.find((entry) => entry.mode === card.mode);
                if (!option) return;
                if (card.action === "scaffold") {
                  void scaffold(option);
                } else {
                  void choose(option);
                }
              }}
            >
              {card.actionLabel}
            </button>
          </div>
        {/each}
      </div>

      {#if status.installs.length > 0}
        <div class="grid gap-2 rounded-box border border-base-300 bg-base-200 p-3">
          <p class="text-sm font-semibold">Nextcloud apps</p>
          {#each status.installs as install (install.app)}
            <div class="grid gap-1">
              <p class="text-xs {install.ok ? 'text-success' : 'text-warning'}">
                {install.app}: {install.ok ? "installed and enabled" : "not installed"}
              </p>
              {#if install.detail}
                <p class="text-xs break-words text-base-content/70">{install.detail}</p>
              {/if}
            </div>
          {/each}
          <a class="btn btn-sm btn-outline w-fit" href={nextcloudUrl("/settings/apps")} target="_top">
            Open Nextcloud's Apps page
          </a>
        </div>
      {/if}

      {#if target}
        <!-- Steps 3 and 4. The carry-over controls appear only when the operator
             says the answer would differ; the facts under them are its own
             plan, not arithmetic done here. -->
        <div
          class="grid gap-3 rounded-box border border-primary bg-primary/5 p-3"
          role="group"
          aria-label="Confirm the storage mode"
        >
          <p class="text-sm font-semibold">Allow access for {recordingAccessLabel(target.mode).toLowerCase()}?</p>
          <p class="text-xs break-words text-base-content/80">{target.consequence}</p>

          {#if previewing}
            <p class="text-xs text-base-content/60" aria-live="polite">
              <span class="loading loading-spinner loading-xs align-middle" aria-hidden="true"></span>
              Working out what this would do…
            </p>
          {:else if previewError}
            <p class="text-xs break-words text-warning">
              Cassini could not work out what this would do ({previewError}). It checks again before
              it writes anything.
            </p>
          {:else if preview}
            {#if needsOverwriteConfirmation}
              <div class="rounded-box border border-warning bg-warning/10 p-2 text-xs">
                <p class="font-semibold">These destination files will be overwritten or removed:</p>
                <p class="mt-1 break-all">{preview.overwrite_names.join(", ")}</p>
              </div>
            {/if}
            {#if !preview.source_readable}
              <!-- The plan's counts are zero for a tree nobody could list, and
                   rendering them as facts would say "nothing moves" on the
                   strength of a question nobody managed to ask. That is the
                   exact shape QA reported against the first pass. -->
              <p class="rounded-box bg-base-100/60 p-2 text-xs break-words text-base-content/80">
                Cassini could not read <code class="break-all">{preview.source_root}</code>, so it
                cannot say what this would do. It checks again before it writes anything.
              </p>
            {:else}
              <ul class="grid gap-1 rounded-box bg-base-100/60 p-2 text-xs">
                {#each facts as fact (fact)}
                  <li class="flex items-start gap-1.5 break-words">
                    <span class="mt-0.5 shrink-0 text-base-content/40" aria-hidden="true">•</span>
                    <span>{fact}</span>
                  </li>
                {/each}
              </ul>
            {/if}
            {#each preview.warnings as warning (warning)}
              <p class="flex items-start gap-1.5 text-xs break-words text-warning">
                <span class="mt-0.5 shrink-0" aria-hidden="true">•</span>
                <span>{warning}</span>
              </p>
            {/each}
          {/if}

          <div class="flex flex-wrap items-center gap-2">
            <!-- Disabled while a preview is in flight: confirming then would
                 commit to a policy whose consequences are not yet on screen, and
                 the operator would refuse a choice the administrator never saw. -->
            <button
              class="btn btn-sm btn-primary"
              type="button"
              disabled={busy || previewing || credential !== null}
              on:click={confirm}
            >
              {#if busy}
                <span class="loading loading-spinner loading-xs" aria-hidden="true"></span>
                {progress || "Applying…"}
              {:else}
                Confirm recording access
              {/if}
            </button>
            <button class="btn btn-sm btn-ghost" type="button" disabled={busy} on:click={cancel}>
              Cancel
            </button>
          </div>
        </div>
      {/if}

      {#if busy && !target}
        <p class="text-xs text-base-content/70" aria-live="polite">
          <span class="loading loading-spinner loading-xs align-middle" aria-hidden="true"></span>
          {progress || "Working…"}
        </p>
      {/if}

      {#if !setupAvailable}
        <!-- The standalone build, or a page Nextcloud's own scripts did not
             reach. Cassini cannot act as the administrator there, so say so
             before a button is pressed rather than after. -->
        <p class="text-xs break-words text-warning">
          This page cannot make changes to Nextcloud itself — open Cassini from Nextcloud's own menu
          to set up a mode from here.
        </p>
      {/if}

      {#if actionError}
        <div class="alert alert-error items-start gap-3 text-sm" role="alert">
          <TriangleAlert size={16} class="mt-0.5 shrink-0" aria-hidden="true" />
          <div class="grid gap-1">
            <p class="font-semibold">That did not finish.</p>
            <p class="text-xs break-words">{actionError}</p>
          </div>
        </div>
      {/if}
    </div>
  {/if}
</section>
