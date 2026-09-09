<script lang="ts">
  // The model field, as a combobox rather than a text box with a "Load models"
  // button beside it.
  //
  // The button was the honest control while the list was expensive and might
  // fail, but it made "which models does this endpoint have?" a thing you had
  // to know to ask. Focus is the ask now: opening the field fetches the list
  // once per provider, and the parent decides how (this component never talks
  // to the operator — it is handed a list, a loading flag and an error).
  //
  // Free text is preserved, deliberately. The field is a model ID, the registry
  // of them belongs to the endpoint rather than to us, and an ID from a newer
  // image must still be typeable when the list is stale, empty or unreachable.
  // So the list narrows what you type; it never gates it.
  import { createEventDispatcher, onDestroy } from "svelte";
  import { createCombobox } from "@melt-ui/svelte";
  import { ChevronDown, TriangleAlert } from "@lucide/svelte";
  import type { LLMModel } from "./operator/types";

  export let value = "";
  export let models: LLMModel[] = [];
  export let loading = false;
  // Non-empty when the last listing failed. Shown under the field rather than
  // swallowed: a model list that could not be read is not an endpoint with no
  // models, and the difference decides whether typing an ID by hand is a
  // workaround or a mistake.
  export let error = "";
  export let disabled = false;
  export let placeholder = "endpoint default";
  export let id = "";
  export let label = "Model";

  const dispatch = createEventDispatcher<{ open: void }>();

  const {
    elements: { menu, input, option, label: labelEl },
    states: { open, inputValue, touchedInput },
    helpers: { isSelected },
  } = createCombobox<string>({
    // Rendered where it stands rather than portalled to <body>: this app mounts
    // into a shadow root inside Nextcloud's page, and a menu portalled out of
    // it loses the app's styles and lands over Nextcloud's own chrome.
    portal: null,
    forceVisible: true,
    positioning: { placement: "bottom-start", sameWidth: true },
    onSelectedChange: ({ next }) => {
      if (next) {
        value = next.value;
      }
      return next;
    },
  });

  // Asked once per opening, not once per keystroke: the parent caches by
  // provider, so a second open of the same endpoint is free and a re-open after
  // a failure is a retry.
  const stopOpen = open.subscribe((isOpen) => {
    if (isOpen) {
      dispatch("open");
    }
  });

  // Typing IS the value — that is what keeps the field free text. Guarded on
  // touchedInput so the programmatic sync below (which writes the same store)
  // cannot bounce back and overwrite the value it just rendered.
  const stopInput = inputValue.subscribe((text) => {
    if ($touchedInput) {
      value = text;
    }
  });

  onDestroy(() => {
    stopOpen();
    stopInput();
  });

  // The other direction: a value arriving from the parent — the saved settings
  // landing, or a provider change clearing the model — has to show in the box.
  // Never while the field is being typed in, or it would fight the cursor.
  $: if (!$touchedInput && $inputValue !== value) {
    inputValue.set(value);
  }

  // Narrowed by what has been typed, but only once something has: opening the
  // field on a saved model must show the whole list rather than the one row
  // that matches it exactly.
  $: visible = $touchedInput
    ? models.filter((model) => matches(model, $inputValue))
    : models;

  function matches(model: LLMModel, text: string): boolean {
    const needle = text.trim().toLowerCase();
    if (needle === "") {
      return true;
    }
    return (
      model.id.toLowerCase().includes(needle) ||
      (model.name ?? "").toLowerCase().includes(needle)
    );
  }
</script>

<!-- Spread + `use:$store.action` rather than melt's `use:melt={$store}` sugar.
     The sugar needs @melt-ui/pp in the Svelte preprocessor chain, and without
     it `use:melt` throws at RUNTIME while the build stays green — which is
     exactly how it reached a browser once. This is the same thing the
     preprocessor emits, and it costs no build-time dependency shared with the
     viewing layer. -->
<div class="model-field">
  <span class="model-label" {...$labelEl} use:$labelEl.action>{label}</span>
  <div class="model-input">
    <input
      {id}
      {disabled}
      {placeholder}
      class="input input-sm w-full border-base-300 pr-7 shadow-none"
      {...$input}
      use:$input.action
    />
    <ChevronDown size={14} class="model-chevron" aria-hidden="true" />
  </div>

  {#if $open}
    <ul class="model-menu" {...$menu} use:$menu.action>
      {#if loading}
        <li class="model-note">Loading models…</li>
      {:else if error}
        <!-- The list failed; the field has not. Saying which is what tells an
             administrator that typing the ID by hand is the way through. -->
        <li class="model-note model-note-warn">
          <TriangleAlert size={12} aria-hidden="true" />
          <span>{error} You can still type a model ID.</span>
        </li>
      {:else if models.length === 0}
        <li class="model-note">This endpoint listed no models. Type a model ID.</li>
      {:else if visible.length === 0}
        <li class="model-note">
          No model matches “{$inputValue}”. It is still accepted — the list is the
          endpoint's, not a limit on what you may send.
        </li>
      {:else}
        {#each visible as model (model.id)}
          {@const opt = $option({ value: model.id, label: model.id })}
          <li class="model-option" {...opt} use:opt.action>
            <span class="model-option-id">{model.id}</span>
            {#if model.name && model.name !== model.id}
              <span class="model-option-name">{model.name}</span>
            {/if}
            {#if $isSelected(model.id)}
              <span class="model-option-tick" aria-hidden="true">✓</span>
            {/if}
          </li>
        {/each}
        {#if visible.length !== models.length}
          <li class="model-note">{visible.length} of {models.length} models</li>
        {/if}
      {/if}
    </ul>
  {/if}
</div>

<style>
  /* Plain CSS: the menu is positioned by melt's floating action and needs a
     stacking context and a scroll cap of its own, neither of which reads well
     as a stack of utilities. */
  .model-field {
    display: flex;
    flex-direction: column;
    gap: 0.25rem;
    min-width: 0;
  }
  .model-label {
    font-size: 0.75rem;
    font-weight: 500;
    color: color-mix(in oklch, var(--color-base-content) 70%, transparent);
  }
  .model-input {
    position: relative;
    display: flex;
    min-width: 0;
  }
  .model-input :global(.model-chevron) {
    position: absolute;
    top: 50%;
    right: 8px;
    transform: translateY(-50%);
    pointer-events: none;
    color: color-mix(in oklch, var(--color-base-content) 50%, transparent);
  }

  .model-menu {
    z-index: 60;
    max-height: 16rem;
    overflow-y: auto;
    overscroll-behavior: contain;
    margin: 0;
    padding: 4px;
    list-style: none;
    background-color: var(--color-base-100);
    border: 1px solid var(--color-base-300);
    border-radius: var(--radius-box, 0.75rem);
    box-shadow: 0 8px 24px oklch(0% 0 0 / 0.18);
  }

  /* Stacked, not side by side: a model id is long, the menu is only as wide as
     the field, and a display name sharing the line broke ids mid-token
     ("anthropic/claude" / "-sonnet-4.5"). */
  .model-option {
    display: grid;
    grid-template-columns: minmax(0, 1fr) auto;
    align-items: baseline;
    gap: 0 0.5rem;
    padding: 6px 8px;
    cursor: pointer;
    border-radius: var(--radius-field, 0.5rem);
    font-size: 0.8125rem;
    color: var(--color-base-content);
  }
  /* melt marks the keyboard-highlighted row with data-highlighted, which is what
     makes arrow keys legible; hover and it must look the same. :global on the
     attribute half because it is set at runtime by the builder, so the compiler
     cannot see it and prunes the rule as unused. */
  .model-option:hover,
  .model-option:global([data-highlighted]) {
    background-color: var(--color-base-200);
  }
  .model-option:global([data-disabled]) {
    opacity: 0.5;
  }
  .model-option-id {
    min-width: 0;
    font-family: monospace;
    overflow-wrap: anywhere;
  }
  .model-option-name {
    grid-column: 1;
    font-size: 0.6875rem;
    color: color-mix(in oklch, var(--color-base-content) 55%, transparent);
  }
  .model-option-tick {
    grid-column: 2;
    grid-row: 1;
    color: var(--color-primary);
  }

  .model-note {
    display: flex;
    align-items: center;
    gap: 0.375rem;
    padding: 8px;
    font-size: 0.75rem;
    line-height: 1.45;
    color: color-mix(in oklch, var(--color-base-content) 60%, transparent);
  }
  .model-note-warn {
    color: var(--color-warning, #b45309);
  }
</style>
