<script lang="ts">
  // The settings surface (D-696): everything an administrator tunes about the
  // pipeline, split out of the operator's run console. Hosts the STT panel
  // (moved from the operator surface) and the LLM panel. Gated exactly like
  // the operator surface: the shell only shows it to admins, and every call it
  // makes hits the ADMIN routes anyway.
  import { loadConfig } from "./operator/config";
  import { OperatorClient } from "./operator/client";
  import SettingsPanel from "./SettingsPanel.svelte";
  import LLMSettingsPanel from "./LLMSettingsPanel.svelte";

  let operatorClient: OperatorClient | null = null;
  let configError = "";
  try {
    const { operatorBasePath } = loadConfig();
    operatorClient = new OperatorClient(operatorBasePath);
  } catch (error) {
    configError = error instanceof Error ? error.message : String(error);
  }
</script>

<div class="mx-auto flex min-h-full w-full flex-col gap-4 px-4 pt-4 pb-10">
  {#if configError}
    <section class="alert alert-error text-sm">{configError}</section>
  {:else}
    <SettingsPanel {operatorClient} />
    <LLMSettingsPanel {operatorClient} />
  {/if}
</div>
