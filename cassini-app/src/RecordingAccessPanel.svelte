<script lang="ts">
  import { onDestroy, onMount } from "svelte";
  import { Check, RefreshCw, TriangleAlert } from "@lucide/svelte";
  import PasswordReveal from "./PasswordReveal.svelte";
  import { loadConfig } from "./operator/config";
  import { OperatorClient, OperatorHttpError } from "./operator/client";
  import {
    NcSetupError,
    isSetupAvailable,
    nextcloudUrl,
    resetServiceAccountPassword,
  } from "./operator/ncSetup";
  import { runModeSetup } from "./operator/runModeSetup";
  import { notifySetupChanged } from "./operator/setupSignal";
  import {
    PARTICIPANTS,
    accessOptions,
    appsInUse,
    doneMessage,
    existingRecordingsLine,
    installButtonLabel,
    missingApps,
    modeSourceLabel,
    needsPrerequisites,
    occRecipe,
    requiredApps,
    storageCheckLine,
    storageLocation,
    switchConfirmation,
    switchSteps,
    switchingLead,
    switchingTitle,
  } from "./operator/recordingAccess";
  import type { AccessMode } from "./operator/recordingAccess";
  import type { StorageMigration, StorageStatus } from "./operator/types";

  // Operator › Settings › Who can see recordings (D-757, D-758).
  //
  // It replaces the Setup tab's "Recording storage" card, and it is the same
  // set of actions read from the other end: a card led with folder paths, a
  // mode enum and a health row, and this leads with who can see a recording.
  // Everything technical is still here, one disclosure down.
  //
  // The decisions — which option is current, what the archive line says, what a
  // switch would mean in a number — live in operator/recordingAccess.ts, which
  // is unit-tested. A .svelte file is not mounted in this repo's tests, so a
  // sentence that carries a number does not belong in one.
  //
  //   idle ─choose the other option─▶ prerequisites? ─▶ confirm ─▶ switching
  //     ▲                                  │              │           │
  //     └────── cancel / error ────────────┴──────────────┘           │
  //     └───────────────── done, re-rendered with the new mark ◀──────┘

  export let operatorClient: OperatorClient | null = null;

  let status: StorageStatus | null = null;
  let loading = true;
  let loadError = "";
  let actionError = "";
  // done is the one-line outcome of the last switch. Ordinary component state:
  // nothing reloads the page, so there is nothing for it to survive.
  let done = "";

  // Which panel is open. Null is the settled section.
  let flow: "prereqs" | "confirm" | "switching" | null = null;
  // target is the mode being switched TO. Null while a switch that this page
  // did not start is being watched, which is the one case where nothing on the
  // wire says where it is going.
  let target: AccessMode | null = null;

  let switching = false;
  let installing = false;
  let resuming = false;
  let resetting = false;
  // progress is runModeSetup's own step message, while the browser is building
  // what the mode still needs.
  let progress = "";
  // migration is the switch's progress, read by the poll below rather than from
  // the PUT — which does not answer until the whole move is done.
  let migration: StorageMigration | null = null;
  // switched records that a switch happened in THIS session, which is the only
  // moment the app can tell a recording that predates it from one that does
  // not: the operator keeps no per-recording audience to read back later.
  let switched = false;
  // credential is the service account's password, when this session just minted
  // one. It exists nowhere else, at either end, and there is no second chance
  // at it — so everything else is disabled until it is acknowledged.
  let credential: { user: string; password: string } | null = null;

  // The full report. The operator's own /status, which is a sibling of every
  // route this client calls.
  let reportUrl = "";
  try {
    reportUrl = `${loadConfig().operatorBasePath}/status`;
  } catch {
    // A panel with no configured base path cannot link to one. The rest of the
    // section still works; the client it was handed is what makes the calls.
  }

  onMount(() => {
    void load();
  });

  onDestroy(() => {
    stopPoll?.();
    stopPoll = null;
  });

  async function load(): Promise<void> {
    if (!operatorClient) {
      return;
    }
    loading = true;
    loadError = "";
    try {
      status = await operatorClient.getStorage();
      watchRunningSwitch();
    } catch (error) {
      loadError = asMessage(error);
    } finally {
      loading = false;
    }
  }

  // recheck makes the operator look at Nextcloud again. The setup writes happen
  // in the browser, and an app installed from Nextcloud's own Apps page happens
  // outside Cassini entirely, so without this the section goes on reporting
  // what was missing before the administrator fixed it.
  async function recheck(): Promise<void> {
    if (!operatorClient || busy) {
      return;
    }
    loading = true;
    loadError = "";
    actionError = "";
    try {
      status = await operatorClient.recheckStorage();
      watchRunningSwitch();
    } catch (error) {
      loadError = asMessage(error);
    } finally {
      loading = false;
    }
  }

  function choose(mode: AccessMode): void {
    if (busy || status === null || status.mode === mode) {
      return;
    }
    actionError = "";
    done = "";
    target = mode;
    // The checklist is only for the two apps, and only while one is missing.
    // Everything else this mode needs is done during the switch.
    flow = needsPrerequisites(status, mode) ? "prereqs" : "confirm";
  }

  function cancel(): void {
    if (switching) {
      return;
    }
    flow = null;
    target = null;
    progress = "";
  }

  // installApps is the operator's own attempt at the app installs. It works on
  // releases that predate Nextcloud's password-confirmation hardening, and
  // where an administrator set a bypass range; where it does not, Nextcloud's
  // Apps page is the button beside it and the recheck notices the result.
  async function installApps(): Promise<void> {
    if (!operatorClient || busy) {
      return;
    }
    installing = true;
    actionError = "";
    try {
      await operatorClient.installStorageApps();
      status = await operatorClient.recheckStorage();
      notifySetupChanged();
      if (target !== null && !needsPrerequisites(status, target)) {
        flow = "confirm";
      }
    } catch (error) {
      actionError = asMessage(error);
    } finally {
      installing = false;
    }
  }

  // confirmSwitch is the only thing that moves recordings, and it is reachable
  // only from the confirmation panel.
  //
  // It builds what the mode still needs first — the Team folder, its mappings,
  // the ACL, the manager — through runModeSetup, which is the sequence the
  // Setup tab used and which keeps the apps-first ordering that is easy to get
  // wrong. Then one PUT, which blocks for the length of the move while the poll
  // below reads its progress.
  async function confirmSwitch(): Promise<void> {
    if (!operatorClient || target === null || switching) {
      return;
    }
    const mode = target;
    switching = true;
    actionError = "";
    done = "";
    migration = null;
    flow = "switching";
    stopPoll?.();
    stopPoll = pollMigration(null);
    try {
      const option = status?.modes.find((row) => row.mode === mode) ?? null;
      if (option && !option.available && option.setup.length > 0) {
        const result = await runModeSetup(operatorClient, option, (message) => {
          progress = message;
        });
        status = result.status;
        if (result.createdAccount && result.password) {
          credential = { user: result.createdAccount, password: result.password };
        }
        if (!result.finished) {
          // An app install the operator could not perform is still outstanding,
          // and everything left lives inside those apps. Back to the checklist,
          // which is where the two buttons for that are.
          notifySetupChanged();
          flow = "prereqs";
          return;
        }
      }
      // Sent without the overwrite answer the client can carry. The operator
      // still refuses a switch that finds artefacts at the destination, and
      // that guard stays; this section does not offer a way past it, because a
      // destination that already holds recordings is the "both places" state
      // and is resolved before anyone reaches a switch. The refusal is the
      // operator's own sentence, shown as an error, and it stops there.
      status = await operatorClient.putStorage(mode === PARTICIPANTS);
      switched = true;
      migration = null;
      flow = null;
      target = null;
      done = doneMessage(mode);
      notifySetupChanged();
    } catch (error) {
      actionError = asMessage(error);
      flow = null;
      target = null;
      // Re-read rather than trusting the pre-switch snapshot. Most failures
      // change nothing, but one does not: a transition that fails AFTER moving
      // the archive has already changed the mode the operator is using, and its
      // message says so.
      try {
        status = await operatorClient.getStorage();
      } catch {
        // Keep what we had; the switch error is the thing worth showing.
      }
    } finally {
      stopPoll?.();
      stopPoll = null;
      switching = false;
      progress = "";
    }
  }

  // resume finishes a switch that stopped part way. One action, whichever half
  // failed: the operator's invariant makes them the same shape, and the archive
  // is complete at the mode's own root either way.
  async function resume(): Promise<void> {
    if (!operatorClient || busy) {
      return;
    }
    resuming = true;
    actionError = "";
    done = "";
    try {
      status = await operatorClient.finishStorageMigration();
      notifySetupChanged();
    } catch (error) {
      actionError = asMessage(error);
      try {
        status = await operatorClient.getStorage();
      } catch {
        // Keep what we had; the error is the thing worth showing.
      }
    } finally {
      resuming = false;
    }
  }

  // setPassword mints a new password for the service account and shows it once.
  // Cassini never sees it: it is generated in this browser and set through
  // Nextcloud's own API on the administrator's session.
  async function setPassword(): Promise<void> {
    if (!status || busy) {
      return;
    }
    resetting = true;
    actionError = "";
    credential = null;
    try {
      const user = status.service_account.user;
      credential = { user, password: await resetServiceAccountPassword(user) };
    } catch (error) {
      actionError = asMessage(error);
      if (error instanceof NcSetupError && error.outcome?.password) {
        // A run that created the account and then failed has minted a password
        // that exists nowhere else. Show it with the error rather than losing it.
        credential = { user: error.outcome.createdAccount, password: error.outcome.password };
      }
    } finally {
      resetting = false;
    }
  }

  // --- Progress -------------------------------------------------------------
  //
  // The PUT blocks for the whole move, so progress is read by a second,
  // parallel reader of the same operator. It touches `migration` and nothing
  // else: the PUT's own answer is the authoritative status, and a snapshot
  // taken mid-move must not replace it.

  const POLL_MS = 2000;
  let stopPoll: (() => void) | null = null;

  function pollMigration(onFinished: (() => void) | null): () => void {
    let stopped = false;
    let timer: ReturnType<typeof setTimeout> | null = null;
    const tick = async () => {
      if (stopped || !operatorClient) {
        return;
      }
      try {
        const snapshot = await operatorClient.getStorage();
        if (stopped) {
          return;
        }
        migration = snapshot.migration;
        if (snapshot.migration === null && onFinished !== null) {
          onFinished();
          return;
        }
      } catch {
        // Swallowed on purpose: the action's own call is what reports a
        // failure, and a poll that lost one round trip has nothing to say.
      }
      if (!stopped) {
        timer = setTimeout(() => void tick(), POLL_MS);
      }
    };
    timer = setTimeout(() => void tick(), POLL_MS);
    return () => {
      stopped = true;
      if (timer !== null) {
        clearTimeout(timer);
      }
    };
  }

  // watchRunningSwitch picks up a switch that was already running when this
  // page opened. The move survives a closed tab, so coming back to a section
  // that said nothing about it would be the tab's failure, not the switch's.
  function watchRunningSwitch(): void {
    if (status?.migration == null || switching) {
      return;
    }
    migration = status.migration;
    flow = "switching";
    stopPoll?.();
    stopPoll = pollMigration(() => {
      stopPoll?.();
      stopPoll = null;
      flow = null;
      void load();
    });
  }

  function asMessage(error: unknown): string {
    if (error instanceof NcSetupError) {
      switch (error.reason) {
        case "cancelled":
          return "Nextcloud needs you to confirm your password before it will make these changes.";
        case "unavailable":
          return `${error.message} Open Cassini from Nextcloud's own menu, or run the commands under "Details for administrators" instead.`;
        default:
          return error.step ? `${error.message} (at: ${error.step})` : error.message;
      }
    }
    if (error instanceof OperatorHttpError) {
      if (error.status === 404) {
        // AppAPI learns an ExApp's routes from the manifest it was REGISTERED
        // with, so an installation updated in place from a version that
        // predates these routes 404s every request this section makes.
        return (
          "This Nextcloud does not know about Cassini's storage routes, which happens when the app " +
          "was updated in place from a version that predates them. Re-register the app in Nextcloud " +
          "(External Apps, remove and add Cassini again, keeping its data)."
        );
      }
      return error.message;
    }
    return error instanceof Error ? error.message : String(error);
  }

  $: options = accessOptions(status);
  $: existingLine = existingRecordingsLine(status, switched);
  $: apps = requiredApps(status);
  $: missing = missingApps(status);
  $: confirmation = switchConfirmation(status, target ?? PARTICIPANTS);
  $: steps = switchSteps(migration);
  $: place = storageLocation(status);
  $: occ = occRecipe(status);
  $: checkLine = storageCheckLine(status);
  // Whether this page can act as the administrator at all. False on the
  // standalone build, which has neither Nextcloud's scripts nor its session.
  $: setupAvailable = isSetupAvailable();
  $: busy = switching || installing || resuming || resetting || credential !== null;
</script>

<section class="rounded-box border border-base-300 bg-base-100 shadow-sm">
  <header class="flex items-start justify-between gap-3 px-4 py-3">
    <div class="min-w-0">
      <h2 class="font-semibold">Who can see recordings</h2>
      <p class="text-xs text-base-content/60">
        Applies to every recording Cassini publishes to this Nextcloud.
      </p>
    </div>
    <button
      class="btn btn-ghost btn-sm btn-square"
      type="button"
      on:click={recheck}
      disabled={loading || busy || !operatorClient}
      aria-label="Check this Nextcloud again"
    >
      <RefreshCw size={16} aria-hidden="true" />
    </button>
  </header>

  {#if loadError}
    <div class="px-4 py-4">
      <div class="alert alert-error text-sm">{loadError}</div>
    </div>
  {:else if loading}
    <div class="flex items-center justify-center p-6 text-sm text-base-content/60">Loading…</div>
  {:else if !status}
    <div class="flex items-center justify-center p-6 text-sm text-base-content/60">
      No storage settings available.
    </div>
  {:else}
    <div class="grid gap-4 p-4">
      {#if credential}
        <!-- Above everything else, and it does not go away on its own: there is
             no second chance at this string. -->
        <PasswordReveal
          user={credential.user}
          password={credential.password}
          resetOcc={status.service_account.reset_occ}
          on:acknowledge={() => (credential = null)}
        />
      {/if}

      {#if done}
        <div class="alert alert-success text-sm" role="status">{done}</div>
      {/if}

      {#if actionError}
        <div class="alert alert-error items-start gap-3 text-sm" role="alert">
          <TriangleAlert size={16} class="mt-0.5 shrink-0" aria-hidden="true" />
          <p class="break-words">{actionError}</p>
        </div>
      {/if}

      {#if !status.migration_clean}
        <!-- A switch that stopped part way. One line and one button: the
             archive is complete at the mode in force either way, so naming the
             root that holds the leftovers is detail, not news. -->
        <div class="flex flex-wrap items-center gap-3 rounded-box border border-warning bg-warning/10 p-3" role="status">
          <p class="text-sm font-semibold">A switch didn't finish.</p>
          <button
            class="btn btn-sm btn-warning"
            type="button"
            disabled={busy}
            on:click={resume}
          >
            {#if resuming}
              <span class="loading loading-spinner loading-xs" aria-hidden="true"></span>
              Resuming…
            {:else}
              Resume
            {/if}
          </button>
        </div>
      {/if}

      <!-- The two audiences. One sentence each about who can see, the current
           one marked, and choosing the other one starts the switch. -->
      <div class="grid gap-3 lg:grid-cols-2" role="radiogroup" aria-label="Who can see recordings">
        {#each options as option (option.mode)}
          <button
            class="grid content-start gap-1.5 rounded-box border p-3 text-left transition {option.current
              ? 'border-primary bg-primary/15 ring-1 ring-inset ring-primary'
              : 'border-base-300 bg-base-200 hover:border-primary/50'}"
            type="button"
            role="radio"
            aria-checked={option.current}
            disabled={busy || option.current}
            on:click={() => choose(option.mode)}
          >
            <span class="flex items-center gap-2">
              <span class="text-sm font-semibold">{option.title}</span>
              {#if option.current}
                <span class="badge badge-primary badge-sm">Current</span>
              {/if}
            </span>
            <span class="text-xs text-base-content/70">{option.description}</span>
          </button>
        {/each}
      </div>

      <!-- The fact an administrator will otherwise be surprised by later: a
           switch does not change who can see the recordings that already
           exist. It stays on screen permanently, not only after a switch. -->
      {#if existingLine}
        <p class="text-sm text-base-content/80">{existingLine}</p>
      {/if}

      {#if flow === "prereqs"}
        <!-- An inline confirmation rather than the platform's modal dialog
             element: the whole app runs inside a shadow root on Nextcloud's
             embedded page, where the top layer is the one thing whose styling
             and focus behaviour do not reliably follow it. -->
        <div
          class="grid gap-3 rounded-box border border-warning bg-warning/10 p-3"
          role="alertdialog"
          aria-label="Two Nextcloud apps are needed"
        >
          <p class="text-sm font-semibold">Two Nextcloud apps are needed</p>
          <ul class="grid gap-1.5">
            {#each apps as app (app.id)}
              <li class="flex items-center gap-2 text-xs">
                <span
                  class="badge badge-sm {app.installed
                    ? 'badge-success'
                    : 'badge-outline border-base-content/30'}"
                >
                  {app.installed ? "Installed" : "Not installed"}
                </span>
                <span>{app.name}</span>
              </li>
            {/each}
          </ul>
          <p class="text-xs break-words text-base-content/80">
            Cassini can install it for you. If Nextcloud refuses, install it from Nextcloud's Apps
            page; Cassini notices when it is there.
          </p>
          <!-- The instance-wide effect, before the install rather than after.
               An acceptance criterion since D-671 that has never shipped. -->
          <p class="rounded-box bg-base-100/60 p-2 text-xs break-words text-base-content/80">
            Everyone Group adds a group called <b>Everyone</b> to the whole of Nextcloud. It shows up
            when sharing files in other apps too, not only in Cassini.
          </p>
          <!-- One sentence, not a checklist: these are Cassini's own steps, it
               performs them during the switch, and an administrator is not
               being asked to do any of them. -->
          <p class="text-xs break-words text-base-content/60">
            Cassini does the rest itself while the switch runs: the Team folder, the group mappings,
            advanced permissions, and the manager that sets each recording's audience.
          </p>
          <div class="flex flex-wrap items-center gap-2">
            <button class="btn btn-sm btn-ghost" type="button" disabled={installing} on:click={cancel}>
              Cancel
            </button>
            <a class="btn btn-sm btn-outline" href={nextcloudUrl("/settings/apps")} target="_top">
              Open Nextcloud Apps
            </a>
            <button
              class="btn btn-sm btn-primary"
              type="button"
              disabled={installing || missing.length === 0}
              on:click={installApps}
            >
              {#if installing}
                <span class="loading loading-spinner loading-xs" aria-hidden="true"></span>
                Installing…
              {:else}
                {installButtonLabel(missing)}
              {/if}
            </button>
          </div>
        </div>
      {/if}

      {#if flow === "confirm"}
        <div
          class="grid gap-2 rounded-box border p-3 {confirmation.danger
            ? 'border-error bg-error/10'
            : 'border-warning bg-warning/10'}"
          role="alertdialog"
          aria-label={confirmation.title}
        >
          <p class="text-sm font-semibold">{confirmation.title}</p>
          {#each confirmation.lines as line (line)}
            <p class="text-xs break-words text-base-content/80">{line}</p>
          {/each}
          <p class="text-xs break-words text-base-content/70">{confirmation.pause}</p>
          <div class="flex flex-wrap items-center gap-2">
            <button class="btn btn-sm btn-ghost" type="button" on:click={cancel}>Cancel</button>
            <button
              class="btn btn-sm {confirmation.danger ? 'btn-error' : 'btn-warning'}"
              type="button"
              disabled={busy}
              on:click={confirmSwitch}
            >
              {confirmation.confirmLabel}
            </button>
          </div>
        </div>
      {/if}

      {#if flow === "switching"}
        <!-- The four real steps, in the order the operator performs them, so an
             interruption is honest: whichever step it stopped at, a complete
             archive exists somewhere. -->
        <div
          class="grid gap-3 rounded-box border border-base-300 bg-base-200 p-3"
          role="status"
          aria-live="polite"
        >
          <p class="text-sm font-semibold">{switchingTitle(target)}</p>
          <p class="text-xs break-words text-base-content/70">{switchingLead(migration)}</p>
          <ul class="grid gap-1.5">
            {#each steps as step (step.phase)}
              <li
                class="flex items-center gap-2 text-xs {step.state === 'pending'
                  ? 'text-base-content/50'
                  : 'text-base-content/80'}"
              >
                {#if step.state === "done"}
                  <Check size={14} class="shrink-0 text-success" aria-hidden="true" />
                {:else if step.state === "now"}
                  <span class="loading loading-spinner loading-xs shrink-0" aria-hidden="true"></span>
                {:else}
                  <span class="size-3.5 shrink-0 rounded-full border border-base-content/30" aria-hidden="true"></span>
                {/if}
                <span class="break-words">{step.label}</span>
                {#if step.count}
                  <span class="ml-auto font-mono text-[11px] text-base-content/60">{step.count}</span>
                {/if}
              </li>
            {/each}
          </ul>
          {#if progress}
            <p class="text-xs break-words text-base-content/60">{progress}</p>
          {/if}
        </div>
      {/if}

      <!-- Everything technical, one disclosure down and collapsed by default:
           the paths, the app ids, the enum, the account and the commands. -->
      <details class="rounded-box border border-base-300 bg-base-200 p-3">
        <summary class="cursor-pointer text-sm font-semibold">Details for administrators</summary>
        <dl class="mt-3 grid gap-3">
          {#if place.root}
            <div>
              <dt class="text-xs uppercase tracking-wide text-base-content/45">Recordings are stored in</dt>
              <dd class="text-sm break-words">
                <code class="break-all">{place.root}</code>
                <span class="text-base-content/70">({place.container})</span>
              </dd>
            </div>
          {/if}
          <div>
            <dt class="text-xs uppercase tracking-wide text-base-content/45">Nextcloud apps in use</dt>
            <dd class="text-sm">{appsInUse(status)}</dd>
          </div>
          <div>
            <dt class="text-xs uppercase tracking-wide text-base-content/45">Storage check</dt>
            <dd class="text-sm">
              <span class={status.ok ? "text-success" : "text-warning"}>{checkLine}</span>
              {#if reportUrl}
                <a
                  class="link link-hover text-base-content/70"
                  href={reportUrl}
                  target="_blank"
                  rel="noopener noreferrer">Full report</a
                >
              {/if}
            </dd>
            {#if !status.ok && status.detail}
              <!-- The operator's own sentence, verbatim, so this section and
                   the container log read the same. -->
              <dd class="mt-1 rounded-box bg-base-100 p-2 font-mono text-xs break-words text-base-content/70">
                {status.detail}
              </dd>
            {/if}
          </div>
          <div>
            <dt class="text-xs uppercase tracking-wide text-base-content/45">The rule Cassini has recorded</dt>
            <dd class="text-sm break-words">
              <code>{status.mode === "" ? "none" : status.mode}</code>
              <span class="text-base-content/70">{modeSourceLabel(status.mode_source)}</span>
            </dd>
          </div>
          <div>
            <dt class="text-xs uppercase tracking-wide text-base-content/45">Service account</dt>
            <dd class="text-sm break-words text-base-content/80">
              Recordings are written and read by a Nextcloud account called
              <code>{status.service_account.user}</code>. Cassini doesn't need its password and
              doesn't keep one.
              <button
                class="link link-hover font-medium"
                type="button"
                disabled={busy || !setupAvailable}
                on:click={setPassword}
              >
                {#if resetting}
                  Setting a password…
                {:else}
                  Set a password
                {/if}
              </button>
              if you want to sign in as it.
            </dd>
          </div>
          {#if occ.length > 0}
            <div>
              <dt class="text-xs uppercase tracking-wide text-base-content/45">Do it by hand instead</dt>
              <dd class="text-sm">
                The same change, as <code>occ</code> commands:
                <pre
                  class="m-0 mt-1 overflow-x-auto rounded-box bg-base-100 p-2 font-mono text-xs leading-relaxed">{occ.join(
                    "\n",
                  )}</pre>
              </dd>
            </div>
          {/if}
        </dl>
        {#if !setupAvailable}
          <!-- The standalone build, or a page Nextcloud's own scripts did not
               reach. Cassini cannot act as the administrator there. -->
          <p class="mt-3 border-t border-base-300 pt-3 text-xs break-words text-base-content/60">
            This build cannot make these changes itself. Open Cassini from Nextcloud's own menu, or
            run the commands above on the server.
          </p>
        {/if}
      </details>
    </div>
  {/if}
</section>
