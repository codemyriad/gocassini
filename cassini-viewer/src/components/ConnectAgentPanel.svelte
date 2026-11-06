<script lang="ts">
  // "Connect an agent" (D-813): the address of Cassini's MCP server and the
  // three steps that connect an agent to it, for every user — the person who
  // can read the meetings is the person who connects the agent, so this is not
  // an administrator's setting.
  //
  // A side panel opened from the rooms rail's footer, not a tab: connecting an
  // agent is done once, and a tab beside Meetings and Import would sit there
  // for every visit after that. Positioned against the browse shell like
  // Prepare — see App.svelte.
  //
  // The agent reads as the person whose app password it holds and nothing
  // more: the same meetings this list shows them, read-only. Saying so here is
  // the point — it is what someone needs to know before they hand an agent a
  // credential.
  import { createEventDispatcher } from "svelte";
  import { ExternalLink } from "@lucide/svelte";
  import type { AgentConnection } from "../viewer/dataProvider";
  import CloseButton from "./ui/CloseButton.svelte";
  import CommandBlock from "./ui/CommandBlock.svelte";

  export let connection: AgentConnection;

  const dispatch = createEventDispatcher<{ close: void }>();

  const PASSWORD_PLACEHOLDER = "<app password>";
  // Other clients' configuration and the CLI live in the guide, so the panel
  // stays three steps long.
  const DOCS_URL =
    "https://github.com/codemyriad/gocassini/blob/main/docs/agent-meeting-access.md#7-connect-an-agent-over-mcp";

  $: claudeCommand = `claude mcp add --transport http cassini ${connection.endpoint} --header "Authorization: Bearer ${PASSWORD_PLACEHOLDER}"`;
</script>

<aside class="agent-panel" aria-labelledby="agent-panel-title">
  <header class="agent-head">
    <div class="min-w-0 flex-1">
      <h2 id="agent-panel-title" class="text-[15px] font-semibold">Connect an agent</h2>
      <p class="mt-0.5 text-[13px] leading-normal text-base-content/70">
        Claude Code, Codex or any MCP client can read the meetings you see here. It reads as you, and it can only read:
        it cannot mark, rename or delete anything.
      </p>
    </div>
    <CloseButton label="Close Connect an agent" on:click={() => dispatch("close")} />
  </header>

  <div class="agent-body">
    <section aria-labelledby="agent-address">
      <h3 id="agent-address" class="text-xs font-semibold text-base-content/80">Server address</h3>
      <CommandBlock command={connection.endpoint} copyLabel="Copy address" />
    </section>

    <ol class="agent-steps">
      <li>
        <p class="text-[13px] leading-normal text-base-content/75">
          <strong class="font-semibold text-base-content">Create an app password</strong> in
          {#if connection.appPasswordsUrl}
            <a class="link" href={connection.appPasswordsUrl} target="_blank" rel="noopener noreferrer">Personal settings › Security</a>.
          {:else}
            Personal settings › Security.
          {/if}
          Revoking it disconnects the agent.
        </p>
      </li>
      <li>
        <p class="text-[13px] leading-normal text-base-content/75">
          <strong class="font-semibold text-base-content">Add Cassini to your agent.</strong> For Claude Code, with your
          app password in place of <code class="agent-code">{PASSWORD_PLACEHOLDER}</code>:
        </p>
        <CommandBlock command={claudeCommand} />
      </li>
      <li>
        <p class="text-[13px] leading-normal text-base-content/75">
          <strong class="font-semibold text-base-content">Ask it something,</strong> like
          <em>“What was decided in yesterday's standup?”</em>
        </p>
      </li>
    </ol>

    <a class="agent-more link" href={DOCS_URL} target="_blank" rel="noopener noreferrer">
      Other agents and the command line
      <ExternalLink size={12} aria-hidden="true" />
    </a>
  </div>
</aside>

<style>
  .agent-panel {
    display: flex;
    flex-direction: column;
    height: 100%;
    min-height: 0;
  }
  .agent-head {
    flex: none;
    display: flex;
    align-items: flex-start;
    gap: 8px;
    padding: 14px 12px 4px 18px;
  }
  .agent-body {
    flex: 1;
    min-height: 0;
    overflow-y: auto;
    overscroll-behavior: contain;
    display: flex;
    flex-direction: column;
    gap: 14px;
    padding: 8px 18px 18px;
  }
  .agent-steps {
    display: flex;
    flex-direction: column;
    gap: 12px;
    padding-left: 1.25rem;
    list-style: decimal;
    font-size: 13px;
  }
  .agent-steps li::marker {
    font-weight: 700;
    color: color-mix(in oklch, var(--color-base-content) 70%, transparent);
  }
  .agent-code {
    border-radius: 4px;
    background: var(--color-base-200);
    padding: 1px 4px;
    font-family: var(--font-mono, ui-monospace, monospace);
    font-size: 0.85em;
  }
  .agent-more {
    display: inline-flex;
    align-items: center;
    gap: 5px;
    padding-top: 10px;
    border-top: 1px solid var(--color-base-300);
    font-size: 12.5px;
    font-weight: 600;
    text-decoration: none;
  }
</style>
