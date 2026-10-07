<script lang="ts">
  export let retainVideo = false;
  export let disabled = false;
  export let deleteSource = false;
</script>
<section class="op-tint p-4 grid gap-2" aria-label="Video capture">
  <label class="flex items-center gap-3 font-semibold">
    <input class="toggle toggle-primary" type="checkbox" bind:checked={retainVideo} {disabled} />
    Capture video
  </label>
  <p class="text-sm">Record audio only by default. Turn this on to also keep camera video on the Cassini server. Published meeting files contain audio only. Applies to recordings started after you save.</p>
  {#if deleteSource}
    <p class="text-sm">Source deletion uses audio-only capture. Media is deleted when processing finishes or fails.</p>
  {:else}
    <p class="text-sm">Storage retention determines how long source recordings remain on the server.</p>
  {/if}
</section>
