<script lang="ts">
  import { deploymentHandoffScript, providerRequest, type DeploymentEnvironment, type RepairPurpose } from './operator/deploymentGuidance';
  import type { RecordingReadiness } from './operator/readiness';
  export let environment:DeploymentEnvironment;
  export let purpose:RepairPurpose;
  export let report:RecordingReadiness;
  export let provisioningURL='';
  let copied='';
  let copyError='';
  let reviewed=false;
  $: script=deploymentHandoffScript(provisioningURL,environment);
  $: request=providerRequest(purpose,report);
  $: { environment; reviewed=false; copied=''; }
  async function copy(text:string,label:string){
    try {await navigator.clipboard.writeText(text);copied=label;copyError='';}
    catch {copyError='Select and copy the text below; clipboard access is unavailable.';}
  }
</script>

<div class="mt-3 space-y-4">
  <p class="text-sm"><strong>What Cassini knows:</strong> the checks above report available capabilities. The Nextcloud installation method and your server access have not been detected. Your choices below guide these instructions; they are not verified configuration.</p>
  <label class="form-control block text-sm">Who can change the server configuration?
    <select class="select select-bordered mt-1 w-full" bind:value={environment.access}>
      <option value="unknown">I’m not sure</option><option value="host">I can use a terminal on the Nextcloud host</option><option value="container">I only have a container or appliance console</option><option value="provider">A provider or another administrator manages it</option>
    </select>
  </label>
  {#if environment.access !== 'provider'}
    <label class="form-control block text-sm">How is Nextcloud installed?
      <select class="select select-bordered mt-1 w-full" bind:value={environment.installation}>
        <option value="unknown">I don’t know</option><option value="aio">Nextcloud All-in-One (AIO)</option><option value="compose">Docker Compose</option><option value="docker">Docker container</option><option value="host">Directly on the host / server archive</option><option value="snap">Nextcloud Snap</option><option value="other">Podman, Kubernetes, NAS package, or another method</option>
      </select>
    </label>
    <p class="text-xs text-base-content/70">AIO can use Compose or run on a NAS: select AIO if it manages Nextcloud. For other NAS installations, select the underlying method only if you know it. A server dashboard or the person who installed Nextcloud can help identify it.</p>
  {/if}

  {#if purpose === 'hpb'}
    <h4 class="font-semibold">What you need</h4>
    <p class="text-sm">A Talk high-performance backend with standalone signaling, media support, and internal-client authentication. A working Talk call alone does not establish that this is available.</p>
    {#if environment.installation === 'aio' && environment.access === 'host'}
      <p class="text-sm">Open the AIO management interface, enable Talk, and start its containers. Check the high-performance backend in Nextcloud’s Talk administration settings, then return here and check again.</p>
    {:else}
      <p class="text-sm">Ask the administrator of Talk’s signaling server whether these capabilities are available. If no signaling server exists, its deployment and connection to Nextcloud must be arranged first.</p>
    {/if}
    <a class="link text-sm" href="https://nextcloud-talk.readthedocs.io/en/stable/quick-install/" target="_blank" rel="noreferrer">Official Talk backend installation guide</a>
  {:else if purpose === 'secret'}
    <h4 class="font-semibold">Where to obtain the internal secret</h4>
    {#if environment.installation === 'aio' && environment.access === 'host'}
      <p class="text-sm">On the Docker host running AIO’s Talk container, run the command below. Paste the value into the Internal secret field above. The value is sensitive; do not include it in a support ticket.</p>
      <pre class="overflow-auto rounded bg-base-100 p-3 text-xs">docker exec nextcloud-aio-talk printenv INTERNAL_SECRET</pre>
    {:else}
      <p class="text-sm">Ask whoever manages Talk’s signaling server for its <code>[clients] internalsecret</code> in <code>server.conf</code>. It may be on a different host from Nextcloud or Cassini. Ask them to enter it directly in Setup if you cannot access that server.</p>
    {/if}
    <p class="text-sm">If no HPB is configured, there is no secret to retrieve yet. Resolve the High-performance backend check first. Once saved, use Test connection to verify authentication.</p>
  {:else}
    <h4 class="font-semibold">What needs changing</h4>
    <p class="text-sm">Talk must use Cassini as its recording backend. This replaces its current recorder. Cassini cannot apply this Nextcloud configuration change from this page; an administrator must review and apply it.</p>
    {#if environment.access === 'host' && ['aio','docker','compose','host','snap'].includes(environment.installation)}
      <p class="text-sm"><strong>Run on the Nextcloud host.</strong> If Cassini or its deployment engine runs elsewhere, do not run the commands there. Use an account authorized to change Nextcloud’s configuration.</p>
      {#if ['docker','compose','host'].includes(environment.installation)}
        {#if environment.installation !== 'host'}
          <label class="form-control block text-sm">{environment.installation === 'compose' ? 'Nextcloud Compose service name' : 'Nextcloud container name'}<input class="input input-bordered mt-1 w-full" bind:value={environment.target} placeholder="From your deployment configuration" /></label>
        {/if}
        <label class="form-control block text-sm">Nextcloud web-server user<input class="input input-bordered mt-1 w-full" bind:value={environment.user} placeholder="Confirm in your installation; often www-data" /></label>
        <label class="form-control block text-sm">Absolute path to Nextcloud’s occ file{environment.installation === 'host' ? '' : ' inside the container'}<input class="input input-bordered mt-1 w-full" bind:value={environment.occPath} placeholder="Confirm the path in this installation" /></label>
        <p class="text-xs text-base-content/70">Find these values in the deployment configuration or ask its administrator. Container names, users and paths differ between official, LinuxServer and NAS packages. No values are assumed.</p>
      {/if}
      {#if environment.installation === 'compose'}<p class="text-sm">Run from the directory containing the Compose file for Nextcloud, using the same project configuration used to deploy it.</p>{/if}
      {#if environment.installation === 'snap'}<p class="text-sm">This uses Snap’s <code>nextcloud.occ</code> wrapper. It only configures Talk; it does not provision an ExApp deployment engine or HPB. Cassini’s execution host may be separate.</p>{/if}
      {#if environment.installation === 'aio'}<p class="text-sm">Keep AIO’s Talk component enabled, disable Talk Recording, and configure <code>NEXTCLOUD_KEEP_DISABLED_APPS=true</code> on the mastercontainer. This also changes cleanup of other disabled optional apps. Follow the persistence guide before applying the handoff, then test again after restarting.</p>{/if}
      {#if script}
        <details><summary class="cursor-pointer text-sm font-medium">Advanced: review the host commands</summary>
          <p class="my-2 text-sm">Requires Bash, curl and jq on the host, plus Docker or sudo for the selected method. The script checks these tools and Nextcloud before changing settings. Missing tools should be installed by the host administrator using that system’s package manager.</p>
          <pre class="my-3 overflow-auto rounded bg-base-100 p-3 text-xs">{script}</pre>
          <label class="flex items-start gap-2 text-sm"><input type="checkbox" class="checkbox checkbox-sm" bind:checked={reviewed} />I have confirmed the target installation and intend to replace its recording backend.</label>
          <button class="btn btn-sm mt-3" disabled={!reviewed} on:click={()=>copy(script ?? '', 'commands')}>{copied === 'commands' ? 'Copied' : 'Copy reviewed commands'}</button>
        </details>
      {:else}<p class="text-sm">Confirm all installation values above to generate commands. You can use the administrator request below instead.</p>{/if}
    {:else}
      <p class="text-sm">Ask the server administrator to apply the change. A container console is not the host terminal these commands require. For an unknown or other installation, use its supported administration tools; Cassini does not have verified commands for it.</p>
    {/if}
    <a class="link text-sm" href="https://github.com/codemyriad/gocassini/blob/main/docs/recording-readiness.md" target="_blank" rel="noreferrer">Persistence and rollback instructions</a>
  {/if}

  <details open={environment.access === 'provider'}><summary class="cursor-pointer text-sm font-medium">Request help from the server administrator or provider</summary>
    <p class="my-2 text-sm">Review this request and add the public Nextcloud address if needed. It includes check states, but no room links, saved secrets, recording IDs or raw logs.</p>
    <pre class="whitespace-pre-wrap break-words rounded bg-base-100 p-3 text-xs">{request}</pre>
    <button class="btn btn-sm mt-3" on:click={()=>copy(request,'request')}>{copied === 'request' ? 'Copied' : 'Copy administrator request'}</button>
  </details>
  {#if copyError}<p role="status" class="text-sm">{copyError}</p>{/if}
</div>
