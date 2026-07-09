<script lang="ts">
  import ViewerApp from "cassini-viewer/App.svelte";
  import { StaticCatalogProvider } from "cassini-viewer/dataProvider";

  // The Cassini in-Nextcloud shell (D-420 V2). Today it hosts a single "browse"
  // surface — cassini-viewer's App (MeetingList + MeetingView), fed through the
  // DataProvider seam (the viewer used as a component in the main app). V3 adds
  // a top nav and the admin-gated operator surface (Operator.svelte) alongside
  // it; the operator JSON API stays ADMIN in info.xml (the real boundary), so
  // the shell only decides whether to *show* the operator (403-probe).
  export let ncMode: boolean = false;

  const dataProvider = new StaticCatalogProvider();
</script>

<ViewerApp {ncMode} {dataProvider} />
