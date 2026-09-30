<script lang="ts">
  import { onMount } from "svelte";
  import App from "./App.svelte";
  import DoctorPreview from "./preview/DoctorPreview.svelte";

  export let ncMode = false;
  // Opt-in review builds only. Normal builds eliminate the gallery and fixtures.
  const previewsEnabled = import.meta.env.VITE_CASSINI_DOCTOR_PREVIEWS === "true";
  let hash = window.location.hash;
  $: scenario = new URLSearchParams(hash.replace(/^#/, "")).get("doctor-preview");
  onMount(() => {
    const changed = () => { hash = window.location.hash; };
    window.addEventListener("hashchange", changed);
    window.addEventListener("popstate", changed);
    return () => {
      window.removeEventListener("hashchange", changed);
      window.removeEventListener("popstate", changed);
    };
  });
</script>

{#if previewsEnabled && scenario !== null}
  {#key scenario}
    <DoctorPreview {scenario} />
  {/key}
{:else}
  <App {ncMode} />
{/if}
