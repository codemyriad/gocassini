<script lang="ts">
  import DetailedStorageUsageReport from "./DetailedStorageUsageReport.svelte";
  import type { OperatorClient } from "./operator/client";

  // The detailed index is the single storage view. The aggregate endpoint
  // remains available for request benchmarks, but is intentionally not part of
  // the operator UI.
  export let operatorClient: OperatorClient | null = null;
</script>

<DetailedStorageUsageReport {operatorClient} />
