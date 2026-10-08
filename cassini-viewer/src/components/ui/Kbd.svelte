<script lang="ts">
  // A key named beside what it does. `sm` is the search field's; the default
  // fits in a small button, where it takes its colour from the button's ink.
  export let size: "xs" | "sm" = "xs";
</script>

<kbd class="kbd cassini-kbd" class:kbd-xs={size === "xs"} class:kbd-sm={size === "sm"}><slot /></kbd>

<style>
  .cassini-kbd {
    padding-inline: 5px;
    border-radius: 5px;
    font-size: 11px;
    line-height: 1.4;
  }
  .cassini-kbd.kbd-sm {
    display: inline-flex;
    align-items: center;
    justify-content: center;
    gap: 2px;
    box-sizing: border-box;
    height: 22px;
    min-height: 0;
    padding-block: 0;
    padding-inline: 6px;
    border-width: 1px;
    font-size: 12px;
    line-height: 1;
  }
  /* In a button, a tint of the button's own ink, so it sits as well on a
     coloured fill as on a plain one. */
  :global(.btn) .cassini-kbd {
    padding-inline: 4px;
    font-size: 10.5px;
    color: inherit;
    background-color: color-mix(in oklch, currentColor 14%, transparent);
    border-color: color-mix(in oklch, currentColor 28%, transparent);
    border-bottom-width: 1px;
    box-shadow: none;
  }
</style>
