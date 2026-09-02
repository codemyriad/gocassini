<script lang="ts">
  import { onMount } from "svelte";
  import { HardDrive, Lock, RefreshCw, TriangleAlert } from "@lucide/svelte";
  import { OperatorClient, OperatorHttpError } from "./operator/client";
  import { NcSetupError, isSetupAvailable, nextcloudUrl, runSetupPlan } from "./operator/ncSetup";
  import type { StorageModeOption, StorageStatus, StorageTransitionPreview } from "./operator/types";

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
    try {
      const next = await operatorClient.previewStorageSwitch(option.mode === "access_controlled");
      // The prompt may have been cancelled or re-pointed while this was in
      // flight; a diff for a mode nobody is looking at must not appear.
      if (pending?.mode === asked) {
        preview = next.preview;
      }
    } catch (error) {
      if (pending?.mode === asked) {
        previewError = asMessage(error);
      }
    } finally {
      previewing = false;
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
    let plan = option;

    // The apps come FIRST, and the plan is then recomputed. Both halves of that
    // matter, and getting either wrong breaks the ordinary case — a Nextcloud
    // with neither app installed.
    //
    // Order: everything after the apps is inside them. Creating the Team folder
    // POSTs to /index.php/apps/groupfolders/…, which simply 404s while
    // `groupfolders` is not installed, so running the browser steps first
    // aborts the whole run at the folder.
    //
    // Recompute: the operator's probe cannot SEE a Team folder until
    // `groupfolders` is enabled (nc_storage_probe.go gates that read on it), so
    // a plan built beforehand says "create the folder" whether or not one
    // exists. Acting on it would make a second `Cassini` folder.
    if (plan.setup.some((step) => !step.browser)) {
      setupProgress = "Installing Nextcloud apps…";
      // The operator's attempt: it succeeds on releases that predate
      // Nextcloud's password-confirmation hardening, and where an administrator
      // set a bypass range. Its per-app outcome comes back on the status.
      status = await operatorClient.installStorageApps();
      const refreshed = modeOptionFor(status, plan.mode);
      if (refreshed && refreshed.setup.some((step) => !step.browser)) {
        // Still missing. Everything left in the plan lives inside those apps,
        // so stopping here is the honest outcome — the per-app detail on screen
        // says what to do, and Nextcloud's Apps page is one click away.
        return;
      }
      if (refreshed) {
        plan = refreshed;
      }
    }

    const browserSteps = plan.setup.filter((step) => step.browser);
    if (browserSteps.length > 0) {
      await runSetupPlan(browserSteps, {
        onProgress: ({ step, index, total }) => {
          setupProgress = `${index + 1}/${total} — ${step.title}`;
        },
      });
    }
    setupProgress = "Checking…";
    status = await operatorClient.recheckStorage();
  }

  // modeOptionFor re-reads one mode from a refreshed status, so a plan can be
  // recomputed rather than reused after something changed underneath it.
  function modeOptionFor(from: StorageStatus | null, mode: string): StorageModeOption | null {
    return from?.modes.find((option) => option.mode === mode) ?? null;
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
    try {
      if (kind === "setup") {
        await runSetup(target);
      } else {
        status = await operatorClient.putStorage(target.mode === "access_controlled");
      }
      pending = null;
      preview = null;
    } catch (error) {
      switchError = asMessage(error);
      pending = null;
      preview = null;
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
      setupProgress = "";
    }
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

  function modeSourceLabel(source: string): string {
    if (source === "configured") return "Chosen";
    if (source === "derived") return "Detected from this Nextcloud";
    return source || "—";
  }

  $: transition = status?.transition ?? null;
  $: unresolved = status !== null && status.mode === "";
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
                  <div class="grid gap-1 rounded-box bg-base-100/60 p-2 text-xs">
                    {#if preview.nothing_to_move}
                      <p class="break-words text-base-content/80">
                        There are no published recordings to move. Only the mode changes.
                      </p>
                    {:else}
                      <p class="break-words text-base-content/80">
                        <span class="font-semibold"
                          >{preview.meetings}
                          {preview.meetings === 1 ? "recording" : "recordings"}</span
                        >
                        would move from <code class="break-all">{preview.source_root}</code> to
                        <code class="break-all">{preview.destination_root}</code>{preview.catalog_present
                          ? ", along with the meeting index"
                          : ""}.
                      </p>
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
            <button class="btn btn-sm btn-warning" type="button" disabled={switching} on:click={confirmSwitch}>
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
