<script lang="ts">
  import { onMount } from "svelte";
  import { HardDrive, KeyRound, Lock, RefreshCw, TriangleAlert } from "@lucide/svelte";
  import MigrationPolicy from "./MigrationPolicy.svelte";
  import PasswordReveal from "./PasswordReveal.svelte";
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
    DEFAULT_MIGRATION_POLICY,
    carryChoiceNeeded,
    describeArchive,
    migrationFacts,
    policyToSend,
  } from "./operator/storageWizard";
  import type {
    StorageMigrationPolicy,
    StorageModeOption,
    StorageStatus,
    StorageTransitionPreview,
  } from "./operator/types";

  // The storage-mode switch, and since D-671 the setup that gets you to one.
  //
  // Every sentence rendered here — what a mode means, what switching to it
  // would do, what is missing and which command fixes it — comes from GET
  // /storage. That is not laziness: the operator is the layer that knows the
  // Team folder's id, the group names and which prerequisite is actually
  // absent, and a wrong instruction is a worse failure than a missing one. This
  // component decides only when to ask, and who performs what.
  //
  // Who performs what is the whole shape of D-671:
  //
  //   step.browser = true    THIS PAGE does it, as the signed-in administrator,
  //                          after Nextcloud's own password-confirmation dialog.
  //                          The operator is refused these writes entirely.
  //   step.browser = false   the OPERATOR attempts it (the app installs), and
  //                          hands off to Nextcloud's Apps page when Nextcloud
  //                          demands a password on the request itself.
  //
  //   idle ─click an unavailable mode─▶ confirm setup ─▶ run plan ─▶ recheck
  //     ▲                                    │              │
  //     │                                    │              └─▶ switch, if asked
  //     └────── cancel / error ──────────────┘

  export let operatorClient: OperatorClient | null = null;

  let status: StorageStatus | null = null;
  let loading = true;
  let loadError = "";
  let switchError = "";
  // pending is what the confirmation prompt is asking about: a mode to switch
  // to, a setup to run, or null.
  let pending: StorageModeOption | null = null;
  // pendingKind distinguishes "switch to this" from "build this first".
  let pendingKind: "switch" | "setup" = "switch";
  let switching = false;
  // setupProgress is the step being performed, for the run's own feedback.
  let setupProgress = "";
  // preview is what the pending switch WOULD do, fetched before the
  // confirmation renders its buttons. Null while it is still being fetched, or
  // when the pending action is a setup rather than a move.
  let preview: StorageTransitionPreview | null = null;
  let previewing = false;
  let previewError = "";
  // previewToken orders the in-flight previews. Changing a control fires a new
  // one, and without this the slower of two answers wins — leaving numbers on
  // screen that describe a policy the button will not send.
  let previewToken = 0;
  // outcome is what the last successful action did, kept on screen until the
  // next one starts. It is ordinary component state: nothing reloads the page,
  // so there is nothing for it to survive.
  let outcome: { tone: "success" | "warning"; message: string; detail?: string } | null = null;
  // repairing is the "finish the switch" action, which is separate from
  // `switching` because it has no confirmation prompt: there is nothing to
  // decide, only leftovers to clear.
  let repairing = false;
  // policy is what a switch does with the recordings that are already there. It
  // is only ever SENT when the operator said there was a choice to make — see
  // policyToSend, and the operator's own refusal to pick one for a conflict
  // nobody was shown (D-708).
  let policy: StorageMigrationPolicy = { ...DEFAULT_MIGRATION_POLICY };
  // credential is the service account's password, when this session just minted
  // one. It is component state and nothing else: it exists nowhere on disk, at
  // either end, and there is no second chance at it.
  let credential: { user: string; password: string } | null = null;
  let resetting = false;

  onMount(() => {
    void loadStorage();
  });

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

  // finishAndAnnounce is how every successful action ends: say what happened,
  // and tell the shell this Nextcloud is not the one it looked at.
  //
  // The notify is the fix for the stale "Cassini is not configured" warning.
  // App.svelte reads its setup health once at mount and nothing wrote it again,
  // so building the substrate here left every other tab showing the problem it
  // had just fixed. It re-reads now, in the same session — no page reload, so
  // the panel's own state, the viewer's playback position and everything else
  // the page was holding survive.
  function finishAndAnnounce(next: NonNullable<typeof outcome>): void {
    outcome = next;
    notifySetupChanged();
  }

  async function loadStorage() {
    if (!operatorClient) {
      return;
    }
    loading = true;
    loadError = "";
    switchError = "";
    outcome = null;
    try {
      status = await operatorClient.getStorage();
    } catch (error) {
      loadError = asMessage(error);
    } finally {
      loading = false;
    }
  }

  function requestSwitch(option: StorageModeOption) {
    // Clicking the mode already in force is a no-op.
    if (option.active || switching) {
      return;
    }
    switchError = "";
    // An unavailable mode is not a dead end any more: if the operator gave a
    // plan for it, offer to build it. Without a plan there is genuinely nothing
    // to offer — its blocker is already on screen.
    pendingKind = option.available ? "switch" : "setup";
    if (pendingKind === "setup" && option.setup.length === 0) {
      return;
    }
    pending = option;
    policy = { ...DEFAULT_MIGRATION_POLICY };
    if (pendingKind === "switch") {
      void loadPreview(option);
    }
  }

  // loadPreview asks the operator what this switch would actually do.
  //
  // It runs when the prompt OPENS, not when it is confirmed, because the whole
  // point is that the numbers are on screen while the administrator decides. A
  // preview that fails does not block the switch — the transition has its own
  // guards, and refusing to let somebody proceed because we could not count
  // their recordings would be worse than proceeding without the count — but it
  // says so rather than rendering an empty diff as "nothing to move".
  async function loadPreview(option: StorageModeOption) {
    if (!operatorClient) {
      return;
    }
    preview = null;
    previewError = "";
    previewing = true;
    const asked = option.mode;
    previewToken += 1;
    const token = previewToken;
    try {
      const next = await operatorClient.previewStorageSwitch(
        option.mode === "access_controlled",
        policy,
      );
      // The prompt may have been cancelled or re-pointed while this was in
      // flight, and a later request for a different policy may already have
      // been issued; a diff for a mode nobody is looking at, or for a policy
      // nobody has selected, must not appear.
      if (token === previewToken && pending?.mode === asked) {
        preview = next.preview;
      }
    } catch (error) {
      if (token === previewToken && pending?.mode === asked) {
        previewError = asMessage(error);
      }
    } finally {
      if (token === previewToken) {
        previewing = false;
      }
    }
  }

  // Re-previewing on every policy change is one PROPFIND pair against Nextcloud,
  // and it is what keeps the numbers under the controls true. A confirmation
  // whose counts describe a policy the administrator has since changed is worse
  // than one with no counts.
  function onPolicyChanged(): void {
    if (pending && pendingKind === "switch" && carryChoiceNeeded(preview)) {
      void loadPreview(pending);
    }
  }

  // resetServiceAccount mints a new password for the account and shows it once.
  //
  // It is a control of its own rather than a setup step: a plan is emitted only
  // for a mode that is NOT ready, so a healthy instance has no step to hang one
  // on — which is exactly when somebody who has lost the password needs it.
  async function resetServiceAccount(): Promise<void> {
    if (!operatorClient || !status || resetting) {
      return;
    }
    resetting = true;
    switchError = "";
    outcome = null;
    credential = null;
    try {
      const user = status.service_account.user;
      credential = { user, password: await resetServiceAccountPassword(user) };
    } catch (error) {
      switchError = asMessage(error);
      // A setup run that failed AFTER creating the account still minted a
      // password, and it exists nowhere else. Show it with the error rather than
      // losing it to a step that failed later.
      rememberCredential(error);
    } finally {
      resetting = false;
    }
  }

  // requestSetupFor offers to build the mode that is ALREADY in force but not
  // usable — the "no cassini service account" case, where there is nothing to
  // switch to and the thing to fix is right here.
  function requestSetupFor(option: StorageModeOption) {
    if (switching || option.setup.length === 0) {
      return;
    }
    switchError = "";
    pendingKind = "setup";
    pending = option;
  }

  function cancelSwitch() {
    pending = null;
    preview = null;
    previewError = "";
  }

  // runSetup performs everything the browser may perform, asks the operator to
  // attempt the rest, and re-probes. It deliberately does NOT switch modes:
  // building a mode and moving into it are separate decisions, and after this
  // the buttons say which one is now possible.
  async function runSetup(option: StorageModeOption) {
    if (!operatorClient) {
      return;
    }
    // The sequence — apps first, then recompute the plan, then the browser
    // steps — lives in runModeSetup.ts, because the setup wizard performs the
    // same one and the ORDER inside it is load-bearing in a way that is easy to
    // get wrong twice. It is tested there, as behaviour rather than as source.
    const result = await runModeSetup(operatorClient, option, (message) => {
      setupProgress = message;
    });
    status = result.status;
    if (result.createdAccount && result.password) {
      credential = { user: result.createdAccount, password: result.password };
    }
    if (!result.finished) {
      // An app install the operator could not perform is still outstanding.
      // Everything left in the plan lives inside those apps, so stopping is the
      // honest outcome — the per-app detail on screen says what to do.
      //
      // The shell is told anyway: one of the two apps may well have gone in, and
      // this component cannot tell from here.
      notifySetupChanged();
      return;
    }
    finishAndAnnounce({
      tone: "success",
      message: `Setup finished for ${option.label.toLowerCase()} storage.`,
      detail: "Every tab now shows the instance as it is; there is nothing to refresh.",
    });
  }

  async function confirmSwitch() {
    if (!operatorClient || !pending || switching) {
      return;
    }
    const target = pending;
    const kind = pendingKind;
    switching = true;
    switchError = "";
    setupProgress = "";
    // Whatever the last action achieved is history the moment a new one starts.
    // Leaving it would put a success strip directly above the error that
    // replaced it.
    outcome = null;
    try {
      if (kind === "setup") {
        await runSetup(target);
        // A scaffold that created the account leaves a password on screen that
        // exists nowhere else. Keep the prompt closed but do not proceed past
        // it: everything is disabled until it is acknowledged.
      } else {
        status = await operatorClient.putStorage(
          target.mode === "access_controlled",
          // Sent only when the operator said there was a choice. It refuses a
          // policy-free switch that finds one under its own lock, and a request
          // carrying an answer is taken to have been answered by a person.
          policyToSend(preview, policy),
        );
        finishAndAnnounce({
          tone: status.transition?.leftover_source ? "warning" : "success",
          message: `Storage is now ${target.label.toLowerCase()}.`,
          detail: describeTransition(status),
        });
      }
      pending = null;
      preview = null;
    } catch (error) {
      switchError = asMessage(error);
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
      // A refused switch keeps the prompt open and re-previews. The one refusal
      // that MUST leave it open is the operator saying a choice appeared between
      // the preview and the switch: closing the prompt there would leave an
      // administrator with a message telling them to decide and nothing to
      // decide with.
      if (kind === "switch" && target.available) {
        await loadPreview(target);
      } else {
        pending = null;
        preview = null;
      }
    } finally {
      switching = false;
      setupProgress = "";
    }
  }

  // finishMigration clears the leftovers a switch did not get to. It is the one
  // recovery action, and it is the same action whichever half of a switch
  // failed — see the operator's finishMigration.
  async function finishMigration() {
    if (!operatorClient || repairing) {
      return;
    }
    repairing = true;
    switchError = "";
    outcome = null;
    try {
      status = await operatorClient.finishStorageMigration();
      finishAndAnnounce({
        tone: "success",
        message: "The interrupted storage switch was finished.",
        detail: "The leftover copy was cleared; your recordings were not touched.",
      });
    } catch (error) {
      switchError = asMessage(error);
      try {
        status = await operatorClient.getStorage();
      } catch {
        // Keep what we had; the error is the thing worth showing.
      }
    } finally {
      repairing = false;
    }
  }

  // describeTransition says what the switch DID, per policy.
  //
  // The first pass had one sentence — "N recordings were copied" — because there
  // was one behaviour. Under `switch_only` that sentence is flatly false, and
  // under `skip` it is incomplete in the direction that matters: some recordings
  // stayed in the source on purpose, and an administrator who is not told will
  // read the leftover as a failure.
  function describeTransition(from: StorageStatus | null): string {
    const transition = from?.transition;
    if (!transition) {
      return "The storage mode changed.";
    }
    // Confirming the mode already in force moves nothing and still changes
    // something: who is on record as having chosen it. "0 recordings were
    // copied" is a strange way to say "thank you, noted".
    if (transition.confirmed) {
      return "Nothing moved — the recordings were already where this mode keeps them.";
    }
    const parts: string[] = [];
    // The deletion leads, and it is called a deletion. `overwrite` removes
    // recordings the switch was not asked to move, and they are in no other
    // folder afterwards.
    if (transition.meetings_deleted_at_destination > 0) {
      parts.push(
        `${plural(transition.meetings_deleted_at_destination, "recording")} in ${transition.destination_root} ${were(transition.meetings_deleted_at_destination)} deleted, because replacing it is what you chose.`,
      );
    }
    if (transition.meetings_moved > 0) {
      parts.push(
        `${plural(transition.meetings_moved, "recording")} ${were(transition.meetings_moved)} moved into ${transition.destination_root}.`,
      );
    } else if (transition.meetings_deleted_at_destination === 0) {
      parts.push(`Nothing was copied; the recordings stayed in ${transition.source_root}.`);
    }
    if (transition.meetings_kept_in_source > 0) {
      parts.push(
        `${plural(transition.meetings_kept_in_source, "recording")} also kept a copy in ${transition.source_root}, as you asked.`,
      );
    }
    if (transition.leftover_source) {
      parts.push(
        `${transition.leftover_source} still holds a copy the tidy-up did not finish — use "Finish the switch" above to clear it.`,
      );
    }
    return parts.join(" ");
  }

  function plural(n: number, noun: string): string {
    return n === 1 ? `1 ${noun}` : `${n} ${noun}s`;
  }

  function were(n: number): string {
    return n === 1 ? "was" : "were";
  }

  function asMessage(error: unknown): string {
    if (error instanceof NcSetupError) {
      // Nextcloud's own refusals, in the administrator's terms rather than the
      // middleware's.
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
        // with. An installation still on a manifest that predates this tab
        // therefore 404s every request it makes, and the symptom — a Setup tab
        // whose buttons all fail — says nothing about the cause. Re-registering
        // is what refreshes the routes.
        return (
          "This Nextcloud does not know about Cassini's storage routes, which happens when the app " +
          "was updated in place from a version that predates them. Re-register the app in Nextcloud " +
          "(External Apps → remove and add Cassini again, keeping its data), or use the commands below."
        );
      }
      return error.message;
    }
    return error instanceof Error ? error.message : String(error);
  }

  // appsPageUrl links to Nextcloud's own Apps page, which is where the install
  // flow Cassini cannot perform actually lives.
  function appsPageUrl(step: { app_url: string }): string {
    return nextcloudUrl(step.app_url || "/settings/apps");
  }

  function installToneClass(reason: string): string {
    return reason === "enabled" ? "text-success" : "text-warning";
  }

  // modeSourceLabel says where the recorded mode came from, and it is the one
  // place an administrator can see that nobody chose it.
  //
  // The first pass rendered every source as "Chosen", because both resolvers
  // flattened the provenance to `configured` on the way out — so a fallback an
  // older build wrote down read identically to a decision somebody took. That is
  // the sentence D-708 had to stop the UI from asserting.
  function modeSourceLabel(source: string): string {
    if (source === "user") return "Chosen here";
    if (source === "env") return "Declared by a deploy option (development/CI)";
    if (source === "migrating") return "Left by an interrupted switch — not chosen";
    if (source === "default") return "A fallback an older version recorded — not chosen";
    if (source === "derived") return "Detected from this Nextcloud by an older version — not chosen";
    if (source === "configured") return "Recorded, but Cassini cannot say by whom";
    return source || "—";
  }

  $: transition = status?.transition ?? null;
  // Where the mode in force keeps recordings. Derived rather than fetched: the
  // repair banner has to say which root is safe, and a second round trip to
  // learn it would leave a window where the banner said nothing at all.
  $: activeRoot =
    status?.mode === "access_controlled" ? "Cassini/Recordings" : "CassiniNoACL/Recordings";
  $: unresolved = status !== null && status.mode === "";
  $: askCarry = carryChoiceNeeded(preview);
  $: facts = migrationFacts(preview);
  // Whether this page can act as the administrator at all. False on the
  // standalone build, which has neither Nextcloud's scripts nor its session.
  $: setupAvailable = isSetupAvailable();
  // Building the access-controlled substrate while the DEFAULT mode is the one
  // in force hides that mode's archive: the Team folder takes the `Cassini`
  // path and Nextcloud renames the existing directory out of the way (D-660).
  // Only worth saying when there is a live archive to hide — which is what an
  // active, working default mode means.
  $: strandsArchive =
    pendingKind === "setup" &&
    pending?.mode === "access_controlled" &&
    status?.mode === "default" &&
    status?.ok === true;
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
        <!-- Not "default": nothing has been chosen, and an unchecked instance is
             not evidence that either mode would work. The remedy is the choice,
             not a re-enable — telling an administrator to disable and re-enable
             the app here would send them back through the edge that produced
             this state. -->
        <div class="alert alert-warning items-start gap-3 text-sm" role="status">
          <TriangleAlert size={16} class="mt-0.5 shrink-0" aria-hidden="true" />
          <span>
            No storage mode has been chosen for this Nextcloud, so nothing is published or
            recorded. Pick one below.
          </span>
        </div>
      {/if}

      {#if credential}
        <!-- Shown above everything else, and it does not go away on its own:
             there is no second chance at this string. -->
        <PasswordReveal
          user={credential.user}
          password={credential.password}
          resetOcc={status.service_account.reset_occ}
          on:acknowledge={() => (credential = null)}
        />
      {/if}

      {#if outcome}
        <!-- What the last successful action did. It stays until the next one
             starts, because nothing here reloads the page out from under it. -->
        <div
          class="alert items-start gap-3 text-sm {outcome.tone === 'warning'
            ? 'alert-warning'
            : 'alert-success'}"
          role="status"
        >
          <div class="grid gap-1">
            <p class="font-semibold">{outcome.message}</p>
            {#if outcome.detail}
              <p class="text-xs break-words">{outcome.detail}</p>
            {/if}
          </div>
        </div>
      {/if}

      {#if !status.migration_clean}
        <!-- A switch that stopped part way. The archive is COMPLETE at the mode
             above — the operator copies before it flips, and only clears
             afterwards — so this is a tidy-up, not a rescue. Saying that first
             is what stops it reading as data loss. -->
        <div class="grid gap-2 rounded-box border border-warning bg-warning/10 p-3" role="status">
          <p class="text-sm font-semibold">A storage switch did not finish.</p>
          <p class="text-xs break-words text-base-content/80">
            Your recordings are all in <code class="break-all">{activeRoot}</code>, which is the mode
            above and the one Cassini reads.
            {#if status.pending_cleanup}
              <code class="break-all">{status.pending_cleanup}</code> still holds a leftover copy that
              nothing reads.
            {/if}
            Finishing the switch clears it.
          </p>
          <button
            class="btn btn-sm btn-warning w-fit"
            type="button"
            disabled={repairing || switching}
            on:click={finishMigration}
          >
            {#if repairing}
              <span class="loading loading-spinner loading-xs" aria-hidden="true"></span>
              Finishing…
            {:else}
              Finish the switch
            {/if}
          </button>
        </div>
      {:else if status.stranded_recordings > 0}
        <!-- Not an error: publishing and reading both work. But an archive in the
             mode that is NOT in force is invisible, and "my recordings are gone"
             is the worst way to discover a mode nobody switched. -->
        <div class="grid gap-2 rounded-box border border-info/50 bg-info/10 p-3" role="status">
          <p class="text-sm font-semibold">
            {status.stranded_recordings}
            {status.stranded_recordings === 1 ? "recording is" : "recordings are"} in the other storage
            mode.
          </p>
          <p class="text-xs break-words text-base-content/80">
            They are in <code class="break-all">{status.stranded_root}</code>, which the
            {status.mode === "access_controlled" ? "access controlled" : "default"} mode does not read,
            so they are not listed. Nothing is lost. Switching modes copies them across.
          </p>
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

            <!-- What is actually in this mode's folder. It is reported for BOTH
                 modes, always, because "my recordings are gone" is the symptom
                 of a mode nobody switched — and an unread folder says so rather
                 than reading as an empty one. -->
            <p class="text-xs break-words text-base-content/60">
              <code class="break-all">{option.root}</code> — {describeArchive(option.archive)}
            </p>

            {#if option.blocker}
              <div class="grid gap-2 rounded-box border border-warning/50 bg-warning/10 p-2">
                <p class="text-xs break-words">{option.blocker}</p>
                {#if option.setup.length > 0}
                  <!-- What Cassini would do, itemised. This is the list an
                       administrator is agreeing to, so it is on screen BEFORE
                       the button rather than inside the confirmation only. -->
                  <ul class="grid gap-1 text-xs">
                    {#each option.setup as step (step.id)}
                      <li class="flex items-start gap-1.5">
                        <span class="mt-0.5 shrink-0 text-base-content/40" aria-hidden="true">•</span>
                        <span class="break-words">
                          {step.title}
                          {#if !step.browser}
                            <!-- Nextcloud demands the administrator's password on
                                 the request itself for these. Cassini asks its
                                 operator to try, and says so plainly rather than
                                 promising something it may not be able to do. -->
                            <span class="text-base-content/60">— Cassini will try; Nextcloud may ask you to do this one yourself</span>
                          {/if}
                        </span>
                      </li>
                    {/each}
                  </ul>
                {/if}
                {#if option.instructions.length > 0}
                  <details class="text-xs">
                    <summary class="cursor-pointer text-base-content/70">Or run it yourself</summary>
                    <pre
                      class="m-0 mt-1 overflow-x-auto rounded-box bg-base-200 p-2 font-mono text-xs leading-relaxed">{option.instructions.join(
                        "\n",
                      )}</pre>
                  </details>
                {/if}
              </div>
            {/if}

            <button
              class="btn btn-sm mt-1 w-full {option.active && option.available
                ? 'btn-disabled'
                : option.available
                  ? 'btn-outline'
                  : 'btn-primary btn-outline'}"
              type="button"
              role="radio"
              aria-checked={option.active}
              disabled={switching ||
                credential !== null ||
                (option.active && option.available) ||
                (!option.available && option.setup.length === 0)}
              on:click={() =>
                option.active ? requestSetupFor(option) : requestSwitch(option)}
            >
              {#if option.active && option.available}
                In use
              {:else if !option.available && option.setup.length === 0}
                Not available yet
              {:else if !option.available}
                Set up {option.label.toLowerCase()}
              {:else}
                Switch to {option.label.toLowerCase()}
              {/if}
            </button>
          </div>
        {/each}
      </div>

      {#if status.installs.length > 0}
        <!-- What the operator's own attempt at the app installs produced. A
             refusal is not a failure of the flow: it is the one step Cassini
             cannot take for you, and Nextcloud's own Apps page is where it
             lives. -->
        <div class="grid gap-2 rounded-box border border-base-300 bg-base-200 p-3">
          <p class="text-sm font-semibold">Nextcloud apps</p>
          {#each status.installs as install (install.app)}
            <div class="grid gap-1">
              <p class="text-xs {installToneClass(install.reason)}">
                {install.app}: {install.ok ? "installed and enabled" : "not installed"}
              </p>
              {#if install.detail}
                <p class="text-xs break-words text-base-content/70">{install.detail}</p>
              {/if}
            </div>
          {/each}
          <a
            class="btn btn-sm btn-outline w-fit"
            href={appsPageUrl({ app_url: "/settings/apps" })}
            target="_top"
          >
            Open Nextcloud's Apps page
          </a>
        </div>
      {/if}

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
          aria-label={pendingKind === "setup"
            ? "Confirm Nextcloud setup changes"
            : "Confirm storage mode change"}
        >
          <div class="flex items-start gap-2">
            <TriangleAlert size={18} class="mt-0.5 shrink-0 text-warning" aria-hidden="true" />
            <div class="grid gap-1">
              {#if pendingKind === "setup"}
                <p class="text-sm font-semibold">
                  Let Cassini make these changes to your Nextcloud?
                </p>
                <ul class="grid gap-1 text-xs text-base-content/80">
                  {#each pending.setup as step (step.id)}
                    <li class="flex items-start gap-1.5">
                      <span class="mt-0.5 shrink-0 text-base-content/40" aria-hidden="true">•</span>
                      <span class="break-words">{step.title}</span>
                    </li>
                  {/each}
                </ul>
                <p class="text-xs break-words text-base-content/70">
                  Cassini acts as you, so Nextcloud may ask you to confirm your password first. It
                  is asked for by Nextcloud's own dialog and Cassini never sees it.
                </p>
                {#if strandsArchive}
                  <!-- Measured, D-660: a Team folder mounted at `Cassini` takes
                       that path and Nextcloud renames the service account's
                       existing directory out of the way. So building the folder
                       while the default mode is live hides the recordings that
                       are in it — until the switch, which finds the renamed tree
                       and moves them. Saying so before the click, because
                       "my recordings vanished" is the worst way to learn it. -->
                  <p class="text-xs break-words text-warning">
                    Recordings published so far will stop being listed once the Team folder exists,
                    until you switch to access controlled — the switch finds them and moves them
                    across. Nothing is deleted.
                  </p>
                {/if}
              {:else}
                <p class="text-sm font-semibold">Switch to {pending.label.toLowerCase()}?</p>
                <p class="text-xs break-words text-base-content/80">{pending.consequence}</p>
                <!-- The policy is above; these are the FACTS. What actually
                     moves, from where, and what is already at the destination.
                     Fetched when this prompt opened, so the numbers are on
                     screen while the decision is being made rather than in the
                     result afterwards. -->
                {#if previewing}
                  <p class="text-xs text-base-content/60" aria-live="polite">
                    <span class="loading loading-spinner loading-xs align-middle" aria-hidden="true"
                    ></span>
                    Working out what would move…
                  </p>
                {:else if preview}
                  {#if askCarry}
                    <!-- Only when the answer would differ. A question with one
                         possible answer is not a question, and asking anyway is
                         how a confirmation stops being read. -->
                    <MigrationPolicy {preview} bind:policy disabled={switching} on:change={onPolicyChanged} />
                  {/if}
                  <div class="grid gap-1 rounded-box bg-base-100/60 p-2 text-xs">
                    {#if !preview.source_readable}
                      <!-- The QA failure, and the one shape this must never
                           render as "nothing to move": nobody managed to look. -->
                      <p class="break-words text-base-content/80">
                        Cassini could not read <code class="break-all">{preview.source_root}</code>,
                        so it cannot say how many recordings would move.
                      </p>
                    {:else}
                      <!-- The operator's own plan, per outcome. Arithmetic here
                           would be a second implementation of the rules, which
                           is exactly how a dialog comes to promise something the
                           operation does not do. -->
                      {#each facts as fact (fact)}
                        <p class="flex items-start gap-1.5 break-words text-base-content/80">
                          <span class="mt-0.5 shrink-0 text-base-content/40" aria-hidden="true">•</span>
                          <span>{fact}</span>
                        </p>
                      {/each}
                    {/if}
                    {#each preview.warnings as warning (warning)}
                      <p class="flex items-start gap-1.5 break-words text-warning">
                        <span class="mt-0.5 shrink-0" aria-hidden="true">•</span>
                        <span>{warning}</span>
                      </p>
                    {/each}
                  </div>
                {:else if previewError}
                  <!-- Not a blocker: the transition has its own guards, and
                       refusing to proceed because we could not COUNT the
                       recordings would be worse than proceeding without the
                       count. But an empty diff must never read as "nothing to
                       move" when nobody managed to look. -->
                  <p class="text-xs break-words text-warning">
                    Cassini could not work out what would move ({previewError}). The switch itself
                    still checks before it writes.
                  </p>
                {/if}
              {/if}
            </div>
          </div>
          <div class="flex flex-wrap items-center gap-2">
            <!-- Disabled while a preview is in flight: confirming then would
                 commit to a policy whose consequences are not yet on screen, and
                 the operator would refuse a choice the administrator never saw.
                 Disabled while an unsaved password is up, because acting again
                 would replace it and there is no second chance at it. -->
            <button
              class="btn btn-sm btn-warning"
              type="button"
              disabled={switching || (pendingKind === "switch" && previewing) || credential !== null}
              on:click={confirmSwitch}
            >
              {#if switching}
                <span class="loading loading-spinner loading-xs" aria-hidden="true"></span>
                {pendingKind === "setup" ? "Setting up…" : "Moving recordings…"}
              {:else if pendingKind === "setup"}
                Yes, set it up
              {:else}
                Yes, switch
              {/if}
            </button>
            <button class="btn btn-sm btn-ghost" type="button" disabled={switching} on:click={cancelSwitch}>
              Cancel
            </button>
            {#if switching && setupProgress}
              <span class="text-xs text-base-content/70">{setupProgress}</span>
            {/if}
          </div>
          {#if pendingKind === "setup" && !setupAvailable}
            <!-- The standalone build, or a page Nextcloud's own scripts did not
                 reach. Cassini cannot act as the administrator there, so say so
                 before the button is pressed rather than after. -->
            <p class="text-xs break-words text-warning">
              This page cannot make the changes itself — open Cassini from Nextcloud's own menu, or
              use the commands under "Or run it yourself".
            </p>
          {/if}
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
            <p class="font-semibold">
              {pendingKind === "setup" ? "Setup did not finish." : "Switching the storage mode failed."}
            </p>
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
            <p class="text-xs break-words">{describeTransition(status)}</p>
          </div>
        </div>
      {/if}

      <!-- The service account's password.
           A control of its own rather than a setup step: a plan is emitted only
           for a mode that is NOT ready, so a healthy instance has no step to
           hang one on — which is exactly when somebody who has lost it needs
           this. Cassini never sees the value; it is minted in this browser and
           set through Nextcloud's own API on your session. -->
      {#if status.service_account.exists}
        <div class="grid gap-2 rounded-box border border-base-300 bg-base-200 p-3">
          <div class="flex items-start gap-2">
            <KeyRound size={16} class="mt-0.5 shrink-0 text-base-content/60" aria-hidden="true" />
            <div class="grid gap-1">
              <p class="text-sm font-semibold">
                The <code>{status.service_account.user}</code> account's password
              </p>
              <p class="text-xs break-words text-base-content/70">
                Every recording is written and read as this account. Cassini does not need its
                password — it signs in a different way — and does not keep a copy, so it cannot
                show you the current one. If you need to log in as
                <code>{status.service_account.user}</code>, set a new one here and save it.
              </p>
            </div>
          </div>
          <button
            class="btn btn-sm btn-outline w-fit"
            type="button"
            disabled={resetting || switching || !setupAvailable}
            on:click={resetServiceAccount}
          >
            {#if resetting}
              <span class="loading loading-spinner loading-xs" aria-hidden="true"></span>
              Setting a new password…
            {:else}
              Set a new password
            {/if}
          </button>
          {#if !setupAvailable}
            <p class="text-xs break-words text-base-content/60">
              This page cannot change it — open Cassini from Nextcloud's own menu, or run
              <code>{status.service_account.reset_occ}</code> on the server.
            </p>
          {/if}
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
