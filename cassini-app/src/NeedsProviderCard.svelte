<script lang="ts">
  // The locked state, inside the operator's own settings.
  //
  // Distinct from NeedsSetupCard, which explains an unconfigured deployment to
  // whoever is looking at the archive — including the people who cannot fix it.
  // This one is only ever seen by an administrator, on a settings page, one
  // click from the panel that fixes it. So it is a title and a button and
  // nothing else: the explanation would be telling someone what they already
  // came here to do.
  import { createEventDispatcher } from "svelte";
  import { KeyRound } from "@lucide/svelte";

  export let title: string;
  // The mock says "Add OpenRouter key" because the prototype presumes
  // OpenRouter. Cassini takes any OpenAI-compatible endpoint, and this is the
  // same words as the button on the panel it opens.
  export let action = "Add a provider";

  const dispatch = createEventDispatcher<{ open: void }>();
</script>

<div class="needs-key" role="status">
  <KeyRound size={20} class="needs-key-icon" aria-hidden="true" />
  <strong class="needs-key-title">{title}</strong>
  <button type="button" class="needs-key-go" on:click={() => dispatch("open")}>
    {action}
  </button>
</div>

<style>
  /* The app's own colour, the one an insight wears: this card stands in for
     the insight the panel cannot offer yet, rather than warning about one. The
     button inside is ink — the card is the colour, so the action need not be. */
  /* One row: the title wraps before the action moves, so the card reads as a
     sentence with its button rather than two stacked things. */
  .needs-key {
    display: flex;
    flex-wrap: nowrap;
    align-items: center;
    gap: 12px;
    padding: 14px;
    background-color: color-mix(in srgb, var(--color-primary) 9%, var(--color-base-100));
    border-radius: var(--radius-box, 0.75rem);
  }
  .needs-key :global(.needs-key-icon) {
    flex: none;
    width: 22px;
    height: 22px;
    color: var(--color-primary);
  }
  .needs-key-title {
    flex: 1 1 auto;
    min-width: 0;
    text-wrap: pretty;
    font-size: 13.5px;
    font-weight: 650;
    color: var(--color-base-content);
  }
  .needs-key-go {
    flex: none;
    margin-left: auto;
    padding: 7px 12px;
    cursor: pointer;
    background-color: var(--color-base-content);
    border: 0;
    border-radius: var(--radius-field, 0.5rem);
    font-size: 12.5px;
    font-weight: 600;
    line-height: 1.2;
    color: var(--color-base-100);
  }
  .needs-key-go:hover {
    background-color: color-mix(in oklch, var(--color-base-content) 88%, var(--color-base-100));
  }
</style>
