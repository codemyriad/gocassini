<script lang="ts">
  import { onDestroy, onMount, tick } from "svelte";
  import { Check, RefreshCw, TriangleAlert } from "@lucide/svelte";
  import PasswordReveal from "./PasswordReveal.svelte";
  import { loadConfig } from "./operator/config";
  import { OperatorClient } from "./operator/client";
  import { accountSteps } from "./operator/firstRun";
  import { LOAD_ERROR_TITLE, buildLoadError, type LoadError } from "./operator/loadError";
  import {
    NcSetupError,
    isSetupAvailable,
    nextcloudUrl,
    resetServiceAccountPassword,
    runSetupPlan,
  } from "./operator/ncSetup";
  import { runModeSetup } from "./operator/runModeSetup";
  import { notifySetupChanged } from "./operator/setupSignal";
  import { pendingAccessChoice } from "./operator/accessChoice";
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
    preparingTitle,
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
  // Both failures are the same shape: a sentence with a next move in it, and
  // the raw diagnosis kept for the disclosure (operator/loadError.ts). Null is
  // "nothing went wrong", which is what an empty string used to mean.
  let loadError: LoadError | null = null;
  let actionError: LoadError | null = null;
  // done is the one-line outcome of the last switch. Ordinary component state:
  // nothing reloads the page, so there is nothing for it to survive.
  let done = "";

  // Which panel is open. Null is the settled section. "preparing" is the
  // browser's own half of a switch and "switching" the operator's: they are two
  // states because only the second one survives a closed tab.
  let flow: "prereqs" | "confirm" | "preparing" | "switching" | null = null;
  // target is the mode being switched TO. Null while a switch that this page
  // did not start is being watched, which is the one case where nothing on the
  // wire says where it is going.
  let target: AccessMode | null = null;

  let switching = false;
  let installing = false;
  let creatingAccount = false;
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
  // credential is the service account's password, when the administrator asked
  // for one through "Set a password". It exists nowhere else, at either end,
  // and there is no second chance at it — so everything else is disabled until
  // it is acknowledged. Nothing else in this section mints one: an account
  // created here signs in through AppAPI's act-as-user header and needs none.
  let credential: { user: string; password: string } | null = null;

  // The first button inside each inline alertdialog. The panels render further
  // down the page than the control that opened them, so an alertdialog nothing
  // focuses is one a keyboard reader is told about and cannot reach.
  let prereqsFocus: HTMLButtonElement | null = null;
  let confirmFocus: HTMLButtonElement | null = null;

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
    loadError = null;
    try {
      status = await operatorClient.getStorage();
      watchRunningSwitch();
    } catch (error) {
      loadError = asFailure(error);
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
    loadError = null;
    actionError = null;
    try {
      status = await operatorClient.recheckStorage();
      watchRunningSwitch();
    } catch (error) {
      loadError = asFailure(error);
    } finally {
      loading = false;
    }
  }

  function choose(mode: AccessMode): void {
    if (busy || status === null || status.mode === mode) {
      return;
    }
    actionError = null;
    done = "";
    target = mode;
    // The checklist is only for the two apps, and only while one is missing.
    // Everything else this mode needs is done during the switch.
    void openPanel(needsPrerequisites(status, mode) ? "prereqs" : "confirm");
  }

  // openPanel shows one of the two alertdialogs and puts the focus on its first
  // button, which is Cancel in both: the way out, not the way on.
  async function openPanel(next: "prereqs" | "confirm"): Promise<void> {
    flow = next;
    await tick();
    (next === "prereqs" ? prereqsFocus : confirmFocus)?.focus();
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
    actionError = null;
    try {
      await operatorClient.installStorageApps();
      status = await operatorClient.recheckStorage();
      notifySetupChanged();
      if (target !== null && !needsPrerequisites(status, target)) {
        void openPanel("confirm");
      }
    } catch (error) {
      actionError = asFailure(error);
    } finally {
      installing = false;
    }
  }

  // createAccount makes the `cassini` account from this section, for the install
  // whose administrator left the first-run dialog by its other button. Without
  // it the only remaining way to create the account is a switch to Meeting
  // participants, which is a different decision entirely.
  //
  // The same two steps the dialog runs, from the same plan (firstRun.ts), and
  // the password runSetupPlan mints is dropped on the floor for the same
  // reason: the operator signs in as the account through AppAPI's act-as-user
  // header, so a credential shown once here would be made out of a value
  // nothing uses. "Set a password" below is the row for the rare day somebody
  // needs to sign in as it themselves.
  //
  // It also answers the first-run flag, because this is the other place the
  // account can come into existence (D-756 review). The dialog's own button
  // answers it after its run; this one answers it after this run, and neither
  // answers it before the account is there — an install that still cannot
  // record must not lose the one dialog that would say so.
  async function createAccount(): Promise<void> {
    if (!operatorClient || busy || accountPlan.length === 0) {
      return;
    }
    creatingAccount = true;
    actionError = null;
    progress = "";
    try {
      await runSetupPlan(accountPlan, {
        onProgress: ({ step, index, total }) => (progress = `${index + 1}/${total} — ${step.title}`),
      });
      status = await operatorClient.recheckStorage();
      if (status.service_account.exists) {
        // Best effort, and separately caught: the account was created, and
        // reporting a failed flag write as though the creation failed would be
        // the opposite of what happened. A flag that did not get written shows
        // the dialog once more, which is the honest degrade.
        try {
          await operatorClient.acknowledgeFirstRun();
        } catch (error) {
          console.warn("Cassini: the first-run acknowledgement failed.", error);
        }
      }
      notifySetupChanged();
    } catch (error) {
      actionError = asFailure(error);
    } finally {
      creatingAccount = false;
      progress = "";
    }
  }

  // confirmSwitch is the only thing that moves recordings, and it is reachable
  // only from the confirmation panel.
  //
  // It builds what the mode still needs first — the Team folder, its mappings,
  // the ACL, the manager — through runModeSetup, which is the sequence the
  // settings section shares with the first-run dialog and which keeps the
  // apps-first ordering that is easy to get wrong. Then one PUT, which blocks
  // for the length of the move while the poll below reads its progress.
  //
  // The two halves are two panels, in that order, because only the second one
  // survives a closed tab: runModeSetup writes to Nextcloud FROM THIS BROWSER,
  // so a page that offered "you can close this page" while it ran would be
  // inviting an administrator to abort the setup and get no switch at all.
  async function confirmSwitch(): Promise<void> {
    if (!operatorClient || target === null || switching) {
      return;
    }
    const mode = target;
    switching = true;
    actionError = null;
    done = "";
    migration = null;
    progress = "";
    flow = "preparing";
    try {
      const option = status?.modes.find((row) => row.mode === mode) ?? null;
      if (option && !option.available && option.setup.length > 0) {
        const result = await runModeSetup(operatorClient, option, (message) => {
          progress = message;
        });
        status = result.status;
        if (!result.finished) {
          // An app install the operator could not perform is still outstanding,
          // and everything left lives inside those apps. Back to the checklist,
          // which is where the two buttons for that are.
          notifySetupChanged();
          void openPanel("prereqs");
          return;
        }
      }
      // From here the work is the operator's and it survives a closed tab, so
      // this is where the switching panel and its poll start.
      progress = "";
      flow = "switching";
      stopPoll?.();
      stopPoll = pollMigration(null);
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
      if (status.first_run && status.service_account.exists) {
        try {
          await operatorClient.acknowledgeFirstRun();
        } catch (error) {
          console.warn("Cassini: the first-run acknowledgement failed.", error);
        }
      }
      notifySetupChanged();
    } catch (error) {
      actionError = asFailure(error);
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
      // Whatever happened, no switch is running as far as this page knows. A
      // stale count left here would go on disabling the whole section, because
      // a running switch is what `busy` is.
      migration = null;
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
    actionError = null;
    done = "";
    try {
      status = await operatorClient.finishStorageMigration();
      notifySetupChanged();
    } catch (error) {
      actionError = asFailure(error);
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
    actionError = null;
    credential = null;
    try {
      const user = status.service_account.user;
      credential = { user, password: await resetServiceAccountPassword(user) };
    } catch (error) {
      actionError = asFailure(error);
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
    const poll = async () => {
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
        timer = setTimeout(() => void poll(), POLL_MS);
      }
    };
    timer = setTimeout(() => void poll(), POLL_MS);
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

  // asFailure turns whatever was thrown into the one sentence this section
  // shows, plus the raw diagnosis for the disclosure.
  //
  // Nextcloud's own refusals are classified here because only this side knows
  // what was being attempted — a cancelled password confirmation is not an
  // error to report, it is a step somebody declined. Everything else is an
  // operator call that failed, and operator/loadError.ts is where a status code
  // becomes a next move (lifted from #288): the section used to render
  // `error.message`, which is how an administrator came to read "HTTP 503" as
  // the reason they could not change who sees recordings.
  function asFailure(error: unknown): LoadError {
    if (error instanceof NcSetupError) {
      switch (error.reason) {
        case "cancelled":
          return plainly(
            "Nextcloud needs you to confirm your password before it will make these changes.",
          );
        case "unavailable":
          return plainly(
            `${error.message} Open Cassini from Nextcloud's own menu, or run the commands under "Details for administrators" instead.`,
          );
        default:
          return plainly(error.message, error.step ? `at: ${error.step}` : "");
      }
    }
    return buildLoadError(error);
  }

  // plainly is a sentence that is already the right one: Nextcloud said what
  // happened, and there is no status code to classify.
  function plainly(summary: string, detail = ""): LoadError {
    return { title: LOAD_ERROR_TITLE, summary, detail };
  }

  $: options = accessOptions(status);

  $: if ($pendingAccessChoice && status && !loading && !busy) {
    const next = $pendingAccessChoice;
    pendingAccessChoice.set(null);
    choose(next);
  }
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
  $: accountPlan = accountSteps(status);
  // The account the first-run dialog would have made. `known` is the operator
  // saying which account it wants; `exists` is whether Nextcloud has it.
  $: needsAccount =
    status !== null && status.service_account.known && !status.service_account.exists;
  // busy is every reason to touch nothing, and a switch RUNNING is one of them
  // whether or not this page started it: a second PUT, or finish_migration, in
  // the middle of a move is the one thing this section must not make reachable.
  $: busy =
    switching ||
    installing ||
    creatingAccount ||
    resuming ||
    resetting ||
    credential !== null ||
    migration !== null;
</script>

<section class="op-tint access">
  <header class="access-head">
    <div class="set-row-main">
      <h2 class="set-row-name op-card-title">Who can see recordings</h2>
      <p class="set-row-sub">
        Applies to every recording Cassini publishes to this Nextcloud.
      </p>
    </div>
    <button
      class="icon-btn"
      type="button"
      on:click={recheck}
      disabled={loading || busy || !operatorClient}
      aria-label="Check this Nextcloud again"
    >
      <RefreshCw size={15} aria-hidden="true" />
    </button>
  </header>

  {#if loadError}
    <!-- The section could not be read at all, so this stands in for it: what
         went wrong in one sentence, the way back, and the raw diagnosis one
         disclosure down (D-756, lifted from #288). -->
    <div class="access-body">
      <div class="alert alert-error items-start gap-3 text-sm" role="alert">
        <TriangleAlert size={16} class="mt-0.5 shrink-0" aria-hidden="true" />
        <div class="grid min-w-0 gap-1">
          <p class="font-semibold">{loadError.title}</p>
          <p class="break-words">{loadError.summary}</p>
        </div>
      </div>
      <div class="flex flex-wrap items-center gap-2">
        <button class="btn btn-sm btn-primary" type="button" disabled={loading} on:click={load}>
          {#if loading}
            <span class="loading loading-spinner loading-xs" aria-hidden="true"></span>
            Checking…
          {:else}
            Try again
          {/if}
        </button>
      </div>
      {#if loadError.detail}
        <details>
          <summary class="tpl-toggle">
            <span class="tpl-chev" aria-hidden="true"></span>
            Details for administrators
          </summary>
          <p class="tpl-def access-mono">{loadError.detail}</p>
        </details>
      {/if}
    </div>
  {:else if loading}
    <p class="op-state">Loading…</p>
  {:else if !status}
    <p class="op-state">No storage settings available.</p>
  {:else}
    <div class="access-body">
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
          <div class="grid min-w-0 gap-1">
            <p class="break-words">{actionError.summary}</p>
            {#if actionError.detail}
              <!-- Same rule as the load failure and the setup notice: the
                   status code and the step name are worth keeping and are not
                   worth reading first. -->
              <details>
                <summary class="cursor-pointer text-xs">Show details</summary>
                <p class="mt-1 font-mono text-xs break-words opacity-80">{actionError.detail}</p>
              </details>
            {/if}
          </div>
        </div>
      {/if}

      {#if !status.migration_clean}
        <!-- A switch that stopped part way. One line and one button: the
             archive is complete at the mode in force either way, so naming the
             root that holds the leftovers is detail, not news. -->
        <div class="flex flex-wrap items-center gap-3 rounded-box border border-warning bg-warning/10 p-3" role="status">
          <p class="text-sm font-semibold">The last switch didn't finish.</p>
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

      {#if needsAccount}
        <!-- The account the first-run dialog would have made, for the install
             whose administrator left that dialog by its other button. It
             applies in both modes: nothing is recorded at all without it. -->
        <div class="flex flex-wrap items-center gap-3 rounded-box border border-warning bg-warning/10 p-3">
          <p class="text-sm">Cassini needs a Nextcloud account to keep recordings in.</p>
          <button
            class="btn btn-sm btn-warning"
            type="button"
            disabled={busy || !setupAvailable || accountPlan.length === 0}
            on:click={createAccount}
          >
            {#if creatingAccount}
              <span class="loading loading-spinner loading-xs" aria-hidden="true"></span>
              Creating…
            {:else}
              Create the account
            {/if}
          </button>
          {#if creatingAccount && progress}
            <p class="text-xs break-words text-base-content/70" aria-live="polite">{progress}</p>
          {/if}
        </div>
      {/if}

      <div class="access-choose">
      <!-- The two audiences. One sentence each about who can see, the current
           one marked, and choosing the other one starts the switch.
           The current option is not disabled: a checked radio that cannot be
           focused is a radiogroup a keyboard reader cannot read, and choose()
           already no-ops on the mode in force. -->
      <div class="access-options" role="radiogroup" aria-label="Who can see recordings">
        {#each options as option (option.mode)}
          <button
            class="access-opt"
            class:selected={option.current}
            type="button"
            role="radio"
            aria-checked={option.current}
            disabled={busy}
            on:click={() => choose(option.mode)}
          >
            <span class="access-radio" class:checked={option.current} aria-hidden="true"></span>
            <span class="access-opt-body">
              <span class="access-opt-title">{option.title}</span>
              <span class="access-opt-desc">{option.description}</span>
            </span>
          </button>
        {/each}
      </div>

      <!-- The fact an administrator will otherwise be surprised by later: a
           switch does not change who can see the recordings that already
           exist. It stays on screen permanently, not only after a switch. -->
      {#if existingLine}
        <p class="set-row-sub access-existing">{existingLine}</p>
      {/if}

      {#if flow === "prereqs"}
        <!-- An inline confirmation rather than the platform's modal dialog
             element: the whole app runs inside a shadow root on Nextcloud's
             embedded page, where the top layer is the one thing whose styling
             and focus behaviour do not reliably follow it. -->
        <div
          class="grid gap-3 rounded-box border border-warning bg-warning/10 p-3"
          role="alertdialog"
          aria-label="This needs two Nextcloud apps"
        >
          <p class="text-sm font-semibold">This needs two Nextcloud apps</p>
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
            Cassini can install what's missing for you. If that fails, install it from Nextcloud's
            Apps page and Cassini will detect it.
          </p>
          <!-- The instance-wide effect, before the install rather than after.
               An acceptance criterion since D-671 that has never shipped. -->
          <p class="rounded-box bg-base-100/60 p-2 text-xs break-words text-base-content/80">
            Everyone Group adds an <b>Everyone</b> group to your whole Nextcloud. It also appears
            when sharing files in other apps, not just in Cassini.
          </p>
          <div class="flex flex-wrap items-center gap-3">
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
            <a class="btn btn-sm btn-outline" href={nextcloudUrl("/settings/apps")} target="_top">
              Open Nextcloud Apps
            </a>
            <button
              class="link-btn op-cancel"
              type="button"
              disabled={installing}
              bind:this={prereqsFocus}
              on:click={cancel}
            >
              Cancel
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
          <div class="flex flex-wrap items-center gap-3">
            <button
              class="btn btn-sm {confirmation.danger ? 'btn-error' : 'btn-warning'}"
              type="button"
              disabled={busy}
              on:click={confirmSwitch}
            >
              {confirmation.confirmLabel}
            </button>
            <button
              class="link-btn op-cancel"
              type="button"
              bind:this={confirmFocus}
              on:click={cancel}
            >
              Cancel
            </button>
          </div>
        </div>
      {/if}

      {#if flow === "preparing" && target}
        <!-- The browser's own half. No permission to close the page: this runs
             HERE, and closing the tab aborts it. -->
        <div
          class="op-tint access-panel grid gap-2"
          role="status"
          aria-live="polite"
        >
          <p class="flex items-center gap-2 text-sm font-semibold">
            <span class="loading loading-spinner loading-xs" aria-hidden="true"></span>
            {preparingTitle(target)}
          </p>
          {#if progress}
            <p class="text-xs break-words text-base-content/70">{progress}</p>
          {/if}
        </div>
      {/if}

      {#if flow === "switching"}
        <!-- The four real steps, in the order the operator performs them, so an
             interruption is honest: whichever step it stopped at, a complete
             archive exists somewhere. -->
        <div
          class="op-tint access-panel grid gap-3"
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
        </div>
      {/if}

      <!-- Everything technical, one disclosure down and collapsed by default:
           the paths, the app ids, the enum, the account and the commands. -->
      <details>
        <summary class="tpl-toggle">
          <span class="tpl-chev" aria-hidden="true"></span>
          Details for administrators
        </summary>
        <dl class="tpl-def access-details">
          {#if place.root}
            <div>
              <dt class="access-dt">Recordings are stored in</dt>
              <dd class="text-sm break-words">
                <code class="break-all">{place.root}</code>
                <span class="text-base-content/70">({place.container})</span>
              </dd>
            </div>
          {/if}
          <div>
            <dt class="access-dt">Nextcloud apps in use</dt>
            <dd class="text-sm">{appsInUse(status)}</dd>
          </div>
          <div>
            <dt class="access-dt">Storage check</dt>
            <dd class="text-sm">
              <span class={status.ok ? "text-success" : "text-warning"}>{checkLine}</span>
              {#if reportUrl}
                <span class="text-base-content/40">·</span>
                <a
                  class="access-link"
                  href={reportUrl}
                  target="_blank"
                  rel="noopener noreferrer">Full report</a
                >
              {/if}
            </dd>
            {#if !status.ok && status.detail}
              <!-- The operator's own sentence, verbatim, so this section and
                   the container log read the same. -->
              <dd class="access-code-block mt-1 break-words">
                {status.detail}
              </dd>
            {/if}
          </div>
          <div>
            <dt class="access-dt">Rule</dt>
            <dd class="text-sm break-words">
              <code>{status.mode === "" ? "none" : status.mode}</code>
            </dd>
          </div>
          <div>
            <dt class="access-dt">How it was set</dt>
            <dd class="text-sm break-words">{modeSourceLabel(status.mode_source)}</dd>
          </div>
          <div>
            <dt class="access-dt">Service account</dt>
            <dd class="text-sm break-words text-base-content/80">
              Cassini stores and reads recordings using a Nextcloud account called
              <code>{status.service_account.user}</code>. Cassini doesn't need or keep its password.
              <button
                class="access-link"
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
              if you want to sign in as this account.
            </dd>
          </div>
          {#if occ.length > 0}
            <div>
              <dt class="access-dt">Set it up manually</dt>
              <dd class="text-sm">
                Run these <code>occ</code> commands on the server to make the same change:
                <pre
                  class="access-code-block m-0 mt-1 overflow-x-auto">{occ.join(
                    "\n",
                  )}</pre>
              </dd>
            </div>
          {/if}
        </dl>
        {#if !setupAvailable}
          <!-- The standalone build, or a page Nextcloud's own scripts did not
               reach. Cassini cannot act as the administrator there. -->
          <p class="set-row-sub access-standalone">
            Cassini can't make these changes from here. Open Cassini from Nextcloud's app menu, or run
            the commands above on the server.
          </p>
        {/if}
      </details>
      </div>
    </div>
  {/if}
</section>

<style>
  .access {
    display: grid;
    gap: 12px;
    margin-top: 12px;
    padding: 14px 16px;
  }
  .access-head {
    display: flex;
    align-items: flex-start;
    gap: 14px;
  }
  .access-choose {
    display: grid;
    gap: 12px;
  }
  .access-body {
    display: grid;
    gap: 12px;
  }
  .access-mono {
    font-family: var(--font-mono);
    font-size: 11.5px;
    overflow-wrap: anywhere;
  }
  .access-options {
    display: grid;
    gap: 12px;
  }
  @media (min-width: 1024px) {
    .access-options {
      grid-template-columns: repeat(2, minmax(0, 1fr));
    }
  }
  .access-opt {
    display: flex;
    align-items: flex-start;
    gap: 10px;
    padding: 10px 12px;
    text-align: left;
    cursor: pointer;
    color: var(--color-base-content);
    background-color: var(--op-inset);
    border: 1px solid var(--op-inset-border);
    border-radius: var(--radius-box, 0.5rem);
  }
  .access-opt:not(:disabled):hover {
    border-color: color-mix(in oklch, var(--color-base-content) 30%, var(--color-base-200));
  }
  .access-opt.selected,
  .access-opt.selected:not(:disabled):hover {
    background-color: color-mix(in srgb, var(--color-primary) 14%, var(--color-base-100));
    border-color: var(--color-primary);
  }
  .access-opt:disabled {
    cursor: default;
  }
  .access-opt-body {
    display: grid;
    gap: 4px;
    min-width: 0;
  }
  .access-radio {
    position: relative;
    flex: none;
    width: 16px;
    height: 16px;
    margin-top: 2px;
    background: var(--color-base-100);
    border: 1px solid color-mix(in oklch, var(--color-base-content) 26%, transparent);
    border-radius: 50%;
  }
  .access-radio.checked {
    border-color: var(--color-primary);
  }
  .access-radio.checked::after {
    content: "";
    position: absolute;
    top: 50%;
    left: 50%;
    width: 8px;
    height: 8px;
    background: var(--color-primary);
    border-radius: 50%;
    transform: translate(-50%, -50%);
  }
  .access-opt-title {
    font-size: 13.5px;
    font-weight: 600;
  }
  .access-opt-desc {
    font-size: 12.5px;
    line-height: 1.5;
    color: color-mix(in oklch, var(--color-base-content) 65%, transparent);
  }
  .set-row-sub {
    display: block;
  }
  .access-existing {
    margin: 0;
  }
  .access-panel {
    padding: 12px 14px;
    background-color: var(--op-inset);
  }
  .access-details {
    display: grid;
    gap: 12px;
    max-width: none;
    color: var(--color-base-content);
  }
  .access-link {
    padding: 0;
    cursor: pointer;
    font: inherit;
    font-weight: 500;
    color: var(--color-base-content);
    background: none;
    border: 0;
    text-decoration: underline;
    text-decoration-color: color-mix(in oklch, var(--color-base-content) 40%, transparent);
    text-underline-offset: 2px;
  }
  .access-link:hover {
    text-decoration-color: currentColor;
  }
  .access-link:disabled {
    cursor: default;
    opacity: 0.55;
  }
  .access-dt {
    margin-bottom: 2px;
    font-size: 12.5px;
    font-weight: 600;
    line-height: 1.4;
    color: var(--color-base-content);
  }
  .access-details code {
    padding: 1px 5px;
    font-family: var(--font-mono);
    font-size: 11.5px;
    font-weight: 500;
    overflow-wrap: anywhere;
    background-color: var(--op-code-bg);
    border: 1px solid var(--op-code-border);
    border-radius: var(--radius-selector, 0.25rem);
  }
  .access-details dd {
    margin: 0;
    font-size: 12.5px;
    line-height: 1.5;
    color: color-mix(in oklch, var(--color-base-content) 70%, transparent);
  }
  .access-code-block {
    padding: 8px 10px;
    font-family: var(--font-mono);
    font-size: 11.5px;
    line-height: 1.6;
    color: color-mix(in oklch, var(--color-base-content) 75%, transparent);
    background-color: var(--op-code-bg);
    border: 1px solid var(--op-code-border);
    border-radius: var(--radius-field, 0.5rem);
  }
  .access-standalone {
    margin-top: 12px;
  }
</style>
