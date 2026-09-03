<script lang="ts">
  // Insight templates (D-723 ships the nav row, D-718 ships the registry).
  //
  // The row is here now because the nav's shape and its deep-link ids are the
  // thing D-723 exists to settle; adding a fourth row later would reopen both.
  // What it must not do is imply a registry that does not exist. There is no
  // endpoint behind this panel yet, so it lists nothing and says why, rather
  // than showing the design prototype's example templates as though they
  // shipped — a name in a list reads as a template you can already use.
  import { FileText } from "@lucide/svelte";
</script>

<section class="rounded-box border border-base-300 bg-base-100 shadow-sm">
  <header class="flex items-center justify-between gap-3 px-4 py-3">
    <div class="flex items-center gap-2">
      <div>
        <h2 class="font-semibold">Insight templates</h2>
        <p class="text-xs text-base-content/60">
          The question a template asks of a set of meetings, and the instruction it sends to the
          model.
        </p>
      </div>
    </div>
  </header>

  <div class="border-t border-base-300 p-4">
    <div
      class="grid content-start justify-items-center gap-2 rounded-box border border-base-300 bg-base-200 px-4 py-8 text-center"
    >
      <FileText size={20} class="text-base-content/40" aria-hidden="true" />
      <p class="text-sm font-medium">No templates to show yet.</p>
      <p class="max-w-md text-xs text-base-content/60">
        Cassini has no template registry behind this panel yet, so there is nothing to list — not
        even the ones that will ship with it. Authoring your own, and pulling them from shared
        repositories, comes after that.
      </p>
    </div>
  </div>
</section>
