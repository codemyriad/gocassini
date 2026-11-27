<script lang="ts">
  // Where this meeting's summary was sent (#532), from the catalog entry's
  // summarySentTo. The popover is one line: the sentence, then the reason.
  import { LL } from "../i18n";
  import { tick } from "svelte";
  import { Send } from "@lucide/svelte";

  import { popover } from "./tags/popover";
  import type { SummarySentTo } from "../viewer/catalog";

  export let sentTo: SummarySentTo | null | undefined = null;

  let open = false;
  // Describes the dialog, so a screen reader reads the sentence on opening.
  const noticeId = `ss-notice-${Math.random().toString(36).slice(2, 10)}`;
  let anchor: HTMLButtonElement;
  let panel: HTMLDivElement;

  $: host = sentTo?.host ?? "";
  $: reason = sentTo?.reason ?? "";
  $: if (!host) open = false;

  async function toggle() {
    open = !open;
    if (open) {
      await tick();
      panel?.focus({ preventScroll: true });
    }
  }
</script>

{#if host}
  <button
    bind:this={anchor}
    type="button"
    class="ss-chip"
    aria-haspopup="dialog"
    aria-expanded={open}
    on:click={toggle}
    data-testid="summary-sent-chip"
  >
    <Send size={12} aria-hidden="true" />
    <span class="truncate">{$LL.viewer.meeting.summary_sent.chip({ host })}</span>
  </button>

  {#if open}
    <div
      bind:this={panel}
      use:popover={{ anchor, close: () => (open = false) }}
      class="tag-popover ss-panel"
      role="dialog"
      aria-label={$LL.viewer.meeting.summary_sent.panel_label()}
      aria-describedby={noticeId}
      tabindex="-1"
      data-testid="summary-sent-popover"
    >
      <p id={noticeId} class="ss-notice">{$LL.viewer.meeting.summary_sent.notice({ host })}{#if reason}{" "}{reason}{/if}</p>
    </div>
  {/if}
{/if}

<style>
  /* The facts chip's shape (MeetingFacts .mf-chip), tinted with the theme's
     accent and outlined in it: the one chip here that is news. The tokens are
     redefined per theme, and by Nextcloud's theming when embedded. */
  .ss-chip {
    display: inline-flex;
    flex: 0 1 auto;
    align-items: center;
    gap: 5px;
    max-width: 100%;
    margin-right: 6px;
    min-width: 0;
    box-sizing: border-box;
    height: 24px;
    padding: 0 8px;
    border: 1px solid color-mix(in oklch, var(--color-primary) 60%, transparent);
    border-radius: 5px;
    background-color: color-mix(in oklch, var(--color-primary) 14%, transparent);
    font-size: 12px;
    font-weight: 550;
    line-height: 1;
    white-space: nowrap;
    color: var(--color-base-content);
    cursor: pointer;
    transition: background-color 150ms ease;
  }
  .ss-chip:hover,
  .ss-chip[aria-expanded="true"] {
    background-color: color-mix(in oklch, var(--color-primary) 24%, transparent);
  }
  .ss-chip:focus-visible {
    outline: 2px solid var(--color-primary);
    outline-offset: 2px;
  }

  .ss-panel {
    width: max-content;
    /* Wide enough that the sentence and a short reason stay on one line. */
    max-width: min(560px, calc(100vw - 16px));
    padding: 10px 12px;
    font-size: 13px;
    color: var(--color-base-content);
  }
  .ss-panel:focus {
    outline: none;
  }
  .ss-notice {
    margin: 0;
    overflow-wrap: anywhere;
  }
</style>
