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
  /* Secondary is this theme's amber — the colour the prototype gives everything
     model-written, and the colour it gives this card. Unlike primary it is not
     remapped to the Nextcloud accent in the embedded build, so a locked panel
     looks the same whatever the instance is themed. The button inside is ink:
     the card is the colour, so the action does not have to be. */
  .needs-key {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: 13px;
    padding: 11px 14px;
    background-color: color-mix(in srgb, var(--color-secondary) 9%, var(--color-base-100));
    border-radius: var(--radius-box, 0.75rem);
  }
  .needs-key :global(.needs-key-icon) {
    flex: none;
    width: 22px;
    height: 22px;
    color: var(--color-secondary);
  }
  .needs-key-title {
    flex: 1 1 auto;
    min-width: 0;
    font-size: 13.5px;
    font-weight: 650;
    color: var(--color-base-content);
  }
  .needs-key-go {
    flex: none;
    margin-left: auto;
    padding: 10px 18px;
    cursor: pointer;
    background-color: var(--color-base-content);
    border: 0;
    border-radius: var(--radius-field, 0.5rem);
    font-size: 13.5px;
    font-weight: 600;
    line-height: 1;
    color: var(--color-base-100);
  }
  .needs-key-go:hover {
    background-color: color-mix(in oklch, var(--color-base-content) 88%, var(--color-base-100));
  }
</style>
