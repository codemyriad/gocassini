<script lang="ts">
  // "Connect an agent" (D-813): the address of Cassini's MCP server and the
  // three steps that connect an agent to it, for every user — the person who
  // can read the meetings is the person who connects the agent, so this is not
  // an administrator's setting.
  //
  // The agent reads as the person whose app password it holds and nothing
  // more: the same meetings this list shows them, read-only. Saying so here is
  // the point — it is what someone needs to know before they hand an agent a
  // credential.
  import type { AgentConnection } from "../viewer/dataProvider";
  import CommandBlock from "./ui/CommandBlock.svelte";

  export let connection: AgentConnection;
  export let active = false;

  const PASSWORD_PLACEHOLDER = "<app password>";
  const DOCS_URL = "https://github.com/codemyriad/gocassini/blob/main/docs/agent-meeting-access.md";

  $: claudeCommand = `claude mcp add --transport http cassini ${connection.endpoint} --header "Authorization: Bearer ${PASSWORD_PLACEHOLDER}"`;
  $: genericConfig = JSON.stringify(
    {
      mcpServers: {
        cassini: {
          type: "http",
          url: connection.endpoint,
          headers: { Authorization: `Bearer ${PASSWORD_PLACEHOLDER}` },
        },
      },
    },
    null,
    2,
  );
</script>

<div class="agent-pane" class:agent-pane-hidden={!active} role="tabpanel" aria-label="Connect an agent">
  <div class="agent-column">
    <header class="pt-4 pb-1">
      <h2 class="text-[17px] font-semibold">Connect an agent</h2>
      <p class="text-sm text-base-content/65">
        Let an AI agent — Claude Code, or any client that speaks MCP — read your meetings. It sees exactly the
        meetings you see here, and it can only read them: it cannot mark, rename or delete anything.
      </p>
    </header>

    <section aria-labelledby="agent-address">
      <h3 id="agent-address" class="text-sm font-semibold">Server address</h3>
      <CommandBlock command={connection.endpoint} copyLabel="Copy address" />
    </section>

    <ol class="agent-steps">
      <li>
        <h3 class="text-sm font-semibold">Create an app password</h3>
        <p class="text-sm text-base-content/70">
          In Nextcloud, open
          {#if connection.appPasswordsUrl}
            <a class="link" href={connection.appPasswordsUrl} target="_blank" rel="noopener noreferrer">Personal settings → Security</a>,
          {:else}
            Personal settings → Security,
          {/if}
          and under <em>Devices &amp; sessions</em> create an app password named after the agent. You can revoke it there
          at any time, which disconnects the agent.
        </p>
      </li>
      <li>
        <h3 class="text-sm font-semibold">Add Cassini to your agent</h3>
        <p class="text-sm text-base-content/70">
          For Claude Code, run this with your app password in place of <code class="agent-code">{PASSWORD_PLACEHOLDER}</code>:
        </p>
        <CommandBlock command={claudeCommand} />
        <details class="mt-2">
          <summary class="text-sm text-base-content/70 cursor-pointer">Another client</summary>
          <p class="text-sm text-base-content/70 mt-1">
            Most clients take the address and a header like this. The app password is sent as a bearer token.
          </p>
          <CommandBlock command={genericConfig} copyLabel="Copy configuration" />
        </details>
      </li>
      <li>
        <h3 class="text-sm font-semibold">Ask it something</h3>
        <p class="text-sm text-base-content/70">
          For example: <em>“What was decided in yesterday's standup?”</em> The agent finds the meeting, reads it and
          cites the minute.
        </p>
      </li>
    </ol>

    <p class="text-sm text-base-content/65 pt-2">
      Prefer a command line? The <code class="agent-code">cassini meetings</code> commands read the same meetings with
      the same app password. Both are described in the
      <a class="link" href={DOCS_URL} target="_blank" rel="noopener noreferrer">agent access guide</a>.
    </p>
  </div>
</div>

<style>
  .agent-pane {
    grid-column: 1 / -1;
    grid-row: 2;
    min-height: 0;
    overflow-y: auto;
    overscroll-behavior: contain;
  }
  .agent-pane-hidden {
    display: none;
  }
  .agent-column {
    max-width: 680px;
    margin: 0 auto;
    padding: 0 16px 48px;
    display: flex;
    flex-direction: column;
    gap: 12px;
  }
  .agent-steps {
    display: flex;
    flex-direction: column;
    gap: 16px;
    padding-left: 1.25rem;
    list-style: decimal;
  }
  .agent-steps li::marker {
    font-weight: 600;
  }
  .agent-code {
    border-radius: 4px;
    background: var(--color-base-200);
    padding: 1px 4px;
    font-family: var(--font-mono, ui-monospace, monospace);
    font-size: 0.85em;
  }
</style>
