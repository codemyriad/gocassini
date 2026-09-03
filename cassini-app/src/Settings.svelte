<script lang="ts">
  // The settings panels, inside the operator surface (D-723). #207 shipped these
  // as a third top-level surface; the design prototype puts them behind a left
  // nav inside Operator instead, and that is what won — so this file is now the
  // host for ONE panel at a time rather than a stack of all of them. The panels
  // themselves are untouched: this was an information-architecture change, not a
  // redesign of what they configure.
  //
  // Gated exactly like the rest of the operator surface: the shell only shows it
  // to admins, and every call it makes hits the ADMIN routes anyway. There is no
  // second notion of admin here.
  import { loadConfig } from "./operator/config";
  import { OperatorClient } from "./operator/client";
  import SettingsPanel from "./SettingsPanel.svelte";
  import LLMSettingsPanel from "./LLMSettingsPanel.svelte";
  import InsightTemplatesPanel from "./InsightTemplatesPanel.svelte";
  import type { OperatorPanel } from "./surfaceRouting";

  export let panel: OperatorPanel = "endpoints";

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
  {:else if panel === "endpoints"}
    <LLMSettingsPanel {operatorClient} />
  {:else if panel === "pipeline"}
    <SettingsPanel {operatorClient} />
  {:else if panel === "templates"}
    <InsightTemplatesPanel />
  {:else}
    <!-- Unreachable: the operator surface renders the run console itself and
         only mounts this host for a Settings panel. Saying so beats a blank
         page if that ever stops being true. -->
    <section class="alert alert-error text-sm">
      No settings panel is named "{panel}".
    </section>
  {/if}
</div>
