<script lang="ts">
  import { createEventDispatcher, onDestroy, tick } from "svelte";
  import { fade } from "svelte/transition";
  import { cubicOut } from "svelte/easing";
  import { Ellipsis, History, X } from "@lucide/svelte";

  import { plural, type TagPick, type TagUpdate, type VocabularyTag } from "../../viewer/annotations";
  import { changedLine, confirmLine, countsLine, createJobTracker, jobStatus, type TagAction, type TagProvider } from "../../viewer/tagManager";
  import { colorFor } from "../../viewer/tagPalette";
  import TagChip from "./TagChip.svelte";
  import TagPicker from "./TagPicker.svelte";
  import TagEditor from "./manager/TagEditor.svelte";
  import { popover, stepIndex } from "./popover";

  export let tags: VocabularyTag[] = [];
  export let provider: TagProvider;
  export let open = false;

  const MENU = [["rename", "Rename"], ["color", "Change colour"], ["merge", "Merge into…"], ["delete", "Delete…"]] as const;
  const dispatch = createEventDispatcher<{ close: void; changed: void }>();
  const jobs = createJobTracker(provider, () => dispatch("changed"));
  onDestroy(jobs.stop);

  let sheet: HTMLElement;
  let menuEl: HTMLElement;
  let menu: { tag: VocabularyTag; anchor: HTMLElement } | null = null;
  let merging: typeof menu = null;
  // The tag whose last change is being read, and the icon it hangs off. One at
  // a time: it is a footnote, not a column.
  let history: { tag: VocabularyTag; anchor: HTMLElement; line: string } | null = null;
  let editing: { tagId: string; choosing: "color" | null; conflict: { tagId: string; label: string } | null } | null = null;
  let confirming: { action: TagAction; title: string; verb: string } | null = null;

  $: if (open) void jobs.refresh();
  else menu = merging = editing = confirming = null;

  function choose(id: (typeof MENU)[number][0]) {
    if (!menu) return;
    const { tag, anchor } = menu;
    menu = null;
    if (id === "merge") merging = { tag, anchor };
    else if (id === "delete") confirming = { action: { kind: "delete", tagId: tag.tagId }, title: `Delete “${tag.label}”?`, verb: "Delete" };
    else editing = { tagId: tag.tagId, choosing: id === "color" ? "color" : null, conflict: null };
  }

  function pickInto({ detail }: CustomEvent<TagPick>) {
    if (!merging || !("tagId" in detail)) return;
    const { tag } = merging;
    merging = null;
    confirming = { action: { kind: "merge", tagId: tag.tagId, into: detail.tagId }, title: `Merge “${tag.label}” into “${detail.label}”?`, verb: "Merge" };
  }

  async function run(action: TagAction) {
    confirming = null;
    const error = await jobs.run(action);
    if (!error) {
      editing = null;
      refocus(action.tagId);
    }
    return error;
  }

  async function save(tag: VocabularyTag, update: TagUpdate) {
    const error = await run({ kind: "update", tagId: tag.tagId, update });
    if (error?.code !== "label-exists" || !editing) return;
    const label = tags.find((other) => other.tagId === error.tagId)?.label ?? update.label ?? "";
    editing = { ...editing, conflict: { tagId: error.tagId ?? "", label } };
  }

  function dismiss() {
    const tagId = editing?.tagId ?? confirming?.action.tagId ?? "";
    editing = confirming = null;
    refocus(tagId);
  }

  // What held focus has gone; keep it inside the sheet.
  async function refocus(tagId: string) {
    await tick();
    const row = sheet?.querySelector<HTMLElement>(`[data-tag-menu="${CSS.escape(tagId)}"]`);
    (row ?? sheet?.querySelector<HTMLElement>("[data-close]"))?.focus();
  }

  function onKeydown(event: KeyboardEvent) {
    if (event.key === "Tab") {
      const items = [...sheet.querySelectorAll<HTMLButtonElement>("button, input")].filter((item) => !item.disabled && item.tabIndex >= 0);
      const [edge, next] = event.shiftKey ? [items[0], items.at(-1)] : [items.at(-1), items[0]];
      if ((sheet.getRootNode() as Document | ShadowRoot).activeElement === edge) {
        event.preventDefault();
        next?.focus();
      }
    } else if (event.key === "Escape" && !event.defaultPrevented) {
      event.preventDefault();
      if (editing || confirming) dismiss();
      else dispatch("close");
    }
  }

  function onMenuKeydown(event: KeyboardEvent) {
    const items = [...menuEl.querySelectorAll<HTMLButtonElement>("button:not(:disabled)")];
    const next = stepIndex(items.indexOf(event.target as HTMLButtonElement), event.key, items.length);
    if (next !== null) {
      event.preventDefault();
      items[next].focus();
    }
  }

  // Focus moves in on open, and back to what opened the sheet on close.
  function modal(node: HTMLElement) {
    let back = document.activeElement;
    while (back?.shadowRoot?.activeElement) back = back.shadowRoot.activeElement;
    node.querySelector<HTMLElement>("[data-close]")?.focus();
    return { destroy: () => (back as HTMLElement | null)?.focus() };
  }

  const matches = (query: string) => typeof window !== "undefined" && window.matchMedia(query).matches;

  function sheetSlide(_node: Element) {
    if (matches("(prefers-reduced-motion: reduce)")) return { duration: 0 };
    const axis = matches("(max-width: 720px)") ? "Y" : "X";
    return { duration: 320, easing: cubicOut, css: (_t: number, u: number) => `transform: translate${axis}(${u * 100}%)` };
  }

  const scrimFade = () => (matches("(prefers-reduced-motion: reduce)") ? { duration: 0 } : { duration: 200 });

  const focus = (node: HTMLElement, on = true) => {
    if (on) node.focus();
  };
</script>

{#if open}
  <button type="button" tabindex="-1" class="absolute inset-0 z-41 cursor-pointer bg-black/55 backdrop-blur-[3px]" aria-label="Close Manage tags"
    transition:fade={scrimFade()} on:click={() => dispatch("close")}></button>
  <div bind:this={sheet} use:modal role="dialog" aria-modal="true" aria-labelledby="tag-manager-title" tabindex="-1"
    transition:sheetSlide on:keydown={onKeydown}
    class="absolute inset-y-0 right-0 z-42 flex w-[min(560px,100%)] flex-col border-base-300 bg-base-100 shadow-2xl min-[721px]:border-l max-[720px]:top-auto max-[720px]:h-[92%] max-[720px]:w-full max-[720px]:rounded-t-box max-[720px]:border-t">
    <header class="tm-head flex items-center gap-2.5 border-b border-base-300 px-5 pb-3 pt-4 max-[720px]:px-4">
      <h2 id="tag-manager-title" class="flex flex-1 items-center gap-2.5 text-[17px] font-semibold">
        Manage tags <span class="badge badge-outline badge-sm font-medium">{plural(tags.length, "tag")}</span>
      </h2>
      <button type="button" data-close class="btn btn-square btn-ghost btn-sm" aria-label="Close" on:click={() => dispatch("close")}><X size={16} /></button>
    </header>
    <div class="min-h-0 flex-1 overflow-y-auto overscroll-contain px-3 pb-8 pt-1.5 max-[720px]:px-2">
      {#if $jobs.notice}<p role="alert" class="alert alert-warning alert-soft sticky top-0 z-10 my-1.5 py-2 text-sm">{$jobs.notice}</p>{/if}
      {#if tags.length === 0}<p class="px-2.5 py-6 text-sm text-base-content/60">No tags yet.</p>{/if}
      <ul>
        {#each tags as tag (tag.tagId)}
          {@const status = jobStatus($jobs, tag.tagId)}
          {@const changed = changedLine(tag)}
          <li class="tm-row relative flex min-h-13 flex-wrap items-center gap-x-2.5 gap-y-0.5 py-2 pl-2.5 pr-1" data-tag-color={colorFor(tag)}>
            {#if editing?.tagId === tag.tagId}
              <div class="basis-full py-0.5">
                <TagEditor {tag} conflict={editing.conflict} choosing={editing.choosing} on:save={(event) => save(tag, event.detail)}
                  on:cancel={dismiss} on:merge={(event) => run({ kind: "merge", tagId: tag.tagId, into: event.detail })} />
              </div>
            {:else}
              <!-- The chip is the tag, so it is what opens the tag's own
                   colour, icon and name. The menu still holds the same Rename,
                   and everything else a chip cannot say. -->
              <button type="button" class="tm-chip flex min-w-0 flex-1 cursor-pointer items-center" aria-label={`Edit ${tag.label}`}
                on:click={() => (editing = { tagId: tag.tagId, choosing: null, conflict: null })}>
                <TagChip label={tag.label} color={colorFor(tag)} icon={tag.icon} variant="whole" />
              </button>
              <span class="whitespace-nowrap text-xs tabular-nums text-base-content/65">{countsLine(tag)}</span>
              {#if changed}
                <button type="button" class="btn btn-square btn-ghost btn-sm text-base-content/55" aria-label={`Last change to ${tag.label}`}
                  aria-haspopup="dialog" aria-expanded={history?.tag.tagId === tag.tagId}
                  on:click={(event) => (history = history?.anchor === event.currentTarget ? null : { tag, anchor: event.currentTarget, line: changed })}>
                  <History size={15} />
                </button>
              {/if}
              <button type="button" data-tag-menu={tag.tagId} class="btn btn-square btn-ghost btn-sm" aria-label={`Actions for ${tag.label}`} aria-haspopup="menu"
                aria-expanded={menu?.tag.tagId === tag.tagId} on:click={(event) => (menu = menu?.anchor === event.currentTarget ? null : { tag, anchor: event.currentTarget })}>
                <Ellipsis size={16} />
              </button>
            {/if}
            {#if status}
              {@const rerun = status.rerun}
              <p role="status" class="order-2 basis-full text-xs text-base-content/75">
                {status.text}{#if status.failed.length > 0}: {status.failed.join(", ")}{/if}
                {#if rerun}<button type="button" class="link ml-1 font-medium" on:click={() => run(rerun)}>Run again</button>{/if}
              </p>
            {/if}
            {#if confirming?.action.tagId === tag.tagId}
              {@const pending = confirming}
              <div class="order-2 my-1 grid basis-full gap-1 rounded-box border border-base-300 bg-base-200 p-3">
                <p class="font-medium">{pending.title}</p>
                <p class="text-xs text-base-content/65">{confirmLine(tag.meetings)}</p>
                <div class="mt-1 flex justify-end gap-2">
                  <button type="button" class="btn btn-ghost btn-sm" on:click={dismiss}>Cancel</button>
                  <button type="button" use:focus class="btn btn-sm {pending.action.kind === 'delete' ? 'btn-error' : 'btn-neutral'}"
                    on:click={() => run(pending.action)}>{pending.verb}</button>
                </div>
              </div>
            {/if}
          </li>
        {/each}
      </ul>
    </div>
    {#if menu}
      <div bind:this={menuEl} use:popover={{ anchor: menu.anchor, close: () => (menu = null) }} role="menu" tabindex="-1"
        aria-label={`Actions for ${menu.tag.label}`} class="tag-popover grid w-44 p-1" on:keydown={onMenuKeydown}>
        {#each MENU as [id, text], index (id)}
          <button type="button" role="menuitem" use:focus={index === 0} disabled={id === "merge" && tags.length < 2} class:text-error={id === "delete"}
            class="rounded-field px-2.5 py-1.5 text-left text-sm hover:bg-base-200 focus-visible:bg-base-200 focus-visible:outline-none disabled:opacity-40"
            on:click={() => choose(id)}>{text}</button>
        {/each}
      </div>
    {/if}
    {#if history}
      {@const shown = history}
      <div use:popover={{ anchor: shown.anchor, close: () => (history = null) }} role="dialog"
        aria-label={`Last change to ${shown.tag.label}`} class="tag-popover w-56 p-2.5 text-xs">
        {shown.line}
      </div>
    {/if}
    {#if merging}
      {@const from = merging.tag}
      <TagPicker tags={tags.filter((tag) => tag.tagId !== from.tagId)} creatable={false} label={`Merge “${from.label}” into`}
        anchor={merging.anchor} on:pick={pickInto} on:close={() => (merging = null)} />
    {/if}
  </div>
{/if}

<style>
  /* Drawn between the content, not the row box: the row is padded so the chip
     and the menu button sit inside it, and a border on the box itself ran past
     both of them. */
  .tm-row:not(:last-child)::after {
    content: "";
    position: absolute;
    left: 10px;
    right: 4px;
    bottom: 0;
    border-bottom: 1px solid var(--tm-line, color-mix(in oklch, var(--color-base-content) 7%, transparent));
  }

  .tm-chip {
    justify-content: flex-start;
  }
  .tm-chip:hover :global(.tag-chip),
  .tm-chip:focus-visible :global(.tag-chip) {
    box-shadow: 0 0 0 1px var(--tag);
  }

  .tm-chip :global(.tag-chip) {
    height: 22px;
    gap: 4px;
    padding: 0 7px;
    font-size: 12.5px;
  }
</style>
