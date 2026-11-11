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
  import { LL } from "../i18n";
  import type { AgentConnection } from "../viewer/dataProvider";
  import CloseButton from "./ui/CloseButton.svelte";
  import CommandBlock from "./ui/CommandBlock.svelte";
  import { richText, slotArgs } from "./ui/richText";

  export let connection: AgentConnection;

  const dispatch = createEventDispatcher<{ close: void }>();

  $: text = $LL.viewer.browse.connect_agent;
  // Part of the command the reader copies, and named in the step above it.
  $: passwordPlaceholder = text.password_placeholder();
  // Other clients' configuration and the CLI live in the guide, so the panel
  // stays three steps long.
  const DOCS_URL =
    "https://github.com/codemyriad/gocassini/blob/main/docs/agent-meeting-access.md#7-connect-an-agent-over-mcp";

  $: claudeCommand = `claude mcp add --transport http cassini ${connection.endpoint} --header "Authorization: Bearer ${passwordPlaceholder}"`;
</script>

<aside class="agent-panel" aria-labelledby="agent-panel-title">
  <header class="agent-head">
    <div class="min-w-0 flex-1">
      <h2 id="agent-panel-title" class="text-[15px] font-semibold">{text.title()}</h2>
      <p class="mt-0.5 text-[13px] leading-normal text-base-content/70">
        {text.intro()}
      </p>
    </div>
    <CloseButton label={text.close()} on:click={() => dispatch("close")} />
  </header>

  <div class="agent-body">
    <section aria-labelledby="agent-address">
      <h3 id="agent-address" class="text-xs font-semibold text-base-content/80">{text.address_heading()}</h3>
      <CommandBlock command={connection.endpoint} copyLabel={text.copy_address()} />
    </section>

    <ol class="agent-steps">
      <li>
        <p class="text-[13px] leading-normal text-base-content/75">
          {#each richText(text.step_password(slotArgs(["title", "link"]))) as part}{#if part.slot === "title"}<strong
                class="font-semibold text-base-content">{text.step_password_title()}</strong
              >{:else if part.slot === "link"}{#if connection.appPasswordsUrl}<a
                  class="link"
                  href={connection.appPasswordsUrl}
                  target="_blank"
                  rel="noopener noreferrer">{text.step_password_link()}</a
                >{:else}{text.step_password_link()}{/if}{:else}{part.text}{/if}{/each}
        </p>
      </li>
      <li>
        <p class="text-[13px] leading-normal text-base-content/75">
          {#each richText(text.step_add(slotArgs(["title", "placeholder"]))) as part}{#if part.slot === "title"}<strong
                class="font-semibold text-base-content">{text.step_add_title()}</strong
              >{:else if part.slot === "placeholder"}<code class="agent-code">{passwordPlaceholder}</code
              >{:else}{part.text}{/if}{/each}
        </p>
        <CommandBlock command={claudeCommand} />
      </li>
      <li>
        <p class="text-[13px] leading-normal text-base-content/75">
          {#each richText(text.step_ask(slotArgs(["title", "example"]))) as part}{#if part.slot === "title"}<strong
                class="font-semibold text-base-content">{text.step_ask_title()}</strong
              >{:else if part.slot === "example"}<em>{text.step_ask_example()}</em>{:else}{part.text}{/if}{/each}
        </p>
      </li>
    </ol>

    <a class="agent-more link" href={DOCS_URL} target="_blank" rel="noopener noreferrer">
      {text.more()}
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
