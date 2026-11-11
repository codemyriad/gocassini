<script lang="ts">
  import { onDestroy } from "svelte";
  import { Check, Copy } from "@lucide/svelte";

  import { LL } from "../../i18n";

  export let command: string;
  // What the copy button says it copies, for a block that is not a command.
  // Empty means "Copy command", in the reader's language.
  export let copyLabel = "";
  $: copyText = copyLabel || $LL.viewer.browse.ui.copy_command();

  let copied = false;
  let copyFailed = false;
  let code: HTMLElement | null = null;
  let reset: ReturnType<typeof setTimeout> | undefined;

  function selectCommand(): boolean {
    const selection = window.getSelection();
    if (!code || !selection) return false;
    const range = document.createRange();
    range.selectNodeContents(code);
    selection.removeAllRanges();
    selection.addRange(range);
    return true;
  }

  function confirmCopied(): void {
    copied = true;
    clearTimeout(reset);
    reset = setTimeout(() => (copied = false), 2000);
  }

  async function copy(): Promise<void> {
    copyFailed = false;
    try {
      await navigator.clipboard.writeText(command);
      confirmCopied();
      return;
    } catch {
      copied = false;
    }
    try {
      if (selectCommand() && document.execCommand("copy")) {
        confirmCopied();
        return;
      }
    } catch {
      copied = false;
    }
    copyFailed = true;
    selectCommand();
  }

  onDestroy(() => clearTimeout(reset));
</script>

<div class="relative mt-2 rounded-field border border-base-300 bg-base-200">
  <pre class="m-0 py-2 pr-10 pl-3 font-mono text-xs leading-relaxed whitespace-pre-wrap text-base-content [overflow-wrap:anywhere]"><code bind:this={code}>{command}</code></pre>
  <button
    type="button"
    class="btn btn-ghost btn-xs btn-square absolute top-1.5 right-1.5"
    aria-label={copied ? $LL.common.status.copied() : copyText}
    title={copied ? $LL.common.status.copied() : copyText}
    on:click={copy}
  >
    {#if copied}<Check size={14} aria-hidden="true" />{:else}<Copy size={14} aria-hidden="true" />{/if}
  </button>
</div>
{#if copyFailed}<p class="mt-1 text-xs text-base-content/65" role="status">{$LL.viewer.browse.ui.copy_blocked()}</p>{/if}
