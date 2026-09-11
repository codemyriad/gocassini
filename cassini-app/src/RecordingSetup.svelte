<script lang="ts">
  import { onMount } from "svelte";
  import type { OperatorClient } from "./operator/client";
  import { checkLabels, stateLabels, readinessTitle, handoffScript, readinessHealthKey, type RecordingReadiness, type RecordingSetupUpdate } from "./operator/readiness";
  import { onSetupChanged, notifySetupChanged } from "./operator/setupSignal";
  export let operatorClient: OperatorClient;
  let report: RecordingReadiness | null = null;
  let secret = "";
  let room = "";
  let busy = false;
  let error = "";
  let panel = "";
  let aio = true;
  let provisioningURL = "";
  let alive = true;
  let polling = false;
  let copied = false;

  async function load(check = false) {
    if (busy || polling) return;
    busy = true; error = "";
    try {
      const next = check ? await operatorClient.checkReadiness() : await operatorClient.getReadiness();
      if (!alive) return;
      const changed = readinessHealthKey(report) !== readinessHealthKey(next);
      report = next;
      if (changed) notifySetupChanged();
      if (!room) room = next.test_room_url;
    } catch (e) { if (alive) error = e instanceof Error ? e.message : String(e); }
    finally { busy = false; }
  }
  async function save(payload: RecordingSetupUpdate) {
    if (busy || polling) return;
    busy = true; error = "";
    try {
      const next = await operatorClient.updateRecordingSetup(payload);
      if (alive) { report = next; secret = ""; notifySetupChanged(); }
    } catch (e) { if (alive) error = e instanceof Error ? e.message : String(e); }
    finally { busy = false; }
  }
  async function action(name: string) {
    if (name === "recheck") { await load(true); return; }
    if (name === "setup_storage") { document.getElementById("recording-storage")?.scrollIntoView({ behavior: "smooth" }); return; }
    panel = name;
    if (name === "connect_talk") {
      // Derive from the current operator URL, preserving installations under a subdirectory.
      const base = new URL((await import("./operator/config")).loadConfig().operatorBasePath, window.location.href);
      provisioningURL = base.href.replace(/\/$/, "") + "/talk/provisioning";
    }
  }
  async function copyScript() {
    try { await navigator.clipboard.writeText(handoffScript(provisioningURL, aio)); copied = true; }
    catch { error = "Clipboard access failed. Select and copy the commands below."; }
  }
  onMount(() => {
    alive = true;
    void load(true);
    const unsubscribe = onSetupChanged(() => void load(true));
    const timer = window.setInterval(async () => {
      if (busy || polling || !report?.test.started_at || document.hidden) return;
      polling = true;
      try {
        const next = await operatorClient.getReadiness();
        if (alive) {
          const changed = readinessHealthKey(report) !== readinessHealthKey(next);
          report = next;
          if (changed) notifySetupChanged();
        }
      }
      catch { if (alive) error = "Could not refresh the test recording. Check the connection and try again."; }
      finally { polling = false; }
    }, 5000);
    return () => { alive = false; secret = ""; unsubscribe(); window.clearInterval(timer); };
  });
</script>

<section class="rounded-box border border-base-300 bg-base-100 p-5 shadow-sm" aria-labelledby="recording-readiness-title" aria-busy={busy}>
  <div class="flex flex-wrap items-center justify-between gap-3">
    <h2 id="recording-readiness-title" class="text-lg font-semibold">{report ? readinessTitle(report) : "Check recording setup"}</h2>
    <button class="btn btn-sm" disabled={busy || polling} on:click={() => load(true)}>{busy ? "Checking…" : "Check again"}</button>
  </div>
  <p class="mt-2 text-sm text-base-content/70">Check the connection, choose recording storage, then verify a short recording through Talk.</p>
  {#if error}<p role="alert" class="mt-3 text-error">{error}</p>{/if}
  {#if report}
    <ul class="mt-4 divide-y divide-base-300">
      {#each report.checks as check}
        <li class="flex flex-wrap items-start justify-between gap-3 py-3">
          <div class="min-w-0 flex-1">
            <p class="font-medium">{checkLabels[check.id] ?? check.id} <span class="ml-2 text-xs font-normal">{stateLabels[check.state]}</span></p>
            <p class="mt-1 text-sm text-base-content/70">{check.message}</p>
            {#if check.checked_at}<p class="mt-1 text-xs text-base-content/50">Checked {new Date(check.checked_at).toLocaleString()}</p>{/if}
          </div>
          {#if check.action}<button class="btn btn-sm btn-outline" disabled={busy} on:click={() => action(check.action ?? "")}>{check.action === "recheck" ? "Check again" : check.action === "test_recording" ? "Test a recording" : check.action === "connect_talk" ? "Connect Talk" : check.action === "setup_storage" ? "Set up storage" : "Configure"}</button>{/if}
        </li>
      {/each}
    </ul>
    <div class="mt-4 flex flex-wrap gap-2">
      <button class="btn btn-sm" on:click={() => panel = "configure_talk"}>Talk authentication</button>
      <button class="btn btn-sm" on:click={() => panel = "test_room"}>Test room</button>
      <button class="btn btn-sm" on:click={() => action("connect_talk")}>Connect Talk</button>
      <button class="btn btn-sm" on:click={() => panel = "test_recording"}>Test a recording</button>
    </div>
    {#if panel}
      <div class="mt-5 rounded-box bg-base-200 p-4">
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
          <details class="mt-3 text-sm"><summary>Where to find it</summary><p class="my-2">On AIO, run this on the Docker host:</p><pre class="overflow-auto text-xs">docker exec nextcloud-aio-talk printenv INTERNAL_SECRET</pre><p class="mt-2">For standalone HPB, ask its administrator for [clients] internalsecret in server.conf. If you use managed hosting, your provider may need to help.</p></details>
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
          <p class="my-2 text-sm">On AIO, enable the Talk component in the AIO management interface and start the containers. Then check Talk’s administration settings. For custom installations, your server administrator needs to deploy and connect HPB.</p>
          <a class="link" href="https://nextcloud-talk.readthedocs.io/en/stable/quick-install/" target="_blank" rel="noreferrer">Open Nextcloud’s HPB installation guide</a>
        {:else if panel === "connect_talk"}
          <h3 class="font-semibold">Use Cassini as Talk’s recording backend</h3>
          <p class="my-2 text-sm">This replaces the current recording backend. Cassini cannot change Talk’s settings directly. The commands below save a backup, request your Nextcloud administrator app password, and configure Talk. Run them with Bash on the server; jq and curl are required.</p>
          <label class="flex items-center gap-2 text-sm"><input type="checkbox" class="checkbox checkbox-sm" bind:checked={aio} />Nextcloud All-in-One</label>
          {#if aio}<p class="my-3 text-sm">Disable AIO’s Talk Recording component and set NEXTCLOUD_KEEP_DISABLED_APPS=true on its mastercontainer before applying the handoff. Keep Talk enabled. The commands check the effective settings; follow the persistence instructions, then repeat the test after restarting.</p>{:else}<p class="my-3 text-sm">Run from your Nextcloud installation directory. Adjust the occ invocation if your installation uses a different web-server user or container.</p>{/if}
          <pre class="my-3 overflow-auto rounded bg-base-100 p-3 text-xs">{handoffScript(provisioningURL, aio)}</pre>
          <button class="btn btn-sm" on:click={copyScript}>{copied ? "Copied" : "Copy commands"}</button>
          <a class="link ml-3 text-sm" href="https://github.com/codemyriad/gocassini/blob/main/docs/recording-readiness.md" target="_blank" rel="noreferrer">Persistence and rollback instructions</a>
        {:else if panel === "test_recording"}
          <h3 class="font-semibold">Verify a recording through Talk</h3>
          <ol class="my-3 list-inside list-decimal space-y-2 text-sm">
            <li>Choose a dedicated test room, then press Prepare test below.</li>
            <li>Open that room, start a call, and use Talk’s Start recording action.</li>
            <li>Speak for about 20 seconds, then stop recording in Talk.</li>
            <li>Wait for publishing, open the result, and confirm that you can play the audio and read the transcript.</li>
          </ol>
          <button class="btn btn-primary btn-sm" disabled={busy || !report.test_room_url} on:click={() => save({ action: "arm_test" })}>{report.test.started_at ? "Prepare a new test" : "Prepare test"}</button>
          {#if report.test_room_url}<a class="btn btn-sm ml-2" href={report.test_room_url} target="_blank" rel="noreferrer">Open test room</a>{/if}
          {#if !report.test_room_url}<button class="btn btn-sm ml-2" on:click={() => panel = "test_room"}>Choose test room</button>{/if}
          {#if report.test.started_at}
            <p class="mt-3 text-sm" role="status">{report.test.state === "waiting_for_talk" ? "Waiting for a recording started through Talk. If none arrives, check the handoff above." : `${report.test.stage ?? "Test"}: ${report.test.state}`}</p>
            {#if report.test.job_id}<p class="mt-1 text-xs">Recording {report.test.job_id}</p>{/if}
            {#if report.test.published && report.test.viewer_url}
              <a class="btn btn-sm mt-3" href={report.test.viewer_url} target="_blank" rel="noreferrer">Open published recording</a>
              <button class="btn btn-sm mt-3" disabled={busy || !!report.test.playback_verified_at} on:click={() => save({ action: "confirm_playback", job_id: report?.test.job_id })}>I played the audio and read the transcript</button>
            {/if}
          {/if}
        {:else if panel === "settings"}
          <p class="text-sm">Open Operator → Settings → Transcription to review the selected quality and device. CPU processing is supported; a GPU is optional.</p>
        {:else}
          <p class="text-sm">Ask your server administrator to check Cassini’s persistent volume and restore recording-setup.json from backup, then restart Cassini and check again.</p>
        {/if}
        <button class="btn btn-ghost btn-sm mt-4" on:click={() => { panel = ""; secret = ""; }}>Close</button>
      </div>
    {/if}
    {#if report.test.playback_verified_at}<p class="mt-4 text-sm">Last test playback confirmed {new Date(report.test.playback_verified_at).toLocaleString()}. Connection checks expire after five minutes; test history does not replace current checks.</p>{/if}
  {/if}
</section>
