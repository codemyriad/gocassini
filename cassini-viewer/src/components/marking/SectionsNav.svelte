<script lang="ts">
  import { createEventDispatcher } from "svelte";
  import { ChevronDown, ChevronUp } from "@lucide/svelte";

  // How many sections are tagged, and the arrows that go between them: in
  // the bar over the tag column on a wide screen, in the dock on a narrow one.
  export let count = 0;
  // The section the reader last went to or opened, once there is one: then
  // the count says where they are among them, as the search's does.
  export let current = -1;
  // Finger-sized arrows, where the screen is a phone's.
  export let large = false;

  const dispatch = createEventDispatcher<{ step: 1 | -1 }>();
</script>

<span class="tf-marks" role="status">
  {#if current >= 0 && current < count}
    <span class="tf-marks-at tabular-nums">{current + 1} of {count}</span>
    {count === 1 ? "tagged section" : "tagged sections"}
  {:else}
    {count}
    {count === 1 ? "tagged section" : "tagged sections"}
  {/if}
</span>
{#if count > 0}
  <span class="tf-marks-group" class:large>
    <button type="button" class="tf-marks-step" aria-label="Previous tagged section" title="Previous tagged section" on:click={() => dispatch("step", -1)}>
      <ChevronUp size={large ? 18 : 13} aria-hidden="true" />
    </button>
    <button type="button" class="tf-marks-step" aria-label="Next tagged section" title="Next tagged section" on:click={() => dispatch("step", 1)}>
      <ChevronDown size={large ? 18 : 13} aria-hidden="true" />
    </button>
  </span>
{/if}

<style>
  .tf-marks-group {
    display: inline-flex;
    flex: none;
    align-items: center;
    gap: 2px;
  }
  .tf-marks-step {
    display: inline-flex;
    align-items: center;
    justify-content: center;
    width: 22px;
    height: 22px;
    cursor: pointer;
    background: none;
    border: 1px solid var(--color-base-300);
    border-radius: 5px;
    color: color-mix(in oklch, var(--color-base-content) 65%, transparent);
  }
  .large {
    gap: 6px;
  }
  .large .tf-marks-step {
    width: 32px;
    height: 32px;
    border-radius: var(--radius-field, 0.5rem);
  }
  .tf-marks-step:hover {
    background-color: color-mix(in oklch, var(--color-base-content) 8%, transparent);
    color: var(--color-base-content);
  }
  /* A count, not a control: the arrows beside it are how to reach them. */
  .tf-marks {
    display: inline-flex;
    flex: none;
    align-items: center;
    gap: 6px;
    font-size: 12px;
    white-space: nowrap;
    color: color-mix(in oklch, var(--color-base-content) 70%, transparent);
  }
  /* Where the reader is, in the pill the search's count sits in. */
  .tf-marks-at {
    padding: 2px 6px;
    border-radius: 5px;
    background-color: color-mix(in oklch, var(--color-base-content) 9%, transparent);
    color: var(--color-base-content);
  }
</style>
