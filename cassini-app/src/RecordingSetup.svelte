<script lang="ts">
  import { createEventDispatcher, onMount } from "svelte";
  import { BookOpen, CircleAlert, CircleCheck, Clock, Headphones, Info, ListChecks, Settings, TriangleAlert } from "@lucide/svelte";
  import type { OperatorClient } from "./operator/client";
  import { checkLabels, checkStateLabel, checkTone, formatAge, readinessTitle, readinessHealthKey, readinessRows, repairLabels, reportTone, rowActions, rowGuide, sharedCheckTime, talkRoomURL, talkSettingsURL as buildTalkSettingsURL, testInFlight, toneClasses, type CheckTone, type RecordingReadiness, type RecordingSetupUpdate } from "./operator/readiness";
  import { onSetupChanged, notifySetupChanged } from "./operator/setupSignal";
  export let operatorClient: Pick<OperatorClient, "getReadiness" | "checkReadiness" | "repairReadiness" | "updateRecordingSetup">;
  // Review fixtures use an inert origin for generated host instructions.
  export let provisioningBase: string | undefined = undefined;
  // Storage is configured in Publish pipeline, and the checks now live in their
  // own Doctor panel — so this action has to move the reader there. It used to
  // scrollIntoView an id that was on the same page; from here that id is not
  // mounted at all, and the button would silently do nothing.
  const dispatch = createEventDispatcher<{ openStorage: void }>();
  let report: RecordingReadiness | null = null;
  let secret = "";
  let busy = false;
  let error = "";
  let stale = false;
  let panel = "";
  let panelOwner = "";
  // The rows the operator sent, unaltered.
  //
  // A failed refresh used to rewrite EVERY row — state forced to not_verified,
  // message replaced with "Could not refresh this check" — so one failed
  // request erased the whole diagnosis. With no backend configured, the single
  // real fault disappeared because a fetch failed. The findings the operator
  // last gave are still the findings; that the panel could not reach it again
  // is reported once, by the error banner, and belongs to the panel rather than
  // to any check.
  $: rows = report ? readinessRows(report) : [];
  $: verdict = stale ? "Recording setup needs verification" : report ? readinessTitle(report) : "";
  $: verdictTone = report && !stale ? reportTone(report) : "neutral";
  $: shared = report ? sharedCheckTime(report.checks) : null;
  const calloutTone: Record<CheckTone, string> = {
    success: "alert-success alert-tinted",
    warning: "alert-warning alert-tinted",
    error: "alert-error alert-tinted",
    neutral: "",
  };
  const toneIcons: Record<CheckTone, typeof CircleCheck> = {
    success: CircleCheck,
    warning: TriangleAlert,
    error: CircleAlert,
    neutral: Info,
  };
  let provisioningURL = "";
  let talkSettingsURL = "";
  // The href for the test conversation. The operator's test_room_url carries
  // the token; behind AppAPI its origin is an internal hostname, so the link a
  // reader clicks has to be rebuilt against this page's own base.
  $: testRoomHref = nextcloudBase && report?.test_room_url ? talkRoomURL(nextcloudBase, report.test_room_url) : "";
  let nextcloudBase = "";
  let alive = true;
  // True only while a re-probe is in flight, so a row can say it is being
  // checked. Distinct from `busy`, which is also set by a plain read and by
  // saving an edit — neither of which re-probes anything.
  let checking = false;
  // The row a scoped check is running for, or "" while every probe runs. Only
  // that row shows a spinner: marking them all would claim work that is not
  // happening.
  let checkingOnly = "";

  // A read that does not announce itself: no busy, so no button is disabled and
  // nothing flickers. Used only while waiting for a recording that is already
  // under way — see followTest below.
  async function refreshQuietly() {
    if (busy) return;
    try {
      const next = await operatorClient.getReadiness();
      if (!alive) return;
      const changed = readinessHealthKey(report) !== readinessHealthKey(next);
      report = next;
      if (changed) notifySetupChanged();
    } catch {
      // A failed background read says nothing the reader needs. The findings on
      // screen are still the last ones the operator gave; an error banner for a
      // refresh nobody asked for is the noise this panel had before.
    }
  }

  // One probe can establish several rows — the Talk probe reports both the
  // backend and the connection — so a check asked for by one of them refreshes
  // the others too. Saying so beats a spinner on one row while another silently
  // changes underneath it.
  function sharesProbe(check: { probe?: string }, withRow: string): boolean {
    if (!check.probe || !withRow) return false;
    return rows.some(row => row.id === withRow && row.probe === check.probe);
  }

  async function load(check = false, only = "") {
    // One guard, and only for work this reader started. There was a second one
    // for the five-second refresh, which dropped any click landing during a
    // read; the refresh is gone and so is the guard.
    if (busy) return;
    busy = true; checking = check; checkingOnly = only; error = "";
    try {
      const next = check
        ? await operatorClient.checkReadiness(only ? [only] : undefined)
        : await operatorClient.getReadiness();
      if (!alive) return;
      const changed = readinessHealthKey(report) !== readinessHealthKey(next);
      report = next; stale = false; error = "";
      if (changed) notifySetupChanged();
    } catch (e) { if (alive) { stale = true; error = e instanceof Error ? e.message : String(e); } }
    finally { busy = false; checking = false; checkingOnly = ""; }
  }
  async function save(payload: RecordingSetupUpdate) {
    if (busy) return;
    // A background GET must neither swallow an edit nor overwrite its response.
    busy = true; error = "";
    try {
      const next = await operatorClient.updateRecordingSetup(payload);
      if (alive) { report = next; stale = false; error = ""; secret = ""; notifySetupChanged(); }
    } catch (e) { if (alive) error = e instanceof Error ? e.message : String(e); }
    finally { busy = false; }
  }
  // The operator performs the repair; this asks it to start and takes the
  // checklist it answers with.
  async function repair(action: string) {
    if (!action || busy) return;
    busy = true; error = "";
    try {
      const next = await operatorClient.repairReadiness(action);
      if (alive) { report = next; stale = false; error = ""; }
    } catch (e) { if (alive) error = e instanceof Error ? e.message : String(e); }
    finally { busy = false; }
  }
  async function action(name: string, owner: string, checkable = false) {
    if (name === "recheck") { await load(true, checkable ? owner : ""); return; }
    if (name === "setup_storage") { dispatch("openStorage"); return; }
    const closing = panel === name && panelOwner === owner;
    panelOwner = owner;
    panel = closing ? "" : name;
    // Unconditionally: setup_hpb, configure_talk and test_recording all link
    // into Nextcloud too, and deriving this only for connect_talk left their
    // links absent — the HPB panel, the one row a broken install leads with,
    // offered no way to reach the settings page it names.
    await resolveNextcloudLinks();
  }
  // Derived from the current operator URL, preserving installations under a
  // subdirectory.
  async function resolveNextcloudLinks() {
    if (nextcloudBase) return;
    const base = new URL(provisioningBase ?? (await import("./operator/config")).loadConfig().operatorBasePath, window.location.href);
    nextcloudBase = base.href;
    provisioningURL = base.href.replace(/\/$/, "") + "/talk/provisioning";
    talkSettingsURL = buildTalkSettingsURL(base.href);
  }
  onMount(() => {
    alive = true;
    // READ ONLY. Opening the panel must not run the checks: they are probes —
    // a media doctor subprocess, a Talk round trip, a whole-archive PROPFIND —
    // and they run when an administrator asks for them, not because a page was
    // loaded (review 2026-09-25).
    //
    // So this is a GET of findings the operator already holds. Rows nobody has
    // checked yet say so, and "Run all checks" or a row's own button takes a
    // reading.
    void load(false);
    // So the test room link is ready without opening a panel first.
    void resolveNextcloudLinks();
    // While a test recording is under way, follow it.
    //
    // This is NOT the five-second health poll that was removed: it is a cheap
    // READ of findings the operator already holds, it runs no probe, it
    // disables no button, and it stops by itself the moment the recording
    // reaches a state only a person can move on from. Without it the one part
    // of this panel whose state advances on its own — Talk starts the
    // recording, the job records, uploads, builds and publishes, none of it
    // touched by the reader — sat still until somebody reloaded the page.
    // Reported from staging: "I had to reload the doctor panel for it to catch
    // my test recording."
    const follow = setInterval(() => { if (testInFlight(report)) void refreshQuietly(); }, 5000);
    // A setup change elsewhere means what is on screen is out of date, so
    // re-READ it. Deliberately not a re-probe: nobody asked for one, and a page
    // reacting to its own events is how a panel starts checking on its own.
    const unsubscribe = onSetupChanged(() => void load(false));
    // NO POLL. The operator establishes a baseline once when its container
    // boots, and after that the findings change only when somebody asks for a
    // check — so there is nothing for a five-second refresh to discover. It
    // re-read unchanged findings twelve times a minute, disabled every button
    // while it did, and dropped clicks that landed on it.
    //
    // What this gives up, knowingly: a check run in another tab is not picked
    // up here, and a running re-index does not advance on its own. Run all
    // checks shows both.
    return () => { alive = false; secret = ""; unsubscribe(); clearInterval(follow); };
  });
</script>

<div class="@container space-y-4">
<header class="op-panel-head">
  <div>
    <div class="op-panel-title justify-between">
      <h1 id="doctor-title">Doctor</h1>
      <button class="op-btn inline-flex items-center gap-1.5" type="button" disabled={busy} on:click={() => load(true)}><ListChecks size={15} aria-hidden="true" />{busy ? "Checking…" : "Run all checks"}</button>
    </div>
    <p>What Cassini needs to record and search meetings, and what to do when something needs attention.</p>
  </div>
</header>
{#if verdict}
  <div class="alert alert-soft items-start gap-2 px-3 py-2 text-sm {calloutTone[verdictTone]}" role="status">
    <svelte:component this={toneIcons[verdictTone]} size={16} class="mt-0.5 shrink-0 {verdictTone === 'neutral' ? 'opacity-70' : toneClasses[verdictTone]}" aria-hidden="true" />
    <div class="flex min-w-0 flex-wrap items-baseline gap-x-2 gap-y-0.5">
      <p class="font-semibold text-base-content">{verdict}</p>
      {#if shared}<p class="text-xs text-base-content/70" title={new Date(shared.checkedAt).toLocaleString()}>Checked {formatAge(shared.checkedAt)}</p>{/if}
    </div>
  </div>
{/if}
{#if error}<p role="alert" class="text-sm {toneClasses.error}">{error}</p>{/if}
{#if report}
<section class="op-tint px-4 py-3.5 @md:px-5 @md:py-2" aria-labelledby="doctor-title" aria-busy={busy}>
    <ul class="divide-y divide-base-300">
      {#each rows as check, index}
        <li class="py-3" data-check-id={check.id}>
          <div class="flex flex-wrap items-center justify-between gap-x-3 gap-y-2">
            <p class="flex min-w-0 flex-[1_1_12rem] flex-wrap items-baseline gap-x-2 py-1 font-medium">{checkLabels[check.id] ?? check.id} <span class="inline-flex items-baseline gap-1 whitespace-nowrap text-xs font-normal {toneClasses[checkTone(check)]}"><svelte:component this={toneIcons[checkTone(check)]} size={14} class="shrink-0 self-center" aria-hidden="true" />{checkStateLabel(check)}</span>{#if checking && check.checkable && (checkingOnly === "" || checkingOnly === check.id || sharesProbe(check, checkingOnly))}<span class="inline-flex items-center gap-1 whitespace-nowrap text-xs font-normal text-base-content/65"><span class="loading loading-spinner loading-xs" aria-hidden="true"></span>Checking…</span>{/if}</p>
            <div class="flex flex-wrap gap-2 *:[--size:1.75rem]">
              {#if check.repair && repairLabels[check.repair]}
                <!-- Only a repair this build knows how to name. "Fix this" for an
                     unrecognised action offered a button whose effect the panel
                     could not describe, which is the panel speaking for the
                     operator again. -->
                <button class="btn btn-sm btn-primary" disabled={busy}
                  on:click={() => repair(check.repair ?? "")}>{repairLabels[check.repair]}</button>
              {/if}
              {#if check.checkable && rowActions(check).every((item) => item.action !== "recheck")}
                <button class="btn btn-sm btn-outline btn-outline-quiet btn-outline-hover" disabled={busy}
                  on:click={() => load(true, check.id)}>Check</button>
              {/if}
              {#each rowActions(check) as item}
                <button class="btn btn-sm btn-outline {item.action === "recheck" ? "btn-outline-quiet" : ""} btn-outline-hover" disabled={busy} aria-expanded={item.action === "recheck" || item.action === "setup_storage" ? undefined : panel === item.action && panelOwner === check.id} on:click={() => action(item.action, check.id, check.checkable ?? false)}>{item.label}</button>
              {/each}
            </div>
          </div>
          <p class="mt-1 text-sm text-base-content/70">{check.message}</p>
          {#if (check.steps ?? []).length > 0}
            <!-- Behind a disclosure, as SetupNotice does it: an
                 administrator who wants to press a button never has to read
                 a command line, and one who wants the commands can open
                 them. -->
            <details class="mt-2">
              <summary class="cursor-pointer text-xs text-base-content/70">What to do about it</summary>
              <ul class="mt-2 space-y-2">
                {#each check.steps ?? [] as step}
                  <li class="text-xs text-base-content/80">{step.label}</li>
                {/each}
              </ul>
            </details>
          {/if}
          {#if check.checked_at && (check.code === "test_playback" || !shared?.ids.has(check.id))}<p class="mt-2 flex items-center gap-1 text-xs text-base-content/65" title={new Date(check.checked_at).toLocaleString()}><svelte:component this={check.code === "test_playback" ? Headphones : Clock} size={12} class="shrink-0" aria-hidden="true" />{check.code === "test_playback" ? "Confirmed" : "Checked"} {formatAge(check.checked_at)}</p>{/if}
          {#if rowGuide(check)}
            <div class="mt-3 flex flex-wrap gap-2">
              <a class="op-btn inline-flex h-auto min-h-8 items-center gap-1.5 py-1.5 text-left text-xs!" href={rowGuide(check)?.href} target="_blank" rel="noreferrer"><BookOpen size={15} class="shrink-0" aria-hidden="true" />{rowGuide(check)?.label}</a>
              {#if talkSettingsURL && check.id === "talk.hpb"}
                <a class="btn btn-sm btn-outline btn-outline-hover" href={talkSettingsURL} target="_blank" rel="noreferrer"><Settings size={15} aria-hidden="true" />Open Talk settings</a>
              {/if}
            </div>
          {/if}
    {#if panel && panelOwner === check.id && rows.findIndex(row => row.id === check.id) === index}
      <div class="mt-3 rounded-box bg-base-200 p-4">
        {#if panel === "configure_talk"}
          <h3 class="font-semibold">Connect to Talk’s signaling server</h3>
          <p class="my-2 text-sm">Use the signaling server’s internal client secret. This is different from the recording-backend secret, which Cassini generates itself.</p>
          {#if report.secret_source === "env"}
            <p class="text-sm">Managed by deployment configuration. Change CASSINI_TALK_SIGNALING_INTERNAL_SECRET in Cassini’s deploy options.</p>
          {:else}
            <p class="my-2 text-sm">{report.secret_configured ? "An internal secret is saved. Enter a new value to replace it." : "No internal secret is saved."}</p>
            <form on:submit|preventDefault={() => save({ internal_secret: secret })}>
              <label class="form-control block">Internal secret<input class="input input-bordered mt-1 block w-full" type="password" autocomplete="new-password" bind:value={secret} /></label>
              <button class="btn btn-primary btn-sm mt-3" disabled={busy || !secret.trim()}>Save secret</button>
            </form>
          {/if}
          <p class="mt-3 text-sm text-base-content/70">This is the internal secret your Talk signaling server is configured with. Cassini cannot read it from Talk, which is why it is asked for here.</p>
          {#if talkSettingsURL}<p class="mt-1 text-sm"><a class="link" href={talkSettingsURL} target="_blank" rel="noreferrer">Open Talk's administration settings</a></p>{/if}
          <!-- No "Test connection" button. It fired the Talk connection check,
               which authenticates with the RECORDING secret and can say nothing
               about this one — the row this form belongs to is the backend row,
               and its own Check button is what tries the credential. -->
        {:else if panel === "connect_talk"}
          <h3 class="font-semibold">Use Cassini as Talk’s recording backend</h3>
          <p class="my-2 text-sm">Talk needs Cassini's recording-server URL and its recording secret. Cassini generates the secret itself but cannot write Talk's configuration, so the values have to be given to Talk.</p>
          {#if provisioningURL}<p class="text-sm"><a class="link" href={provisioningURL} target="_blank" rel="noreferrer">Show both values</a></p>{/if}
          {#if talkSettingsURL}<p class="mt-1 text-sm"><a class="link" href={talkSettingsURL} target="_blank" rel="noreferrer">Open Talk's administration settings</a></p>{/if}
        {:else if panel === "test_recording"}
          <h3 class="font-semibold">Record a test through Talk</h3>
          <!-- No configuration. This step used to begin "choose a dedicated test
               room", which is the one thing Cassini can do for itself, and which
               made the whole tool unreachable until somebody pasted a URL. -->
          <p class="my-2 text-sm">Cassini makes itself a conversation for this and waits. The recording is started from Talk, by you, exactly as a real one would be — which is what makes it worth running.</p>
          <ol class="my-3 list-inside list-decimal space-y-2 text-sm">
            <li>Press Prepare test. Cassini creates a conversation named “Cassini recording test” in your name, so that you can moderate it — Talk’s Start recording action belongs to a conversation’s moderators.</li>
            <li>Open the room, join the call, and use Talk’s Start recording action.</li>
            <li>Say a few words, then stop the recording in Talk.</li>
            <li>Wait for it to publish, then play the audio and confirm you can hear it.</li>
          </ol>
          <p class="mb-3 text-sm text-base-content/70">A recording captures a call, so the call needs someone in it: Cassini joins to record, not to talk. The conversation and the test recording are both ordinary ones, and can be deleted afterwards.</p>
          <button class="btn btn-primary btn-sm" disabled={busy} on:click={() => save({ action: "arm_test" })}>{report.test.started_at ? "Prepare a new test" : "Prepare test"}</button>
          {#if testRoomHref}<a class="btn btn-sm ml-2" href={testRoomHref} target="_blank" rel="noreferrer">Open test room</a>{/if}
          {#if report.test.started_at}
            <p class="mt-3 text-sm" role="status">{report.test.state === "waiting_for_talk" ? "Waiting for a recording to start. Open the test room, join the call, and use Talk’s Start recording action." : `${report.test.stage ?? "Test"}: ${report.test.state}`}</p>
            {#if report.test.job_id}<p class="mt-1 text-xs">Recording {report.test.job_id}</p>{/if}
            {#if report.test.published && report.test.viewer_url}
              <a class="btn btn-sm mt-3" href={report.test.viewer_url} target="_blank" rel="noreferrer">Open published recording</a>
              <button class="btn btn-sm mt-3" disabled={busy || !!report.test.playback_verified_at} on:click={() => save({ action: "confirm_playback", job_id: report?.test.job_id })}>I played the published audio</button>
            {/if}
          {/if}
        {:else if panel === "settings"}
          <p class="text-sm">Review optional transcription below in Publish pipeline. Recording and playback can work without a transcript. CPU transcription is supported; a GPU is optional.</p>
        {:else if panel === "repair_configuration"}
          <!-- Named, not a fall-through. This was the {:else}, so ANY action the
               panel had no branch for showed this — telling a reader to restore
               recording-setup.json from backup for a fault that had nothing to
               do with the file. rowActions now drops an action this build
               cannot name, so there is no longer an unnamed branch to land in. -->
          <h3 class="font-semibold">Cassini’s saved configuration cannot be read</h3>
          <p class="my-2 text-sm">Ask your server administrator to check Cassini’s persistent volume and restore recording-setup.json from backup, then restart Cassini and check again.</p>
        {/if}
        <button class="btn btn-ghost btn-sm mt-4" on:click={() => { panel = ""; secret = ""; }}>Close</button>
      </div>
    {/if}
        </li>
      {/each}
    </ul>
</section>
{/if}
</div>
