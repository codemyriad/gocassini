<script lang="ts">
  // A shell around the viewer for its not-found slot's browser test: it
  // fills `meeting-not-found` for the ids it is given, as cassini-app does.
  import App from "../App.svelte";
  import type { DataProvider } from "../viewer/dataProvider";

  export let dataProvider: DataProvider;
  export let explained: string[] = [];
</script>

<App {dataProvider} notFoundFilledFor={explained}>
  <svelte:fragment slot="meeting-not-found" let:meetingId let:refresh>
    <p data-testid="shell-not-found">The shell explains {meetingId}</p>
    <button type="button" on:click={() => void refresh()}>Accept</button>
  </svelte:fragment>
</App>
