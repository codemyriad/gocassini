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
    ignoredSummary,
    openRecordingAudienceLine,
    openRecordingDate,
    openRecordingLabel,
    openRecordingReasonLine,
    openRecordingsSummary,
    restrictButtonLabel,
    restrictConfirmation,
    restrictResultLine,
    selectAllLabel,
  } from "./operator/recordingAccess";
  import type { AccessMode } from "./operator/recordingAccess";
  import type {
    OpenRecording,
    OpenRecordings,
    RestrictResult,
    StorageMigration,
    StorageStatus,
  } from "./operator/types";

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

  // D-769: the recordings a migration left readable by everyone.
  //
  // Loaded on first expand rather than with the section, because answering it
  // costs the operator a PROPFIND of the Team folder and most visits to this
  // page are not about it. Null means nobody has asked yet, which the summary
  // renders as nothing at all — never as "none".
  let openRecordings: OpenRecordings | null = null;
  let openLoading = false;
  let openError: LoadError | null = null;
  let openAsked = false;
  // The ticked rows, by id. Only narrowable rows can be in here.
  let selected: Record<string, boolean> = {};
  let restrictFlow: "confirm" | null = null;
  let restricting = false;
  let restrictResults: RestrictResult[] = [];
  let ignoring = "";

  // The first button inside each inline alertdialog. The panels render further
  // down the page than the control that opened them, so an alertdialog nothing
  // focuses is one a keyboard reader is told about and cannot reach.
  let prereqsFocus: HTMLButtonElement | null = null;
  let confirmFocus: HTMLButtonElement | null = null;
  let restrictFocus: HTMLButtonElement | null = null;

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
      // Storage status deliberately returns the last preflight snapshot. That
      // makes an ordinary status reader cheap, but the archive total in this
      // panel is an administrator-facing fact: recordings may have published
      // since that snapshot was taken. Entering this panel therefore asks
      // Nextcloud for a fresh archive listing rather than calling "0" the
      // number from the enabled edge.
      status = await operatorClient.recheckStorage();
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

  async function choose(mode: AccessMode): Promise<void> {
    if (!operatorClient || busy || status === null || status.mode === mode) {
      return;
    }
    // A mode change moves the complete archive. Recheck immediately before
    // showing its confirmation, not only when the page was opened: an
    // administrator can leave Settings open while new recordings publish.
    // The confirmation's number and the actual source tree now start from the
    // same fresh view of Nextcloud.
    loading = true;
    actionError = null;
    try {
      const fresh = await operatorClient.recheckStorage();
      status = fresh;
      watchRunningSwitch();
      // A second administrator may have completed the requested switch while
      // this request was out. Do not open a confirmation that now describes a
      // no-op, or one while their migration is running.
      if (fresh.mode === mode || fresh.migration !== null) {
        return;
      }
      done = "";
      target = mode;
      // The checklist is only for the two apps, and only while one is missing.
      // Everything else this mode needs is done during the switch.
      void openPanel(needsPrerequisites(fresh, mode) ? "prereqs" : "confirm");
    } catch (error) {
      // Do not fall back to the old count: opening a destructive confirmation
      // on a stale archive inventory is worse than asking the administrator to
      // try its fresh check again.
      actionError = asFailure(error);
    } finally {
      loading = false;
    }
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
      // A storage-mode switch rewrites every recording leaf. In particular, a
      // participants -> everyone -> participants round trip deliberately
      // removes any per-recording restrictions we applied earlier. The open
      // recordings list is a snapshot from a PROPFIND, so keeping the earlier
      // snapshot here would hide the recordings that became open again until
      // the whole component happened to be reloaded (D-769).
      //
      // Do not re-fetch eagerly: the list is deliberately paid for only when
      // an administrator opens its disclosure. Resetting makes the next open
      // ask the server about the newly authoritative archive.
      resetOpenRecordings();
      switched = true;
      migration = null;
      flow = null;
      target = null;
      done = doneMessage(mode);
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

  // --- D-769: listing, limiting and ignoring open recordings ----------------

  // openRecordingsApplicable gates the whole section. The question only has a
  // meaning under Meeting participants — in the other model everything is
  // readable by everyone by design — and only once the instance is settled,
  // because during an unfinished migration which root is authoritative is
  // exactly what is unresolved.
  $: openRecordingsApplicable =
    status !== null && status.mode === PARTICIPANTS && status.migration_clean;

  async function loadOpenRecordings(): Promise<void> {
    if (!operatorClient || !openRecordingsApplicable || openLoading) {
      return;
    }
    openLoading = true;
    openError = null;
    openAsked = true;
    try {
      const next = await operatorClient.listOpenRecordings();
      status = next;
      openRecordings = next.open_recordings;
      pruneSelection();
    } catch (error) {
      // Not swallowed into an empty list: "Cassini could not look" and "nothing
      // is open" are opposite answers and only one of them is reassuring.
      openError = asFailure(error);
      openRecordings = null;
    } finally {
      openLoading = false;
    }
  }

  // resetOpenRecordings forgets a PROPFIND result after an operation that
  // changes the archive beneath it. `openAsked` is part of the cache: leaving
  // it true would make the newly rendered disclosure look loaded but prevent
  // its first expansion from asking the operator again.
  function resetOpenRecordings(): void {
    openRecordings = null;
    openLoading = false;
    openError = null;
    openAsked = false;
    selected = {};
    restrictFlow = null;
    restrictResults = [];
    ignoring = "";
  }

  // pruneSelection drops ticks for rows that are no longer there to tick. The
  // list is re-derived after every write, so a restricted recording disappears
  // from it — and a selection that outlived its row would send an id the
  // operator would only refuse.
  function pruneSelection(): void {
    const live = new Set((openRecordings?.recordings ?? []).filter((r) => r.narrowable).map((r) => r.id));
    const next: Record<string, boolean> = {};
    for (const [id, ticked] of Object.entries(selected)) {
      if (ticked && live.has(id)) {
        next[id] = true;
      }
    }
    selected = next;
  }

  function toggleSelected(id: string): void {
    selected = { ...selected, [id]: !selected[id] };
  }

  function toggleAll(): void {
    if (selectedIds.length === narrowableRows.length) {
      selected = {};
      return;
    }
    const next: Record<string, boolean> = {};
    for (const row of narrowableRows) {
      next[row.id] = true;
    }
    selected = next;
  }

  async function openRestrictConfirm(): Promise<void> {
    restrictResults = [];
    restrictFlow = "confirm";
    await tick();
    restrictFocus?.focus();
  }

  async function applyRestrict(): Promise<void> {
    if (!operatorClient) {
      return;
    }
    restricting = true;
    openError = null;
    try {
      const meetings = narrowableRows
        .filter((row) => selected[row.id])
        .map((row) => ({ id: row.id, audience_digest: row.audience_digest }));
      const next = await operatorClient.restrictRecordings(meetings);
      status = next;
      restrictResults = next.restricted;
      openRecordings = next.open_recordings ?? openRecordings;
      selected = {};
      pruneSelection();
      restrictFlow = null;
    } catch (error) {
      openError = asFailure(error);
      restrictFlow = null;
    } finally {
      restricting = false;
    }
  }

  async function setIgnored(id: string, ignored: boolean): Promise<void> {
    if (!operatorClient) {
      return;
    }
    ignoring = id;
    openError = null;
    try {
      const next = await operatorClient.ignoreRecordings([id], ignored);
      status = next;
      openRecordings = next.open_recordings ?? openRecordings;
      pruneSelection();
    } catch (error) {
      openError = asFailure(error);
    } finally {
      ignoring = "";
    }
  }

  function resultLabelFor(id: string): string {
    const known = [...(openRecordings?.recordings ?? []), ...(openRecordings?.ignored ?? [])].find(
      (row) => row.id === id,
    );
    return known ? openRecordingLabel(known) : id;
  }

  $: options = accessOptions(status);
  $: existingLine = existingRecordingsLine(status, switched);
  $: narrowableRows = (openRecordings?.recordings ?? []).filter(
    (row: OpenRecording) => row.narrowable,
  );
  $: selectedIds = narrowableRows.filter((row: OpenRecording) => selected[row.id]).map((row) => row.id);
  $: openSummary = openRecordingsSummary(openRecordings);
  $: ignoredLine = ignoredSummary(openRecordings);
  $: restrictDialog = restrictConfirmation(selectedIds.length);
  // Derived booleans rather than length comparisons in the markup: the rule in
  // this file is that a number on screen comes from the tested module beside
  // it, and a condition that counts is one edit away from being a number.
  $: hasOpenRows = (openRecordings?.recordings ?? []).length > 0;
  $: offerSelectAll = narrowableRows.length > 1;
  $: allSelected = narrowableRows.length > 0 && selectedIds.length === narrowableRows.length;
  $: nothingSelected = selectedIds.length === 0;
  $: hasResults = restrictResults.length > 0;
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
    <!-- The section could not be read at all, so this stands in for it: what
         went wrong in one sentence, the way back, and the raw diagnosis one
         disclosure down (D-756, lifted from #288). -->
    <div class="grid gap-3 p-4">
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
        <details class="rounded-box border border-base-300 bg-base-200 p-3">
          <summary class="cursor-pointer text-sm font-semibold">Details for administrators</summary>
          <p class="mt-2 font-mono text-xs break-words text-base-content/70">{loadError.detail}</p>
        </details>
      {/if}
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

      <!-- The two audiences. One sentence each about who can see, the current
           one marked, and choosing the other one starts the switch.
           The current option is not disabled: a checked radio that cannot be
           focused is a radiogroup a keyboard reader cannot read, and choose()
           already no-ops on the mode in force. -->
      <div class="grid gap-3 lg:grid-cols-2" role="radiogroup" aria-label="Who can see recordings">
        {#each options as option (option.mode)}
          <button
            class="grid content-start gap-1.5 rounded-box border p-3 text-left transition {option.current
              ? 'border-primary bg-primary/15 ring-1 ring-inset ring-primary'
              : 'border-base-300 bg-base-200 hover:border-primary/50'}"
            type="button"
            role="radio"
            aria-checked={option.current}
            disabled={busy}
            on:click={() => void choose(option.mode)}
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

      <!-- D-769. Collapsed by default: a migration can leave dozens of rows
           here, and this is not what most visits to the page are about. The
           list is derived on the operator from the archive's own permissions,
           so a recording leaves it by actually being limited rather than by
           this page removing a row. -->
      {#if openRecordingsApplicable}
        <details
          class="rounded-box border border-base-300 bg-base-200 p-3"
          on:toggle={(event) => {
            if ((event.currentTarget as HTMLDetailsElement).open && !openAsked) {
              void loadOpenRecordings();
            }
          }}
        >
          <summary class="cursor-pointer text-sm font-semibold">
            {#if openLoading && openRecordings === null}
              Checking which recordings are visible to everyone…
            {:else if openSummary}
              {openSummary}
            {:else if openAsked && openError === null}
              Every recording is limited to the people who were in the call
            {:else}
              Recordings visible to everyone
            {/if}
          </summary>

          <div class="mt-3 flex flex-col gap-3">
            {#if openError}
              <div class="alert alert-warning text-sm" role="alert">
                <TriangleAlert class="size-4 shrink-0" aria-hidden="true" />
                <span>{openError.summary}</span>
              </div>
              <div>
                <button
                  class="btn btn-sm btn-ghost"
                  type="button"
                  disabled={openLoading}
                  on:click={() => void loadOpenRecordings()}
                >
                  Try again
                </button>
              </div>
            {/if}

            {#if hasResults}
              <ul class="flex flex-col gap-1 text-sm">
                {#each restrictResults as result (result.id)}
                  <li class:text-error={result.outcome === "failed"}>
                    {restrictResultLine(result, resultLabelFor(result.id))}
                  </li>
                {/each}
              </ul>
            {/if}

            {#if openRecordings !== null && hasOpenRows}
              <p class="text-sm text-base-content/70">
                These are the people who had access to each room when it was recorded. Later
                changes to a room do not affect them. Anything else: Files → Advanced permissions.
              </p>

              {#if offerSelectAll}
                <label class="flex items-center gap-2 text-sm">
                  <input
                    class="checkbox checkbox-sm"
                    type="checkbox"
                    checked={allSelected}
                    on:change={toggleAll}
                  />
                  <span>{selectAllLabel(narrowableRows.length)}</span>
                </label>
              {/if}

              <ul class="flex flex-col gap-2">
                {#each openRecordings.recordings as row (row.id)}
                  <li class="flex items-start justify-between gap-3 border-t border-base-300 pt-2">
                    <div class="flex items-start gap-2">
                      {#if row.narrowable}
                        <input
                          class="checkbox checkbox-sm mt-1"
                          type="checkbox"
                          checked={selected[row.id] === true}
                          on:change={() => toggleSelected(row.id)}
                          aria-label={`Limit ${openRecordingLabel(row)}`}
                        />
                      {:else}
                        <!-- No control at all. The row is a statement: this
                             recording is open and Cassini cannot narrow it. -->
                        <span class="mt-1 inline-block size-4" aria-hidden="true"></span>
                      {/if}
                      <div class="text-sm">
                        <div class="font-medium">
                          {openRecordingLabel(row)}
                          <span class="ml-2 text-xs text-base-content/60">{openRecordingDate(row)}</span>
                        </div>
                        {#if row.narrowable}
                          <div class="text-xs text-base-content/70">{openRecordingAudienceLine(row)}</div>
                        {:else}
                          <div class="text-xs text-base-content/70">{openRecordingReasonLine(row)}</div>
                        {/if}
                      </div>
                    </div>
                    <button
                      class="btn btn-ghost btn-xs"
                      type="button"
                      disabled={ignoring === row.id}
                      on:click={() => void setIgnored(row.id, true)}
                    >
                      Ignore
                    </button>
                  </li>
                {/each}
              </ul>

              <div>
                <button
                  class="btn btn-sm btn-primary"
                  type="button"
                  disabled={nothingSelected || restricting}
                  on:click={() => void openRestrictConfirm()}
                >
                  {restrictButtonLabel(selectedIds.length)}
                </button>
              </div>
            {:else if openAsked && !openLoading && openError === null}
              <p class="text-sm text-base-content/70">
                Nothing is visible to everyone. Every recording is limited to the people who were
                in the call.
              </p>
            {/if}

            {#if ignoredLine}
              <details>
                <summary class="cursor-pointer text-xs">{ignoredLine}</summary>
                <ul class="mt-2 flex flex-col gap-1">
                  {#each openRecordings?.ignored ?? [] as row (row.id)}
                    <li class="flex items-center justify-between gap-3 text-sm">
                      <span>
                        {openRecordingLabel(row)}
                        <span class="ml-2 text-xs text-base-content/60">{openRecordingDate(row)}</span>
                      </span>
                      <button
                        class="btn btn-ghost btn-xs"
                        type="button"
                        disabled={ignoring === row.id}
                        on:click={() => void setIgnored(row.id, false)}
                      >
                        Stop ignoring
                      </button>
                    </li>
                  {/each}
                </ul>
              </details>
            {/if}
          </div>
        </details>
      {/if}

      {#if restrictFlow === "confirm"}
        <!-- The same inline alertdialog shape the switch confirmations use, and
             deliberately NOT the danger variant: this narrows access, which is
             the direction that cannot disclose anything. -->
        <div class="rounded-box border border-base-300 bg-base-200 p-3" role="alertdialog" aria-label={restrictDialog.title}>
          <p class="font-semibold">{restrictDialog.title}</p>
          <ul class="mt-2 flex list-disc flex-col gap-1 pl-5 text-sm">
            {#each restrictDialog.lines as line}
              <li>{line}</li>
            {/each}
          </ul>
          <div class="mt-3 flex gap-2">
            <button
              class="btn btn-sm btn-primary"
              type="button"
              bind:this={restrictFocus}
              disabled={restricting}
              on:click={() => void applyRestrict()}
            >
              {restricting ? "Limiting…" : restrictDialog.confirmLabel}
            </button>
            <button
              class="btn btn-sm btn-ghost"
              type="button"
              disabled={restricting}
              on:click={() => (restrictFlow = null)}
            >
              Cancel
            </button>
          </div>
        </div>
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
          <div class="flex flex-wrap items-center gap-2">
            <button
              class="btn btn-sm btn-ghost"
              type="button"
              disabled={installing}
              bind:this={prereqsFocus}
              on:click={cancel}
            >
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
            <button
              class="btn btn-sm btn-ghost"
              type="button"
              bind:this={confirmFocus}
              on:click={cancel}
            >
              Cancel
            </button>
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

      {#if flow === "preparing" && target}
        <!-- The browser's own half. No permission to close the page: this runs
             HERE, and closing the tab aborts it. -->
        <div
          class="grid gap-2 rounded-box border border-base-300 bg-base-200 p-3"
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
                <span class="text-base-content/40">·</span>
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
