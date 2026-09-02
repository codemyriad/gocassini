<script lang="ts">
  import { onMount } from "svelte";
  import { TriangleAlert } from "@lucide/svelte";
  import { loadConfig } from "./operator/config";
  import { OperatorClient } from "./operator/client";
  import StoragePanel from "./StoragePanel.svelte";

  // The Setup surface (D-616 first pass): instance-level configuration, as
  // opposed to the operator surface's runs and the browse surface's meetings.
  //
  // It holds one panel today. It exists as its own surface anyway, because the
  // thing it holds moves every recording in the instance and does not belong
  // beside a run list — and because the next few settings of this kind (who may
  // record, retention, the disclosure notice) are the same sort of decision,
  // not more run controls.

  let operatorClient: OperatorClient | null = null;
  let configError = "";

  onMount(() => {
    try {
      operatorClient = new OperatorClient(loadConfig().operatorBasePath);
    } catch (error) {
      configError = error instanceof Error ? error.message : String(error);
    }
  });
</script>

<div class="flex min-h-full flex-col bg-base-200 text-base-content">
  <div class="mx-auto flex min-h-full w-full max-w-5xl flex-col gap-4 px-4 pt-4 pb-10">
    {#if configError}
      <section class="alert alert-error">
        <TriangleAlert size={16} aria-hidden="true" />
        <span>{configError}</span>
      </section>
    {:else if operatorClient}
      {#key operatorClient}
        <StoragePanel {operatorClient} />
      {/key}
    {/if}
  </div>
</div>
