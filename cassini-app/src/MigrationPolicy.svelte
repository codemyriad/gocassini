<script lang="ts">
  import { createEventDispatcher } from "svelte";
  import { conflictOptions, strategyOptions } from "./operator/storageWizard";
  import type {
    StorageConflictPolicy,
    StorageMigrationPolicy,
    StorageMigrationStrategy,
    StorageTransitionPreview,
  } from "./operator/types";

  // What happens to the recordings that are already there (D-708).
  //
  // This component renders ONLY when there is a choice to make, and its caller
  // is what enforces that — the spec's rule is that a question with one possible
  // answer is not a question, and asking anyway is how a confirmation dialog
  // stops being read. The two levels come apart:
  //
  //   recordings in both folders     the strategy matters: leave them, copy
  //                                  them, or replace what is there
  //   the SAME recording in both     and then the conflict rule matters too
  //
  // Both bits come from the operator's preview, computed under its own lock
  // against both roots. Nothing here decides whether to ask.

  export let preview: StorageTransitionPreview | null = null;
  export let policy: StorageMigrationPolicy;
  export let disabled = false;

  $: sourceRoot = preview?.source_root ?? "the other folder";
  $: destinationRoot = preview?.destination_root ?? "this folder";
  $: strategies = strategyOptions(sourceRoot, destinationRoot);
  $: conflicts = conflictOptions(sourceRoot, destinationRoot);
  // The conflict rule only means anything for a merge: switch-only copies
  // nothing, and overwrite has no conflicts by construction because the
  // destination's copy is being removed either way.
  $: showConflict = preview?.conflict_matters === true && policy.strategy === "merge";

  // The caller re-previews on this: the counts under these controls describe a
  // policy, and a set of numbers describing one the administrator has since
  // changed is worse than no numbers at all.
  const dispatch = createEventDispatcher<{ change: StorageMigrationPolicy }>();

  function chooseStrategy(value: StorageMigrationStrategy): void {
    policy = { ...policy, strategy: value };
    dispatch("change", policy);
  }

  function chooseConflict(value: StorageConflictPolicy): void {
    policy = { ...policy, on_conflict: value };
    dispatch("change", policy);
  }
</script>

<div class="grid gap-3 rounded-box border border-base-300 bg-base-100/60 p-3">
  <div class="grid gap-1">
    <p class="text-sm font-semibold">
      There are recordings in both folders. What should happen to them?
    </p>
    {#if preview}
      <p class="text-xs break-words text-base-content/70">
        <code class="break-all">{sourceRoot}</code> holds {preview.meetings}, and
        <code class="break-all">{destinationRoot}</code> holds {preview.destination_meetings}.
      </p>
    {/if}
  </div>

  <fieldset class="grid gap-2" {disabled}>
    <legend class="sr-only">What to do with the recordings that are already there</legend>
    {#each strategies as option (option.value)}
      <label class="flex cursor-pointer items-start gap-2 rounded-box border border-base-300 p-2 transition
        {policy.strategy === option.value ? 'border-primary bg-primary/10' : 'bg-base-200'}">
        <input
          class="radio radio-sm mt-0.5"
          type="radio"
          name="storage-migration-strategy"
          value={option.value}
          checked={policy.strategy === option.value}
          on:change={() => chooseStrategy(option.value)}
        />
        <span class="grid gap-0.5">
          <span class="text-sm font-medium">{option.label}</span>
          <span class="text-xs break-words text-base-content/70">{option.detail}</span>
        </span>
      </label>
    {/each}
  </fieldset>

  {#if showConflict}
    <div class="grid gap-2 border-t border-base-300 pt-3">
      <div class="grid gap-1">
        <p class="text-sm font-semibold">
          {preview?.conflicts}
          {preview?.conflicts === 1 ? "recording is" : "recordings are"} in both folders under the same
          name. Which copy wins?
        </p>
        {#if preview && preview.conflict_names.length > 0}
          <p class="text-xs break-all text-base-content/60">
            {preview.conflict_names.slice(0, 5).join(", ")}{preview.conflict_names.length > 5
              ? ", and more"
              : ""}
          </p>
        {/if}
      </div>
      <fieldset class="grid gap-2" {disabled}>
        <legend class="sr-only">Which copy of a duplicated recording wins</legend>
        {#each conflicts as option (option.value)}
          <label class="flex cursor-pointer items-start gap-2 rounded-box border border-base-300 p-2 transition
            {policy.on_conflict === option.value ? 'border-primary bg-primary/10' : 'bg-base-200'}">
            <input
              class="radio radio-sm mt-0.5"
              type="radio"
              name="storage-migration-conflict"
              value={option.value}
              checked={policy.on_conflict === option.value}
              on:change={() => chooseConflict(option.value)}
            />
            <span class="grid gap-0.5">
              <span class="text-sm font-medium">{option.label}</span>
              <span class="text-xs break-words text-base-content/70">{option.detail}</span>
            </span>
          </label>
        {/each}
      </fieldset>
    </div>
  {/if}
</div>
