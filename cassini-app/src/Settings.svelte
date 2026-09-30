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
  import { createEventDispatcher } from "svelte";
  import { loadConfig } from "./operator/config";
  import { OperatorClient } from "./operator/client";
  import SettingsPanel from "./SettingsPanel.svelte";
  import RetentionPanel from "./RetentionPanel.svelte";
  import LLMSettingsPanel from "./LLMSettingsPanel.svelte";
  import InsightTemplatesPanel from "./InsightTemplatesPanel.svelte";
  import type { OperatorPanel } from "./surfaceRouting";

  export let panel: OperatorPanel = "endpoints";

  // A locked panel offers the way out of being locked, and the nav that can
  // take it belongs to the operator surface. Forwarded rather than handled
  // here: this host does not own which panel is showing, and a second way to
  // change it would be a second answer to the one the URL holds.
  const dispatch = createEventDispatcher<{ panel: OperatorPanel }>();

  let operatorClient: OperatorClient | null = null;
  let configError = "";
  try {
    const { operatorBasePath } = loadConfig();
    operatorClient = new OperatorClient(operatorBasePath);
  } catch (error) {
    configError = error instanceof Error ? error.message : String(error);
  }
</script>

<div class="op-settings">
  {#if configError}
    <section class="alert alert-error text-sm">{configError}</section>
  {:else if panel === "endpoints"}
    <LLMSettingsPanel {operatorClient} />
  {:else if panel === "pipeline"}
    <SettingsPanel
      {operatorClient}
      on:openProviders={() => dispatch("panel", "endpoints")}
      on:openTemplates={() => dispatch("panel", "templates")}
    />
  {:else if panel === "storage"}
    <RetentionPanel {operatorClient} />
  {:else if panel === "templates"}
    <InsightTemplatesPanel
      {operatorClient}
      on:openProviders={() => dispatch("panel", "endpoints")}
    />
  {:else}
    <!-- Unreachable: the operator surface renders the run console itself and
         only mounts this host for a Settings panel. Saying so beats a blank
         page if that ever stops being true. -->
    <section class="alert alert-error text-sm">
      No settings panel is named "{panel}".
    </section>
  {/if}
</div>

<style>
  .op-settings {
    --op-x: 20px;
    --op-inset: var(--color-base-200);
    --op-inset-border: color-mix(in oklch, var(--color-base-content) 16%, var(--color-base-200));
    --op-code-bg: var(--op-inset);
    --op-code-border: var(--op-inset-border);
    display: flex;
    flex-direction: column;
    gap: 16px;
    width: 100%;
    min-height: 100%;
    padding: 16px var(--op-x) 48px;
    color: var(--color-base-content);
  }
  @media (max-width: 720px) {
    .op-settings {
      --op-x: 16px;
      padding: 8px var(--op-x) 40px;
    }
  }

  :global(:where(.op-settings .op-panel-head)) {
    display: flex;
    flex-wrap: wrap;
    align-items: flex-start;
    gap: 16px;
  }
  :global(:where(.op-settings .op-panel-head > div:first-child)) {
    flex: 1;
    min-width: 0;
  }
  :global(:where(.op-settings .op-panel-title)) {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: 10px;
  }
  :global(:where(.op-settings .op-panel-head h1)) {
    margin: 0;
    font-size: 17px;
    font-weight: 650;
    line-height: 1.3;
    letter-spacing: -0.01em;
  }
  :global(:where(.op-settings .op-panel-head p)) {
    margin: 5px 0 0;
    font-size: 13px;
    line-height: 1.5;
    color: color-mix(in oklch, var(--color-base-content) 65%, transparent);
  }
  :global(:where(.op-settings .op-panel-actions)) {
    display: flex;
    flex: none;
    align-items: center;
    gap: 8px;
    margin-left: auto;
  }
  :global(:where(.op-settings .panel-badge)) {
    padding: 4px 7px;
    font-size: 10px;
    font-weight: 600;
    line-height: 1;
    letter-spacing: 0.07em;
    text-transform: uppercase;
    color: color-mix(in oklch, var(--color-base-content) 45%, transparent);
    border: 1px solid var(--color-base-300);
    border-radius: var(--radius-selector, 0.25rem);
  }

  :global(:where(.op-settings .op-btn)) {
    padding: 6px 12px;
    cursor: pointer;
    background-color: var(--color-base-content);
    border: 0;
    border-radius: var(--radius-field, 0.5rem);
    font-size: 13px;
    font-weight: 600;
    line-height: 1.4;
    color: var(--color-base-100);
  }
  :global(:where(.op-settings .op-btn:not(:disabled):hover)) {
    background-color: color-mix(in oklch, var(--color-base-content) 88%, var(--color-base-100));
  }
  :global(:where(.op-settings .op-btn:disabled)) {
    cursor: not-allowed;
    background-color: var(--color-base-300);
    color: color-mix(in oklch, var(--color-base-content) 45%, transparent);
  }
  :global(:where(.op-settings .link-btn)) {
    padding: 2px 0;
    cursor: pointer;
    background: none;
    border: 0;
    font-size: 12.5px;
    color: color-mix(in oklch, var(--color-base-content) 65%, transparent);
  }
  :global(:where(.op-settings .op-cancel)) {
    display: inline-flex;
    align-items: center;
    padding: 6px 0;
    line-height: 1.4;
  }
  :global(:where(.op-settings .link-btn:hover)) {
    color: var(--color-base-content);
  }
  :global(:where(.op-settings .link-btn.danger:hover)) {
    color: var(--color-error);
  }
  :global(:where(.op-settings .icon-btn)) {
    display: inline-flex;
    flex: none;
    align-items: center;
    justify-content: center;
    width: 32px;
    height: 32px;
    padding: 0;
    cursor: pointer;
    color: color-mix(in oklch, var(--color-base-content) 70%, transparent);
    background-color: color-mix(in oklch, var(--color-base-content) 4%, var(--color-base-200));
    border: 1px solid color-mix(in oklch, var(--color-base-content) 9%, var(--color-base-200));
    border-radius: var(--radius-field, 0.5rem);
  }
  :global(:where(.op-settings .icon-btn svg)) {
    width: 16px;
    height: 16px;
  }
  :global(:where(.op-settings .icon-btn:not(:disabled):hover)) {
    color: var(--color-base-content);
    background-color: color-mix(in oklch, var(--color-base-content) 8%, var(--color-base-200));
    border-color: color-mix(in oklch, var(--color-base-content) 16%, var(--color-base-200));
  }
  :global(:where(.op-settings .icon-btn:disabled)) {
    cursor: default;
    opacity: 0.5;
  }

  :global(:where(.op-settings .op-tint)) {
    background-color: color-mix(in oklch, var(--color-base-content) 4%, var(--color-base-200));
    border: 1px solid color-mix(in oklch, var(--color-base-content) 9%, var(--color-base-200));
    border-radius: var(--radius-box, 0.5rem);
  }

  :global(:where(.op-settings .set-card)) {
    border-top: 1px solid var(--color-base-300);
  }
  :global(:where(.op-settings .set-card.no-rule)) {
    border-top: 0;
  }
  :global(:where(.op-settings .set-row)) {
    display: flex;
    align-items: flex-start;
    gap: 14px;
    padding: 15px 0;
  }
  :global(:where(.op-settings .set-row + .set-row)) {
    border-top: 1px solid var(--color-base-300);
  }
  :global(:where(.op-settings .set-row.off)),
  :global(:where(.op-settings .off)) {
    opacity: 0.55;
  }
  :global(:where(.op-settings .set-row-main)) {
    flex: 1;
    min-width: 0;
  }
  :global(:where(.op-settings .set-row-name)) {
    display: flex;
    align-items: center;
    gap: 6px;
    margin: 0;
    font-size: 13.5px;
    font-weight: 600;
    line-height: 1.45;
  }
  :global(:where(.op-settings .op-card-title)) {
    font-size: 15px;
    font-weight: 650;
    line-height: 1.4;
  }
  :global(:where(.op-settings .set-row-sub)) {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: 4px 12px;
    margin: 2px 0 0;
    font-size: 12.5px;
    line-height: 1.5;
    color: color-mix(in oklch, var(--color-base-content) 65%, transparent);
  }
  :global(:where(.op-settings .set-row-meta)) {
    flex: none;
    font-family: var(--font-mono);
    font-size: 11.5px;
    font-weight: 500;
    line-height: 1.8;
    color: color-mix(in oklch, var(--color-base-content) 45%, transparent);
  }
  @media (max-width: 560px) {
    :global(:where(.op-settings .set-row.wraps)) {
      flex-wrap: wrap;
    }
    :global(:where(.op-settings .set-row.wraps .set-row-meta)) {
      flex-basis: 100%;
      margin-top: -8px;
    }
  }

  :global(:where(.op-settings .tpl-toggle)) {
    display: inline-flex;
    align-items: center;
    gap: 7px;
    margin: 3px 0 0;
    padding: 0;
    cursor: pointer;
    list-style: none;
    font-size: 12.5px;
    line-height: 1.5;
    color: color-mix(in oklch, var(--color-base-content) 65%, transparent);
  }
  :global(:where(.op-settings .tpl-toggle)::-webkit-details-marker) {
    display: none;
  }
  :global(:where(.op-settings .tpl-toggle:hover)) {
    color: var(--color-base-content);
  }
  :global(:where(.op-settings .tpl-chev)) {
    flex: none;
    width: 5px;
    height: 5px;
    border-right: 1.4px solid currentColor;
    border-bottom: 1.4px solid currentColor;
    transform: rotate(-45deg);
    transition: transform 0.12s ease;
  }
  :global(:where(.op-settings details[open] > .tpl-toggle .tpl-chev)) {
    transform: rotate(45deg);
  }
  :global(:where(.op-settings .tpl-def)) {
    max-width: 74ch;
    margin: 9px 0 0;
    padding-left: 11px;
    border-left: 2px solid var(--color-base-300);
    font-size: 12.5px;
    line-height: 1.6;
    color: color-mix(in oklch, var(--color-base-content) 65%, transparent);
  }

  :global(:where(.op-settings .op-field)) {
    display: flex;
    flex-direction: column;
    gap: 4px;
    min-width: 0;
  }
  :global(:where(.op-settings .op-field-label)),
  :global(:where(.op-settings .model-label)) {
    font-size: 12.5px;
    font-weight: 550;
    color: color-mix(in oklch, var(--color-base-content) 85%, transparent);
  }
  :global(:where(.op-settings .op-input)) {
    width: 100%;
    min-width: 0;
    height: auto;
    padding: 5px 8px;
    font-size: 13px;
    line-height: 1.5;
    color: var(--color-base-content);
    background-color: var(--op-inset);
    border: 1px solid var(--op-inset-border);
    border-radius: var(--radius-field, 0.5rem);
    box-shadow: none;
    outline: none;
  }
  :global(:where(.op-settings .model-input .op-input)) {
    padding-right: 28px;
  }
  :global(:where(.op-settings select.op-input)) {
    padding-right: 28px;
    cursor: pointer;
    appearance: none;
    -webkit-appearance: none;
    background-image: none;
  }
  :global(:where(.op-settings .op-select)) {
    position: relative;
    display: flex;
    min-width: 0;
  }
  :global(:where(.op-settings .op-select-chevron)) {
    position: absolute;
    top: 50%;
    right: 8px;
    transform: translateY(-50%);
    pointer-events: none;
    color: color-mix(in oklch, var(--color-base-content) 50%, transparent);
  }
  :global(:where(.op-settings .op-input)::placeholder) {
    color: color-mix(in oklch, var(--color-base-content) 45%, transparent);
  }
  :global(:where(.op-settings .op-input:focus)),
  :global(:where(.op-settings .op-input:focus-within)) {
    border-color: color-mix(in oklch, var(--color-base-content) 45%, transparent);
    box-shadow: 0 0 0 3px color-mix(in oklch, var(--color-base-content) 11%, var(--color-base-100));
    outline: none;
  }

  :global(:where(.op-settings .op-state)) {
    margin: 0;
    padding: 15px 0;
    font-size: 13px;
    color: color-mix(in oklch, var(--color-base-content) 65%, transparent);
  }
  :global(:where(.op-settings .op-empty)) {
    display: flex;
    flex-direction: column;
    align-items: center;
    justify-content: center;
    gap: 6px;
    padding: 40px 20px;
    text-align: center;
    border: 1px dashed var(--color-base-300);
    border-radius: var(--radius-box, 0.5rem);
    color: color-mix(in oklch, var(--color-base-content) 45%, transparent);
  }
  :global(:where(.op-settings .op-empty .t)) {
    margin: 0;
    font-size: 13px;
    font-weight: 600;
    color: var(--color-base-content);
  }
  :global(:where(.op-settings .err-box)) {
    padding: 10px 12px;
    font-size: 12.5px;
    font-weight: 600;
    color: var(--color-error);
    background-color: color-mix(in oklch, var(--color-error) 16%, var(--color-base-200));
    border: 1px solid var(--color-error);
    border-radius: var(--radius-box, 0.5rem);
  }

  @media (prefers-reduced-motion: reduce) {
    :global(:where(.op-settings .tpl-chev)) {
      transition: none;
    }
  }
</style>
