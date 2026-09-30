<script lang="ts">
  export let retainVideo = false;
  export let disabled = false;
</script>
<section class="op-tint p-4 grid gap-2" aria-label="Video capture">
  <label class="flex items-center gap-3 font-semibold">
    <input class="toggle toggle-primary" type="checkbox" bind:checked={retainVideo} {disabled} />
    Capture video
  </label>
  <p class="text-sm">Record audio only by default. Turn this on to also keep camera video on the Cassini server. Published meeting files contain audio only. Applies to recordings started after you save.</p>
  <p class="text-sm">Storage retention determines how long source recordings remain on the server.</p>
</section>
