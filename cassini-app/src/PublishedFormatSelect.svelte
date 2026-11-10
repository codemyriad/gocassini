<script lang="ts">
  // What participants receive, as a listbox rather than a native select: each
  // option carries a line saying what it is, and the option that cannot be
  // chosen says why in its place instead of silently greying out.
  import { createEventDispatcher } from "svelte";
  import { writable } from "svelte/store";
  import { createSelect } from "@melt-ui/svelte";
  import { ChevronDown } from "@lucide/svelte";
  import { LL } from "cassini-viewer/i18n";

  type Format = "opus" | "json";
  export let value: Format = "opus";
  // Whether recordings are transcribed. Without a transcript there is no
  // transcript-only meeting to publish.
  export let transcript = true;
  export let disabled = false;

  const dispatch = createEventDispatcher<{ change: Format }>();
  $: t = $LL.app.settings.published_format;
  $: LABELS = { opus: t.opus_label(), json: t.json_label() } satisfies Record<Format, string>;

  $: options = [
    {
      value: "opus" as const,
      label: LABELS.opus,
      description: transcript ? t.opus_description() : t.opus_description_no_transcript(),
      disabled: false,
    },
    {
      value: "json" as const,
      label: LABELS.json,
      description: transcript ? t.json_description() : t.json_needs_transcript(),
      disabled: !transcript,
    },
  ];

  const selected = writable<{ value: Format; label: string }>({ value, label: "" });
  const {
    elements: { trigger, menu, option, label },
    states: { open },
    helpers: { isSelected },
  } = createSelect<Format>({
    // Rendered where it stands rather than portalled to <body>: this app mounts
    // into a shadow root inside Nextcloud's page, and a menu portalled out of
    // it loses the app's styles and lands over Nextcloud's own chrome.
    portal: null,
    forceVisible: true,
    positioning: { placement: "bottom-start" },
    selected,
    onSelectedChange: ({ next }) => {
      if (next && next.value !== value) dispatch("change", next.value);
      return next;
    },
  });
  // The value arriving from the parent (the saved settings landing, or the
  // fallback to audio when transcription is turned off) shows in the button.
  $: selected.set({ value, label: LABELS[value] });
</script>

<!-- Spread + `use:$store.action` rather than melt's `use:melt={$store}` sugar;
     see ModelCombobox for why. -->
<div class="fmt-field">
  <span class="fmt-label" {...$label} use:$label.action>{t.field_label()}</span>
  <button type="button" class="op-input fmt-trigger" {disabled} {...$trigger} use:$trigger.action>
    <span>{LABELS[value]}</span>
    <ChevronDown size={14} class="fmt-chevron" aria-hidden="true" />
  </button>
  {#if $open}
    <ul class="fmt-menu" {...$menu} use:$menu.action>
      {#each options as item (item.value)}
        {@const opt = $option({ value: item.value, label: item.label, disabled: item.disabled })}
        <li class="fmt-option" {...opt} use:opt.action>
          <span class="fmt-option-title">{item.label}{#if $isSelected(item.value)}<span class="fmt-tick" aria-hidden="true">✓</span>{/if}</span>
          <span class="fmt-option-desc" class:fmt-option-why={item.disabled}>{item.description}</span>
        </li>
      {/each}
    </ul>
  {/if}
</div>

<style>
  .fmt-field {
    position: relative;
    display: grid;
    min-width: 0;
    max-width: 100%;
  }
  .fmt-label {
    position: absolute;
    width: 1px;
    height: 1px;
    overflow: hidden;
    clip: rect(0 0 0 0);
    white-space: nowrap;
  }
  .fmt-trigger {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 10px;
    width: auto;
    min-width: 17rem;
    max-width: 100%;
    cursor: pointer;
    text-align: left;
  }
  .fmt-trigger:disabled {
    cursor: default;
    opacity: 0.6;
  }
  .fmt-trigger :global(.fmt-chevron) {
    flex: none;
    color: color-mix(in oklch, var(--color-base-content) 50%, transparent);
  }
  .fmt-menu {
    z-index: 60;
    min-width: 17rem;
    max-width: min(28rem, 90vw);
    margin: 0;
    padding: 4px;
    list-style: none;
    background-color: var(--color-base-100);
    border: 1px solid var(--color-base-300);
    border-radius: var(--radius-box, 0.5rem);
    box-shadow: 0 8px 24px oklch(0% 0 0 / 0.18);
  }
  .fmt-option {
    display: grid;
    gap: 1px;
    padding: 7px 10px;
    font-size: 13px;
    border-radius: var(--radius-field, 0.375rem);
    cursor: pointer;
  }
  .fmt-option[data-highlighted] {
    background-color: var(--color-base-200);
  }
  .fmt-option[data-selected] .fmt-option-title {
    font-weight: 600;
  }
  .fmt-option[data-disabled] {
    cursor: default;
  }
  .fmt-option[data-disabled] .fmt-option-title {
    color: color-mix(in oklch, var(--color-base-content) 50%, transparent);
  }
  .fmt-tick {
    margin-left: 6px;
    color: var(--color-primary);
  }
  .fmt-option-desc {
    font-size: 12px;
    color: color-mix(in oklch, var(--color-base-content) 65%, transparent);
  }
  .fmt-option-why {
    color: var(--color-warning);
  }
</style>
