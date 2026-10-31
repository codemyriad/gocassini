<script lang="ts">
  // Marks a topic suggestions surface as a preview (docs/topic-suggestions.md).
  // The word carries the meaning, so it is not a colour alone and a screen
  // reader says it too. `xs` fits a rail row; the default sits by a heading.
  export let size: "xs" | "sm" = "sm";
</script>

<span class="preview-badge badge shrink-0 font-medium" class:badge-xs={size === "xs"} class:badge-sm={size === "sm"}>Preview</span>

<style>
  /* Ink in the text colour on a primary tint: readable in both themes, where
     the themes' info and warning inks are too pale for small text. */
  .preview-badge {
    color: var(--color-base-content);
    background-color: color-mix(in oklch, var(--color-primary) 16%, transparent);
    border-color: color-mix(in oklch, var(--color-primary) 55%, transparent);
  }
</style>
