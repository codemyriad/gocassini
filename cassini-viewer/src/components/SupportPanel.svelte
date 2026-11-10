<script lang="ts">
  // Help, opened from the rooms rail's footer. The viewer owns the sheet's
  // chrome only: what can be downloaded here is a fact about the deployment
  // around this viewer (the Cassini shell knows it; a standalone export has
  // nothing to offer), so the body is the shell's, through the default slot.
  import { createEventDispatcher } from "svelte";
  import CloseButton from "./ui/CloseButton.svelte";

  const dispatch = createEventDispatcher<{ close: void }>();
</script>

<section class="support-panel" aria-labelledby="support-panel-title">
  <header class="support-head">
    <div class="min-w-0 flex-1">
      <h2 id="support-panel-title" class="text-[15px] font-semibold">A recording missing, or something not working?</h2>
      <p class="mt-0.5 text-[13px] leading-normal text-base-content/70">
        Download a support bundle and send it to whoever runs Cassini for you.
      </p>
    </div>
    <CloseButton label="Close Help" on:click={() => dispatch("close")} />
  </header>

  <div class="support-body">
    <slot />
  </div>
</section>

<style>
  .support-panel {
    display: flex;
    flex-direction: column;
    height: 100%;
    min-height: 0;
  }
  .support-head {
    flex: none;
    display: flex;
    align-items: flex-start;
    gap: 8px;
    padding: 14px 12px 4px 18px;
  }
  .support-body {
    flex: 1;
    min-height: 0;
    overflow-y: auto;
    overscroll-behavior: contain;
    padding: 8px 18px 18px;
  }
</style>
