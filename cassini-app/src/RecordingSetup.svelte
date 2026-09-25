<script lang="ts">
  import DeploymentGuidance from './DeploymentGuidance.svelte';
  import { initialEnvironment } from './operator/deploymentGuidance';
  import { createEventDispatcher, onMount } from "svelte";
  import type { OperatorClient } from "./operator/client";
  import { checkLabels, checkStateLabel, checkTone, formatAge, isReprobedOnCheck, readinessTitle, readinessHealthKey, readinessRows, repairLabels, reportTone, rowActions, toneClasses, type RecordingReadiness, type RecordingSetupUpdate } from "./operator/readiness";
  import { onSetupChanged, notifySetupChanged } from "./operator/setupSignal";
  export let operatorClient: OperatorClient;
  // Storage is configured in Publish pipeline, and the checks now live in their
  // own Doctor panel — so this action has to move the reader there. It used to
  // scrollIntoView an id that was on the same page; from here that id is not
  // mounted at all, and the button would silently do nothing.
  const dispatch = createEventDispatcher<{ openStorage: void }>();
  let report: RecordingReadiness | null = null;
  let secret = "";
  let room = "";
  let busy = false;
  let error = "";
  let stale = false;
  let panel = "";
  let panelOwner = "";
  $: rows = report ? readinessRows(report).map(check => stale ? { ...check, state: "not_verified" as const, message: "Could not refresh this check. Check again for its current status." } : check) : [];
  let environment = initialEnvironment();
  let provisioningURL = "";
  let alive = true;
  let polling = false;
  // True only while a re-probe is in flight, so a row can say it is being
  // checked. Distinct from `busy`, which is also set by a plain read and by
  // saving an edit — neither of which re-probes anything.
  let checking = false;
  let reportVersion = 0;

  async function load(check = false) {
    if (busy || polling) return;
    busy = true; checking = check; error = "";
    try {
      const next = check ? await operatorClient.checkReadiness() : await operatorClient.getReadiness();
      if (!alive) return;
      const changed = readinessHealthKey(report) !== readinessHealthKey(next);
      report = next; stale = false; error = "";
      if (changed) notifySetupChanged();
      if (!room) room = next.test_room_url;
    } catch (e) { if (alive) { stale = true; error = e instanceof Error ? e.message : String(e); } }
    finally { busy = false; checking = false; }
  }
  async function save(payload: RecordingSetupUpdate) {
    if (busy) return;
    // A background GET must neither swallow an edit nor overwrite its response.
    ++reportVersion;
    busy = true; error = "";
    try {
      const next = await operatorClient.updateRecordingSetup(payload);
      if (alive) { report = next; stale = false; error = ""; secret = ""; notifySetupChanged(); }
    } catch (e) { if (alive) error = e instanceof Error ? e.message : String(e); }
    finally { busy = false; }
  }
  // The operator performs the repair; this only asks it to start, and takes the
  // checklist it answers with. The work outlives the request, so the row reports
  // that it is running and the poll picks up how it went.
  async function repair(action: string) {
    if (!action || busy || polling) return;
    busy = true; error = "";
    try {
      const next = await operatorClient.repairReadiness(action);
      if (alive) { report = next; stale = false; error = ""; }
    } catch (e) { if (alive) error = e instanceof Error ? e.message : String(e); }
    finally { busy = false; }
  }
  async function action(name: string, owner: string) {
    if (name === "recheck") { await load(true); return; }
    if (name === "setup_storage") { dispatch("openStorage"); return; }
    const closing = panel === name && panelOwner === owner;
    panelOwner = owner;
    panel = closing ? "" : name;
    if (name === "connect_talk") {
      // Derive from the current operator URL, preserving installations under a subdirectory.
      const base = new URL((await import("./operator/config")).loadConfig().operatorBasePath, window.location.href);
      provisioningURL = base.href.replace(/\/$/, "") + "/talk/provisioning";
    }
  }
  onMount(() => {
    alive = true;
    // Read before re-probing. A POST /health/check runs the media doctor, the
    // Talk probe and the storage preflight before it answers, and the list was
    // hidden behind `{#if report}` for the whole of it — so the panel sat empty
    // for seconds and then every row appeared at once. The GET is a read of
    // findings the operator already holds (startup establishes them), so the
    // checklist is on screen immediately and the re-probe updates it in place.
    void (async () => {
      await load(false);
      if (alive) await load(true);
    })();
    const unsubscribe = onSetupChanged(() => void load(true));
    const timer = window.setInterval(async () => {
      if (busy || polling || !report || document.hidden) return;
      polling = true;
      const version = reportVersion;
      try {
        const next = await operatorClient.getReadiness();
        if (alive && version === reportVersion) {
          const changed = readinessHealthKey(report) !== readinessHealthKey(next);
          report = next; stale = false; error = "";
          if (changed) notifySetupChanged();
        }
      }
      catch { if (alive && version === reportVersion) { stale = true; error = "Could not refresh recording checks. Check the connection and try again."; } }
      finally { polling = false; }
    }, 5000);
    return () => { alive = false; secret = ""; unsubscribe(); window.clearInterval(timer); };
  });
</script>

<section class="rounded-box border border-base-300 bg-base-100 p-5 shadow-sm" aria-labelledby="recording-readiness-title" aria-busy={busy}>
  <div class="flex flex-wrap items-center justify-between gap-3">
    <h2 id="recording-readiness-title" class="text-lg font-semibold {report && !stale ? toneClasses[reportTone(report)] : ''}">{stale ? "Recording setup needs verification" : report ? readinessTitle(report) : "Check recording setup"}</h2>
    <button class="btn btn-sm" disabled={busy || polling} on:click={() => load(true)}>{busy ? "Checking…" : "Run all checks"}</button>
  </div>
  <p class="mt-2 text-sm text-base-content/70">Check the connection and recording storage, then verify a short recording through Talk.</p>
  {#if error}<p role="alert" class="mt-3 text-error">{error}</p>{/if}
  {#if report}
    <ul class="mt-4 divide-y divide-base-300">
      {#each rows as check, index}
        <li class="py-3" data-check-id={check.id}>
          <div class="flex flex-wrap items-start justify-between gap-3">
            <div class="min-w-0 flex-1">
              <p class="font-medium">{checkLabels[check.id] ?? check.id} <span class="ml-2 text-xs font-normal {toneClasses[checkTone(check)]}">{checkStateLabel(check)}</span>{#if checking && isReprobedOnCheck(check.id)}<span class="ml-2 inline-flex items-center gap-1 text-xs font-normal text-base-content/60"><span class="loading loading-spinner loading-xs" aria-hidden="true"></span>Checking…</span>{/if}</p>
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
              {#if check.checked_at}<p class="mt-1 text-xs text-base-content/50" title={new Date(check.checked_at).toLocaleString()}>{check.code === "test_playback" ? "Confirmed" : "Checked"} {formatAge(check.checked_at)}</p>{/if}
            </div>
            <div class="flex flex-wrap gap-2">
              {#if check.repair}
                <button class="btn btn-sm btn-primary" disabled={busy || polling}
                  on:click={() => repair(check.repair ?? "")}>{repairLabels[check.repair] ?? "Fix this"}</button>
              {/if}
              {#each rowActions(check) as item}
                <button class="btn btn-sm btn-outline" disabled={busy || polling} aria-expanded={item.action === "recheck" || item.action === "setup_storage" ? undefined : panel === item.action && panelOwner === check.id} on:click={() => action(item.action, check.id)}>{item.label}</button>
              {/each}
            </div>
          </div>
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
          <DeploymentGuidance purpose="secret" {report} bind:environment />
          <button class="btn btn-sm mt-3" disabled={busy} on:click={() => load(true)}>Test connection</button>
        {:else if panel === "test_room"}
          <h3 class="font-semibold">Choose a test room</h3>
          <p class="my-2 text-sm">Paste the browser link for a dedicated room on this Nextcloud instance. Cassini checks its configured Nextcloud backend using the room token, without joining or recording the call. Cassini saves this URL for future checks.</p>
          <form on:submit|preventDefault={() => save({ test_room_url: room.trim() })}>
            <label class="block">Talk room URL<input class="input input-bordered mt-1 block w-full" type="url" bind:value={room} placeholder="https://cloud.example.com/call/roomtoken" /></label>
            <button class="btn btn-primary btn-sm mt-3" disabled={busy || !room.trim()}>Save test room</button>
          </form>
        {:else if panel === "setup_hpb"}
          <h3 class="font-semibold">Enable Talk’s high-performance backend</h3>
          <DeploymentGuidance purpose="hpb" {report} bind:environment />
        {:else if panel === "connect_talk"}
          <h3 class="font-semibold">Use Cassini as Talk’s recording backend</h3>
          <DeploymentGuidance purpose="handoff" {report} {provisioningURL} bind:environment />
        {:else if panel === "test_recording"}
          <h3 class="font-semibold">Verify a recording through Talk</h3>
          <ol class="my-3 list-inside list-decimal space-y-2 text-sm">
            <li>Choose a dedicated test room, then press Prepare test below.</li>
            <li>Open that room, start a call, and use Talk’s Start recording action.</li>
            <li>Speak for about 20 seconds, then stop recording in Talk.</li>
            <li>Wait for publishing, open the result, and play the audio to confirm that you can hear it.</li>
          </ol>
          <p class="mb-3 text-sm text-base-content/70">If transcription is enabled, check the transcript afterward. A transcript is not required to confirm audio playback.</p>
          <button class="btn btn-primary btn-sm" disabled={busy || !report.test_room_url} on:click={() => save({ action: "arm_test" })}>{report.test.started_at ? "Prepare a new test" : "Prepare test"}</button>
          {#if report.test_room_url}<a class="btn btn-sm ml-2" href={report.test_room_url} target="_blank" rel="noreferrer">Open test room</a>{/if}
          {#if !report.test_room_url}<button class="btn btn-sm ml-2" on:click={() => action("test_room", "talk.discovery")}>Choose test room</button>{/if}
          {#if report.test.started_at}
            <p class="mt-3 text-sm" role="status">{report.test.state === "waiting_for_talk" ? "Waiting for a recording started through Talk. If none arrives, check the handoff above." : `${report.test.stage ?? "Test"}: ${report.test.state}`}</p>
            {#if report.test.job_id}<p class="mt-1 text-xs">Recording {report.test.job_id}</p>{/if}
            {#if report.test.published && report.test.viewer_url}
              <a class="btn btn-sm mt-3" href={report.test.viewer_url} target="_blank" rel="noreferrer">Open published recording</a>
              <button class="btn btn-sm mt-3" disabled={busy || !!report.test.playback_verified_at} on:click={() => save({ action: "confirm_playback", job_id: report?.test.job_id })}>I played the published audio</button>
            {/if}
          {/if}
        {:else if panel === "settings"}
          <p class="text-sm">Review optional transcription below in Publish pipeline. Recording and playback can work without a transcript. CPU transcription is supported; a GPU is optional.</p>
        {:else}
          <p class="text-sm">Ask your server administrator to check Cassini’s persistent volume and restore recording-setup.json from backup, then restart Cassini and check again.</p>
        {/if}
        <button class="btn btn-ghost btn-sm mt-4" on:click={() => { panel = ""; secret = ""; }}>Close</button>
      </div>
    {/if}
        </li>
      {/each}
    </ul>
    {#if report.test.playback_verified_at}<p class="mt-4 text-sm">Last test playback confirmed {new Date(report.test.playback_verified_at).toLocaleString()}. Outbound connection findings show when they were checked; past playback does not verify the current handoff.</p>{/if}
  {/if}
</section>
