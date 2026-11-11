<script lang="ts">
  import { dateFormat, LL } from "cassini-viewer/i18n";
  import { createEventDispatcher, onMount } from "svelte";
  import { BookOpen, Check, CircleAlert, CircleCheck, Clock, FileSearch, Headphones, Info, ListChecks, Play, RefreshCw, Settings, TextSearch, TriangleAlert, Video, X } from "@lucide/svelte";
  import type { OperatorClient } from "./operator/client";
  import CommandBlock from "cassini-viewer/ui/CommandBlock.svelte";
  import { checkLabels, checkStateLabel, checkTone, describeRefreshFailure, formatAge, labelParts, readinessTitle, testFollowUp, readinessHealthKey, readinessRows, repairLabels, repairLabel, reportTone, rowActions, rowGuide, sharedCheckTime, talkRoomURL, talkSettingsURL as buildTalkSettingsURL, testInFlight, toneClasses, unsharedSummary, shareLimitedSummary, shareLimitCommands, type CheckTone, type RecordingReadiness, type RecordingSetupUpdate } from "./operator/readiness";
  import { checkMessage, operatorErrorText, stepLabel } from "./operator/doctorText";
  import { onSetupChanged, notifySetupChanged } from "./operator/setupSignal";
  export let operatorClient: Pick<OperatorClient, "getReadiness" | "checkReadiness" | "repairReadiness" | "updateRecordingSetup">;
  // Review fixtures use an inert origin for generated host instructions.
  export let provisioningBase: string | undefined = undefined;
  // Storage is configured in Publish pipeline, and the checks now live in their
  // own Doctor panel — so this action has to move the reader there. It used to
  // scrollIntoView an id that was on the same page; from here that id is not
  // mounted at all, and the button would silently do nothing.
  // openRuns: the Recording sharing row's count of what is unshared leads to
  // the run console, where each meeting is re-shared (D-885).
  const dispatch = createEventDispatcher<{ openStorage: void; openRun: string; openRuns: void }>();
  let report: RecordingReadiness | null = null;
  let secret = "";
  let busy = false;
  let error = "";
  let stale = false;
  let refreshFailure = "";
  let lastLoad = { check: false, only: "" };
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
  $: verdict = stale ? $LL.app.operator.readiness.title.not_verified() : report ? readinessTitle(report) : "";
  $: verdictTone = report && !stale ? reportTone(report) : "neutral";
  $: shared = report ? sharedCheckTime(report.checks) : null;
  $: followUp = report && !stale ? testFollowUp(report) : "";
  let panelRoot: HTMLElement | null = null;
  function goToTest(): void {
    const row = panelRoot?.querySelector<HTMLElement>('[data-check-id="test"]');
    if (!row) return;
    const reduced = window.matchMedia?.("(prefers-reduced-motion: reduce)").matches;
    row.scrollIntoView({ behavior: reduced ? "auto" : "smooth", block: "center" });
    row.querySelector<HTMLElement>(".mt-3 a, .mt-3 button")?.focus({ preventScroll: true });
  }
  $: waitingForTalk = report?.test.state === "waiting_for_talk";
  $: awaitingPlayback = !!report?.test.published && !!report?.test.viewer_url && !report?.test.playback_verified_at;
  $: testFailed = report?.test.state === "failed";
  $: testSteps = [
    $LL.app.operator.doctor.test.step_prepare(),
    $LL.app.operator.doctor.test.step_record(),
    $LL.app.operator.doctor.test.step_speak(),
    $LL.app.operator.doctor.test.step_play(),
  ];
  // The verdict and its follow-up are one sentence, with the follow-up as a
  // button: split the translated sentence around it.
  function followUpParts(verdictText: string, followUpText: string, but: boolean): [string, string] {
    const marker = "\u0000";
    const sentence = but
      ? $LL.app.operator.doctor.verdict_but_follow_up({ verdict: verdictText, follow_up: marker })
      : $LL.app.operator.doctor.verdict_and_follow_up({ verdict: verdictText, follow_up: marker });
    const [before, after = ""] = sentence.split(marker);
    return [before, after];
  }
  $: followUpSentence = followUp ? followUpParts(verdict, followUp, testFailed && report?.recording_state === "passed") : null;
  $: testProgress = !report ? 0
    : report.test.playback_verified_at ? 4
    : report.test.published ? 3
    : report.test.job_id ? 2
    : report.test.started_at ? 1
    : 0;
  $: testStepsDone = testSteps.map((_, stepIndex) => stepIndex < testProgress);
  const calloutTone: Record<CheckTone, string> = {
    success: "alert-success alert-tinted",
    warning: "alert-warning alert-tinted",
    error: "alert-error alert-tinted",
    neutral: "op-tint",
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
  // Only a room the reader owns is worth linking to. Talk's Start recording
  // action belongs to a conversation's moderators, so a room the connection
  // check made (it creates one as Cassini's own account, needing only a token
  // to read recording settings with) or one a different administrator armed is
  // a call this reader cannot record in — and the steps beside the link tell
  // them to go and record. With no link the panel offers "Prepare a new test",
  // which makes them a room of their own.
  $: testRoomHref = nextcloudBase && report?.test_room_url && report?.test_room_mine ? talkRoomURL(nextcloudBase, report.test_room_url) : "";
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
    lastLoad = { check, only };
    busy = true; checking = check; checkingOnly = only; error = "";
    try {
      const next = check
        ? await operatorClient.checkReadiness(only ? [only] : undefined)
        : await operatorClient.getReadiness();
      if (!alive) return;
      const changed = readinessHealthKey(report) !== readinessHealthKey(next);
      report = next; stale = false; error = "";
      if (changed) notifySetupChanged();
    } catch (e) { if (alive) { stale = true; refreshFailure = describeRefreshFailure(e); } }
    finally { busy = false; checking = false; checkingOnly = ""; }
  }
  async function save(payload: RecordingSetupUpdate) {
    if (busy) return;
    // A background GET must neither swallow an edit nor overwrite its response.
    busy = true; error = "";
    try {
      const next = await operatorClient.updateRecordingSetup(payload);
      if (alive) { report = next; stale = false; error = ""; secret = ""; notifySetupChanged(); }
    } catch (e) { if (alive) error = operatorErrorText(e); }
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
    } catch (e) { if (alive) error = operatorErrorText(e); }
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
    const follow = setInterval(() => { if (testInFlight(report) || report?.checks.some(check => check.running)) void refreshQuietly(); }, 5000);
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
    // up here. Run all checks shows it.
    return () => { alive = false; secret = ""; unsubscribe(); clearInterval(follow); };
  });
</script>

<div class="@container space-y-4" bind:this={panelRoot}>
<header class="op-panel-head">
  <div>
    <div class="op-panel-title justify-between">
      <h1 id="doctor-title">{$LL.app.operator.doctor.title()}</h1>
      <button class="op-btn inline-flex items-center gap-1.5" type="button" disabled={busy} on:click={() => load(true)}><ListChecks size={15} aria-hidden="true" />{busy ? $LL.app.operator.doctor.checking() : $LL.app.operator.doctor.run_all()}</button>
    </div>
    <p>{$LL.app.operator.doctor.intro()}</p>
  </div>
</header>
{#if stale}
  <div class="alert alert-soft alert-warning alert-tinted items-start gap-2 px-3 py-2 text-sm" role="alert">
    <TriangleAlert size={16} class="mt-0.5 shrink-0 {toneClasses.warning}" aria-hidden="true" />
    <div class="min-w-0">
      <p class="font-semibold text-base-content">{$LL.app.operator.doctor.refresh_failed()}</p>
      <p class="mt-0.5 text-base-content/80">{refreshFailure}</p>
      <div class="mt-2 flex flex-wrap items-center gap-x-3 gap-y-1">
        <button type="button" class="op-btn inline-flex h-auto min-h-8 items-center gap-1.5 py-1.5 text-left text-xs!" disabled={busy} on:click={() => load(lastLoad.check, lastLoad.only)}><RefreshCw size={15} class="shrink-0" aria-hidden="true" />{busy ? $LL.app.operator.doctor.trying() : $LL.common.action.try_again()}</button>
        {#if shared}<p class="text-xs text-base-content/70" title={dateFormat({ dateStyle: "medium", timeStyle: "short" }).format(new Date(shared.checkedAt))}>{$LL.app.operator.doctor.last_checked({ age: formatAge(shared.checkedAt) })}</p>{/if}
      </div>
    </div>
  </div>
{:else if verdict}
  <div class="alert alert-soft items-start gap-2 px-3 py-2 text-sm {calloutTone[verdictTone]}" role="status">
    <svelte:component this={toneIcons[verdictTone]} size={16} class="mt-0.5 shrink-0 {verdictTone === 'neutral' ? 'opacity-70' : toneClasses[verdictTone]}" aria-hidden="true" />
    <div class="flex min-w-0 flex-wrap items-baseline gap-x-2 gap-y-0.5">
      <p class="font-semibold text-base-content">{#if followUpSentence}{followUpSentence[0]}<button type="button" class="link font-semibold" on:click={goToTest}>{followUp}</button>{followUpSentence[1]}{:else}{verdict}{/if}</p>
      {#if shared}<p class="text-xs text-base-content/70" title={dateFormat({ dateStyle: "medium", timeStyle: "short" }).format(new Date(shared.checkedAt))}>{$LL.app.operator.doctor.checked({ age: formatAge(shared.checkedAt) })}</p>{/if}
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
            <p class="flex min-w-0 flex-[1_1_9rem] flex-wrap items-baseline gap-x-2 py-1 font-medium">{checkLabels[check.id] ?? check.id} {#if !check.running}<span class="inline-flex items-baseline gap-1 whitespace-nowrap text-xs font-normal {toneClasses[checkTone(check)]}"><svelte:component this={toneIcons[checkTone(check)]} size={14} class="shrink-0 self-center" aria-hidden="true" />{checkStateLabel(check)}</span>{/if}{#if check.id === "test" && waitingForTalk}<span class="inline-flex items-baseline gap-1 whitespace-nowrap text-xs font-normal text-base-content/65"><span class="loading loading-spinner loading-xs self-center" aria-hidden="true"></span>{$LL.app.operator.doctor.waiting()}</span>{/if}{#if check.running}<span class="inline-flex items-baseline gap-1 whitespace-nowrap text-xs font-normal text-base-content/65"><span class="loading loading-spinner loading-xs self-center" aria-hidden="true"></span>{check.id === "archive.search" ? $LL.app.operator.doctor.reindexing() : $LL.app.operator.doctor.running()}</span>{/if}{#if checking && check.checkable && (checkingOnly === "" || checkingOnly === check.id || sharesProbe(check, checkingOnly))}<span class="inline-flex items-baseline gap-1 whitespace-nowrap text-xs font-normal text-base-content/65"><span class="loading loading-spinner loading-xs self-center" aria-hidden="true"></span>{$LL.app.operator.doctor.checking()}</span>{/if}</p>
            <div class="flex flex-wrap gap-2 *:[--size:1.75rem]">
              {#if check.checkable && rowActions(check).every((item) => item.action !== "recheck")}
                <button class="btn btn-sm btn-outline btn-outline-quiet btn-outline-hover" disabled={busy}
                  on:click={() => load(true, check.id)}>{$LL.app.operator.readiness.action.recheck()}</button>
              {/if}
              {#each rowActions(check).filter((item) => item.action === "recheck") as item}
                <button class="btn btn-sm btn-outline btn-outline-quiet btn-outline-hover" disabled={busy} on:click={() => action(item.action, check.id, check.checkable ?? false)}>{item.label}</button>
              {/each}
            </div>
          </div>
          <p class="mt-1 text-sm text-base-content/70">{checkMessage(check)}</p>
          {#if check.unshared}
            <!-- In any state: the setting may be fixed and the meetings it
                 refused still unshared. Each is re-shared from its run, which
                 is where the button goes. -->
            <div class="mt-2 flex flex-wrap items-center gap-x-3 gap-y-1">
              <p class="text-sm text-base-content/70">{unsharedSummary(check.unshared)}</p>
              <button type="button" class="btn btn-sm btn-outline btn-outline-quiet btn-outline-hover" on:click={() => dispatch("openRuns")}>{$LL.app.operator.doctor.open_runs()}</button>
            </div>
          {/if}
          {#if check.share_limited}
            <!-- Nextcloud allows 20 new shares per account every 10 minutes.
                 Each held-back meeting is re-shared from its run, which is
                 where the button goes. The way to lift the limit sits behind
                 a disclosure because it changes a Nextcloud-wide setting. -->
            <div class="mt-2">
              <div class="flex flex-wrap items-center gap-x-3 gap-y-1">
                <p class="text-sm text-base-content/70">{shareLimitedSummary(check.share_limited)}</p>
                <button type="button" class="btn btn-sm btn-outline btn-outline-quiet btn-outline-hover" on:click={() => dispatch("openRuns")}>{$LL.app.operator.doctor.open_runs()}</button>
              </div>
              <details class="group mt-1">
                <summary class="tpl-toggle text-sm! group-open:text-base-content!"><span class="tpl-chev" aria-hidden="true"></span>{$LL.app.operator.doctor.raise_limit()}</summary>
                <div class="mt-2 ml-px space-y-2 border-l-2 border-base-300 pl-3">
                  <p class="text-sm text-base-content/70">{#each labelParts($LL.app.operator.doctor.raise_limit_body({ account: "`cassini`" })) as part}{#if part.code}<code class="rounded bg-base-200 px-1 py-0.5 font-mono text-[0.85em] text-base-content">{part.text}</code>{:else}{part.text}{/if}{/each}</p>
                  {#each shareLimitCommands as command}<CommandBlock {command} />{/each}
                </div>
              </details>
            </div>
          {/if}
          {#if (check.steps ?? []).length > 0}
            <!-- Behind a disclosure, as SetupNotice does it: an
                 administrator who wants to press a button never has to read
                 a command line, and one who wants the commands can open
                 them. -->
            <details class="group mt-2">
              <summary class="tpl-toggle text-sm! group-open:text-base-content!"><span class="tpl-chev" aria-hidden="true"></span>{$LL.app.operator.doctor.what_to_do()}</summary>
              <ul class="mt-2 ml-px space-y-2 border-l-2 border-base-300 pl-3">
                {#each check.steps ?? [] as step}
                  <li class="text-sm text-base-content/70">{#each labelParts(stepLabel(step)) as part}{#if part.code}<code class="rounded bg-base-200 px-1 py-0.5 font-mono text-[0.85em] text-base-content">{part.text}</code>{:else}{part.text}{/if}{/each}{#each step.commands ?? [] as command}<CommandBlock {command} />{/each}</li>
                {/each}
              </ul>
            </details>
          {/if}
          {#if check.checked_at && (check.code === "test_playback" || !shared?.ids.has(check.id))}<p class="mt-2 flex items-center gap-1 text-xs text-base-content/65" title={dateFormat({ dateStyle: "medium", timeStyle: "short" }).format(new Date(check.checked_at))}><svelte:component this={check.code === "test_playback" ? Headphones : Clock} size={12} class="shrink-0" aria-hidden="true" />{check.code === "test_playback" ? $LL.app.operator.doctor.confirmed({ age: formatAge(check.checked_at) }) : $LL.app.operator.doctor.checked({ age: formatAge(check.checked_at) })}</p>{/if}
          {#if (check.repair && repairLabels[check.repair]) || rowGuide(check) || rowActions(check).some((item) => item.action !== "recheck")}
            <div class="mt-3 flex flex-wrap gap-2">
              {#if check.id === "test" && waitingForTalk && testRoomHref}
                <a class="op-btn inline-flex h-auto min-h-8 items-center gap-1.5 py-1.5 text-left text-xs!" href={testRoomHref} target="_blank" rel="noreferrer"><Video size={15} class="shrink-0" aria-hidden="true" />{$LL.app.operator.doctor.open_test_room()}</a>
              {/if}
              {#if check.id === "test" && testFailed}
                {#if report.test.job_id}
                  <button type="button" class="op-btn inline-flex h-auto min-h-8 items-center gap-1.5 py-1.5 text-left text-xs!" on:click={() => dispatch("openRun", report?.test.job_id ?? "")}><FileSearch size={15} class="shrink-0" aria-hidden="true" />{$LL.app.operator.doctor.see_why_stopped()}</button>
                {/if}
                <button class="btn btn-sm btn-outline btn-outline-hover" disabled={busy} on:click={() => save({ action: "arm_test" })}>{$LL.app.operator.doctor.prepare_new_test()}</button>
              {/if}
              {#if check.id === "test" && awaitingPlayback}
                <a class="op-btn inline-flex h-auto min-h-8 items-center gap-1.5 py-1.5 text-left text-xs!" href={report.test.viewer_url} target="_blank" rel="noreferrer"><Play size={15} class="shrink-0" aria-hidden="true" />{$LL.app.operator.doctor.play_recording()}</a>
              {/if}
              {#if check.repair && repairLabels[check.repair]}
                <!-- Only a repair this build knows how to name. "Fix this" for an
                     unrecognised action offered a button whose effect the panel
                     could not describe, which is the panel speaking for the
                     operator again. -->
                <button type="button" class="op-btn inline-flex h-auto min-h-8 items-center gap-1.5 py-1.5 text-left text-xs!" disabled={busy}
                  on:click={() => repair(check.repair ?? "")}><TextSearch size={15} class="shrink-0" aria-hidden="true" />{repairLabel(check)}</button>
              {/if}
              {#if rowGuide(check)}
                <a class="op-btn inline-flex h-auto min-h-8 items-center gap-1.5 py-1.5 text-left text-xs!" href={rowGuide(check)?.href} target="_blank" rel="noreferrer"><BookOpen size={15} class="shrink-0" aria-hidden="true" />{rowGuide(check)?.label}</a>
                {#if talkSettingsURL && check.id === "talk.hpb"}
                  <a class="btn btn-sm btn-outline btn-outline-hover" href={talkSettingsURL} target="_blank" rel="noreferrer"><Settings size={15} aria-hidden="true" />{$LL.app.operator.doctor.open_talk_settings()}</a>
                {/if}
              {/if}
              {#each rowActions(check).filter((item) => item.action !== "recheck") as item}
                <button class="btn btn-sm btn-outline {check.state === "passed" || check.code === "test_in_progress" || check.code === "test_awaiting_playback" || check.code === "test_failed" ? "btn-outline-quiet" : ""} btn-outline-hover" disabled={busy} aria-expanded={item.action === "setup_storage" ? undefined : panel === item.action && panelOwner === check.id} on:click={() => action(item.action, check.id, check.checkable ?? false)}>{item.label}</button>
              {/each}
            </div>
          {/if}
          {#if check.id === "test" && waitingForTalk}<p class="mt-2 text-xs text-base-content/65">{$LL.app.operator.doctor.test_picks_up()}</p>{/if}
          {#if check.running}<p class="mt-2 text-xs text-base-content/65">{$LL.app.operator.doctor.reindex_updates()}</p>{/if}
          {#if check.id === "test" && awaitingPlayback}
            <label class="mt-3 flex w-fit cursor-pointer items-center gap-2 text-sm">
              <input type="checkbox" class="checkbox checkbox-xs border-base-content/55" checked={!!report.test.playback_verified_at} disabled={busy} on:change={() => save({ action: "confirm_playback", job_id: report?.test.job_id })} />
              {$LL.app.operator.doctor.confirm_playback()}
            </label>
          {/if}
    {#if panel && panelOwner === check.id && rows.findIndex(row => row.id === check.id) === index}
      <div class="relative mt-3 rounded-box border border-base-300 bg-base-200 p-4">
        <button class="btn btn-ghost btn-sm btn-square absolute top-3 right-3" type="button" aria-label={$LL.common.action.close()} on:click={() => { panel = ""; secret = ""; }}><X size={16} aria-hidden="true" /></button>
        {#if panel === "configure_talk"}
          <h3 class="pr-10 font-semibold">{$LL.app.operator.doctor.secret.title()}</h3>
          <p class="my-2 text-sm">{$LL.app.operator.doctor.secret.intro()}</p>
          {#if report.secret_source === "env"}
            <p class="text-sm">{#each labelParts($LL.app.operator.doctor.secret.managed({ variable: "`CASSINI_TALK_SIGNALING_INTERNAL_SECRET`" })) as part}{#if part.code}<code class="rounded bg-base-200 px-1 py-0.5 font-mono text-[0.85em] text-base-content">{part.text}</code>{:else}{part.text}{/if}{/each}</p>
          {:else}
            <p class="my-2 text-sm">{report.secret_configured ? $LL.app.operator.doctor.secret.saved() : $LL.app.operator.doctor.secret.not_saved()}</p>
            <form class="mt-3" on:submit|preventDefault={() => save({ internal_secret: secret })}>
              <label class="block text-sm font-semibold" for="talk-internal-secret">{$LL.app.operator.doctor.secret.label()}</label>
              <div class="mt-1 flex flex-col items-start gap-2 @md:flex-row @md:items-stretch">
                <input id="talk-internal-secret" class="input input-bordered w-full min-w-0 @md:w-auto @md:flex-1" type="password" autocomplete="new-password" bind:value={secret} />
                <button class="op-btn inline-flex h-10 shrink-0 items-center justify-center" disabled={busy || !secret.trim()}>{$LL.app.operator.doctor.secret.save()}</button>
              </div>
            </form>
          {/if}
          <p class="mt-3 text-xs text-base-content/65">{$LL.app.operator.doctor.secret.why()}</p>
          {#if talkSettingsURL}<p class="mt-1 text-xs"><a class="link" href={talkSettingsURL} target="_blank" rel="noreferrer">{$LL.app.operator.doctor.open_talk_admin()}</a></p>{/if}
          <!-- No "Test connection" button. It fired the Talk connection check,
               which authenticates with the RECORDING secret and can say nothing
               about this one — the row this form belongs to is the backend row,
               and its own Check button is what tries the credential. -->
        {:else if panel === "connect_talk"}
          <h3 class="pr-10 font-semibold">{$LL.app.operator.doctor.backend.title()}</h3>
          <p class="my-2 text-sm">{$LL.app.operator.doctor.backend.intro()}</p>
          {#if provisioningURL}<p class="text-sm"><a class="link" href={provisioningURL} target="_blank" rel="noreferrer">{$LL.app.operator.doctor.backend.show_values()}</a></p>{/if}
          {#if talkSettingsURL}<p class="mt-1 text-sm"><a class="link" href={talkSettingsURL} target="_blank" rel="noreferrer">{$LL.app.operator.doctor.open_talk_admin()}</a></p>{/if}
        {:else if panel === "test_recording"}
          <h3 class="pr-10 font-semibold">{$LL.app.operator.doctor.test.title()}</h3>
          <!-- No configuration. This step used to begin "choose a dedicated test
               room", which is the one thing Cassini can do for itself, and which
               made the whole tool unreachable until somebody pasted a URL. -->
          <p class="my-2 text-sm">{$LL.app.operator.doctor.test.intro()}</p>
          <ol class="my-3 list-inside list-decimal space-y-2 text-sm">
            {#each testSteps as step, stepIndex}
              <li class={testStepsDone[stepIndex] ? "text-base-content/50" : ""}>{#if testStepsDone[stepIndex]}<span class="sr-only">{$LL.app.operator.doctor.test.step_done()} </span><Check size={14} class="mr-1 inline align-[-2px]" aria-hidden="true" /><span class="line-through">{step}</span>{:else}{step}{/if}</li>
            {/each}
          </ol>
          <p class="mb-3 text-sm text-base-content/70">{$LL.app.operator.doctor.test.note()}</p>
          <div class="flex flex-wrap gap-2">
            <!-- A failed test's actions belong on the row, next to the reason
                 it failed ("See why it stopped" and a retry, above), so this
                 panel offers none. Written as a guard rather than an empty
                 first branch, which read as an oversight and made every build
                 print `block_empty`. -->
            {#if !testFailed}
              {#if (waitingForTalk && testRoomHref) || awaitingPlayback}
                <button class="btn btn-sm btn-outline btn-outline-quiet btn-outline-hover" disabled={busy} on:click={() => save({ action: "arm_test" })}>{$LL.app.operator.doctor.prepare_new_test()}</button>
              {:else}
                <button type="button" class="op-btn inline-flex h-auto min-h-8 items-center py-1.5 text-left text-xs!" disabled={busy} on:click={() => save({ action: "arm_test" })}>{report.test.started_at ? $LL.app.operator.doctor.prepare_new_test() : $LL.app.operator.doctor.prepare_test()}</button>
                {#if testRoomHref}<a class="btn btn-sm" href={testRoomHref} target="_blank" rel="noreferrer">{$LL.app.operator.doctor.open_test_room()}</a>{/if}
              {/if}
            {/if}
          </div>
          {#if report.test.started_at}
            {#if !waitingForTalk && !awaitingPlayback && !testFailed}<p class="mt-3 text-sm" role="status">{$LL.app.operator.doctor.test.status({ stage: report.test.stage ?? $LL.app.operator.doctor.test.stage_unknown(), state: report.test.state })}</p>{/if}
            {#if report.test.job_id}<p class="mt-1 text-xs">{$LL.app.operator.doctor.test.job({ id: report.test.job_id })}</p>{/if}
            {#if report.test.published && report.test.viewer_url && !awaitingPlayback}
              <a class="btn btn-sm mt-3" href={report.test.viewer_url} target="_blank" rel="noreferrer">{$LL.app.operator.doctor.test.open_published()}</a>
              <button class="btn btn-sm mt-3" disabled={busy || !!report.test.playback_verified_at} on:click={() => save({ action: "confirm_playback", job_id: report?.test.job_id })}>{$LL.app.operator.doctor.test.confirm_published()}</button>
            {/if}
          {/if}
        {:else if panel === "settings"}
          <p class="text-sm">{$LL.app.operator.doctor.settings_note()}</p>
        {:else if panel === "repair_configuration"}
          <!-- Named, not a fall-through. This was the {:else}, so ANY action the
               panel had no branch for showed this — telling a reader to restore
               recording-setup.json from backup for a fault that had nothing to
               do with the file. rowActions now drops an action this build
               cannot name, so there is no longer an unnamed branch to land in. -->
          <h3 class="pr-10 font-semibold">{$LL.app.operator.doctor.repair_config.title()}</h3>
          <p class="my-2 text-sm">{$LL.app.operator.doctor.repair_config.body({ file: "recording-setup.json" })}</p>
        {/if}
      </div>
    {/if}
        </li>
      {/each}
    </ul>
</section>
{/if}
</div>
