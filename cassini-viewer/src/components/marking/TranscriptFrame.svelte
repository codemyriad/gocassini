<script lang="ts">
  import { onMount, tick } from "svelte";

  import type { FindStop } from "../../core/find";
  import {
    formatPreciseTime,
    nudge,
    rangeOfSpan,
    rowAt,
    spanForDrag,
    spanForRange,
    type TimedWord,
    type WordSpan,
  } from "../../core/marking";
  import { keyboardEventTargetsControl } from "../../core/wordInteraction";
  import type { AnnotationRequest, VocabularyTag } from "../../viewer/annotations";
  import MarkBrackets from "./MarkBrackets.svelte";
  import MarkingRail from "./MarkingRail.svelte";
  import MarksList from "./MarksList.svelte";
  import StretchToolbar from "./StretchToolbar.svelte";
  import TranscriptToolbar from "./TranscriptToolbar.svelte";
  import {
    moveRequest,
    pickColor,
    removeRequest,
    stretchRequest,
    viewMarks,
    type MarksSession,
    type PlacedMark,
    type TagPick,
  } from "./session";

  // The transcript column: its toolbar, and while marks are loaded the rail on
  // the left and the brackets on the right. Words are decorated through their
  // attributes, so a drag never re-renders the transcript.
  export let session: MarksSession;
  export let vocabulary: readonly VocabularyTag[] = [];
  export let words: readonly TimedWord[] = [];
  export let rows: readonly { startMs: number; endMs: number }[] = [];
  export let stops: readonly FindStop[] = [];
  export let query = "";
  export let onlyMatching = false;
  export let durationMs = 0;
  export let playheadMs = 0;
  export let seek: (ms: number) => void = () => {};
  export let stickTop = 0;
  export let viewHeight = 0;

  type Selection = WordSpan & { itemId?: string; moved?: boolean };
  type Point = { x: number; y: number; h: number };
  const EDGES = ["from", "to"] as const;

  let root: HTMLElement;
  let textCell: HTMLElement;
  let toolbar: TranscriptToolbar;
  let stretchToolbar: StretchToolbar | undefined;
  let width = 0;
  let barHeight = 0;
  let selection: Selection | null = null;
  let armed: TagPick | null = null;
  let marksOpen = false;
  let hoverId: string | null = null;
  let stop = -1;
  // Every word on the page, timed or not, in page order; and each one's place.
  let page: HTMLElement[] = [];
  let pageAt = new Map<string, number>();
  let brackets: { mark: PlacedMark; top: number; height: number }[] = [];
  let pins: { from: Point; to: Point } | null = null;
  let pinDrag: "from" | "to" | null = null;
  const painted = new Map<string, Map<HTMLElement, string>>();

  $: marking = $session.status === "ready";
  $: view = marking ? viewMarks($session, vocabulary) : null;
  $: wide = width >= 720;
  $: indexById = new Map(words.map((word, index) => [word.id, index]));
  $: range = selection ? rangeOfSpan(words, selection) : null;
  $: selectedMark = view?.placed.find((mark) => mark.item.id === selection?.itemId) ?? null;
  $: hoverMark = view?.placed.find((mark) => mark.item.id === hoverId) ?? null;
  $: selColor = selectedMark?.color ?? (armed ? pickColor(armed, vocabulary) : "slate");
  $: stretchProps = marking &&
    range && {
      startMs: range.startMs,
      endMs: range.endMs,
      mark: selectedMark,
      moved: Boolean(selection?.moved),
      armed,
      vocabulary,
      busy: $session.busy,
    };

  let seenWords = words;
  $: if (words !== seenWords) {
    seenWords = words;
    selection = null;
  }

  $: void reindex(rows, words, marking);
  async function reindex(..._changed: unknown[]) {
    await tick();
    page = [...(textCell?.querySelectorAll<HTMLElement>("[data-word-id]") ?? [])];
    pageAt = new Map(page.map((element, at) => [element.dataset.wordId!, at]));
  }

  let seenStops = stops;
  $: if (stops !== seenStops) {
    seenStops = stops;
    stop = stops.length > 0 ? 0 : -1;
    void tick().then(revealStop);
  }

  // Where a span of words lies on the page, the untimed words inside it included.
  function onPage(span: WordSpan | null | undefined, places: Map<string, number>): [number, number] | null {
    let first = Number.POSITIVE_INFINITY;
    let last = Number.NEGATIVE_INFINITY;
    for (let index = span?.from ?? 0; span && index <= span.to; index += 1) {
      const at = places.get(words[index]!.id);
      if (at !== undefined) {
        first = Math.min(first, at);
        last = Math.max(last, at);
      }
    }
    return first <= last ? [first, last] : null;
  }

  function paint(attribute: string, next: Map<HTMLElement, string>) {
    const previous = painted.get(attribute) ?? new Map<HTMLElement, string>();
    for (const element of previous.keys()) {
      if (!next.has(element)) element.removeAttribute(attribute);
    }
    for (const [element, value] of next) {
      if (previous.get(element) !== value) element.setAttribute(attribute, value);
    }
    painted.set(attribute, next);
  }

  const across = (stretch: [number, number] | null) =>
    new Map(stretch ? page.slice(stretch[0], stretch[1] + 1).map((element) => [element, ""]) : []);

  $: selected = onPage(selection, pageAt);
  $: paint("data-sel", across(selected));
  $: paint("data-lit", across(hoverMark && onPage(spanForRange(words, hoverMark.startMs, hoverMark.endMs), pageAt)));
  $: paint(
    "data-find",
    new Map(
      stops.flatMap((found, index) => {
        const element = page[pageAt.get(found.id) ?? -1];
        return element ? [[element, index === stop ? "current" : ""] as const] : [];
      }),
    ),
  );

  function edges(stretch: [number, number] | null) {
    const first = stretch && page[stretch[0]]?.getClientRects()[0];
    const lines = stretch && page[stretch[1]]?.getClientRects();
    const last = lines?.[lines.length - 1];
    return first && last ? { first, last } : null;
  }

  function layout() {
    if (!textCell) return;
    const origin = textCell.getBoundingClientRect();
    const point = (rect: DOMRect, x: number): Point => ({ x: x - origin.left, y: rect.top - origin.top, h: rect.height });
    const ends = edges(selected);
    pins = ends && { from: point(ends.first, ends.first.left), to: point(ends.last, ends.last.right) };
    brackets = (view?.placed ?? []).flatMap((mark) => {
      const found = edges(onPage(spanForRange(words, mark.startMs, mark.endMs), pageAt));
      if (!found) return [];
      const top = Math.min(found.first.top, found.last.top) - origin.top - 3;
      return [{ mark, top, height: Math.max(found.first.bottom, found.last.bottom) - origin.top + 3 - top }];
    });
  }
  $: selected, view, pageAt, width, void tick().then(layout);

  function revealWord(index: number | undefined, block: ScrollLogicalPosition = "center") {
    page[pageAt.get(words[index ?? -1]?.id ?? "") ?? -1]?.scrollIntoView({ block });
  }

  function revealStop() {
    page[pageAt.get(stops[stop]?.id ?? "") ?? -1]?.scrollIntoView({ block: "center" });
  }

  function step(direction: 1 | -1) {
    stop = (stop + direction + stops.length) % stops.length;
    revealStop();
  }

  function setEdge(edge: "from" | "to", index: number) {
    if (!selection) return;
    selection =
      edge === "from"
        ? { ...selection, from: Math.min(index, selection.to), moved: true }
        : { ...selection, to: Math.max(index, selection.from), moved: true };
  }

  function grab(aMs: number, bMs: number, handle: boolean) {
    const span = spanForDrag(words, aMs, bMs);
    selection = span && handle && selection ? { ...span, itemId: selection.itemId, moved: true } : span;
    revealWord(spanForDrag(words, bMs, bMs)?.from);
  }

  function pickTurn(ms: number) {
    const row = rowAt(rows, ms);
    selection = (row && spanForRange(words, row.startMs, row.endMs + 1)) ?? spanForDrag(words, ms, ms);
    revealWord(selection?.from);
  }

  // Its end, where the toolbar sits.
  function selectMark(mark: PlacedMark) {
    const span = spanForRange(words, mark.startMs, mark.endMs);
    if (span) {
      selection = { ...span, itemId: mark.item.id };
      revealWord(span.to);
    }
  }

  function jump(mark: PlacedMark) {
    marksOpen = false;
    seek(mark.startMs);
    revealWord(spanForRange(words, mark.startMs, mark.endMs)?.from);
  }

  function grabPin(event: PointerEvent, edge: "from" | "to") {
    if (event.button > 0) return;
    event.preventDefault();
    const pin = event.currentTarget as HTMLElement;
    pin.setPointerCapture?.(event.pointerId);
    pin.focus({ preventScroll: true });
    pinDrag = edge;
  }

  // The timed word nearest the page word under the pointer.
  function dragPin(event: PointerEvent) {
    if (!pinDrag) return;
    const scope = root.getRootNode() as Document | ShadowRoot;
    const word = scope
      .elementsFromPoint(event.clientX, event.clientY)
      .find((element): element is HTMLElement => element instanceof HTMLElement && Boolean(element.dataset.wordId));
    const at = word ? pageAt.get(word.dataset.wordId!) : undefined;
    for (let distance = 0; at !== undefined && distance < page.length; distance += 1) {
      const index = indexById.get(page[at - distance]?.dataset.wordId ?? "") ?? indexById.get(page[at + distance]?.dataset.wordId ?? "");
      if (index !== undefined) {
        setEdge(pinDrag, index);
        return;
      }
    }
  }

  function nudgePin(event: KeyboardEvent, edge: "from" | "to") {
    const direction = { ArrowRight: 1, ArrowDown: 1, ArrowLeft: -1, ArrowUp: -1 }[event.key] as 1 | -1 | undefined;
    if (!direction || !selection) return;
    event.preventDefault();
    selection = { ...nudge(words, selection, edge, direction, event.shiftKey), moved: true };
    revealWord(selection[edge], "nearest");
  }

  async function write(request: AnnotationRequest) {
    if (await session.write(request)) selection = null;
  }
  const tagStretch = (event: CustomEvent<TagPick>) =>
    range && write(stretchRequest(event.detail, range.startMs, range.endMs));
  const saveMove = () =>
    selectedMark && range && write(moveRequest(selectedMark.item.id, selectedMark.tag, range.startMs, range.endMs));
  const removeMark = () => selectedMark && write(removeRequest([selectedMark.item.id]));

  // Capture phase, so Esc clears a selection before the shell closes the sheet on it.
  function onKeydown(event: KeyboardEvent) {
    if (event.defaultPrevented || !root || root.offsetParent === null) return;
    const path = event.composedPath();
    if ((event.ctrlKey || event.metaKey) && !event.altKey && event.key.toLowerCase() === "f") {
      const scope = root.closest(".meeting-viewer") ?? root;
      if (path.includes(scope) || path[0] === document.body || path[0] === document.documentElement) {
        event.preventDefault();
        toolbar.focusFind();
      }
      return;
    }
    const inField = path.some((node) => node instanceof HTMLElement && node.matches("input, textarea, select, [role='dialog']"));
    if (!marking || inField) return;
    if (event.key === "Escape") {
      if (selection) selection = null;
      else if (marksOpen) marksOpen = false;
      else if (armed) armed = null;
      else return;
      event.preventDefault();
    } else if (event.key === "Enter" && selection) {
      const onPin = path.some((node) => node instanceof HTMLElement && node.hasAttribute("data-pin"));
      if (onPin || !keyboardEventTargetsControl(event)) {
        event.preventDefault();
        stretchToolbar?.confirm();
      }
    }
  }

  onMount(() => {
    window.addEventListener("keydown", onKeydown, true);
    return () => window.removeEventListener("keydown", onKeydown, true);
  });
</script>

<div bind:this={root} bind:clientWidth={width} class="frame">
  <div
    bind:offsetHeight={barHeight}
    class="sticky z-10 -mx-2 grid gap-2 border-b border-base-300 bg-base-200 px-2 py-2 {armed ? 'shadow-[inset_0_-2px_0_var(--tag)]' : ''}"
    style:top="{stickTop}px"
    data-tag-color={armed ? pickColor(armed, vocabulary) : undefined}
  >
    <TranscriptToolbar
      bind:this={toolbar}
      bind:query
      bind:onlyMatching
      bind:armed
      bind:marksOpen
      stops={stops.length}
      current={stop}
      tagging={marking}
      {vocabulary}
      marksCount={view?.placed.length ?? 0}
      on:step={(event) => step(event.detail)}
    />
    {#if marksOpen && view}
      <MarksList marks={view.placed} on:jump={(event) => jump(event.detail)} />
    {/if}
    {#if stretchProps && !wide}
      <StretchToolbar bind:this={stretchToolbar} {...stretchProps} on:tag={tagStretch} on:save={saveMove} on:remove={removeMark} on:clear={() => (selection = null)} />
    {/if}
  </div>

  <div
    class="mt-3 grid"
    style:grid-template-columns={marking ? (wide ? "64px minmax(0,1fr) 196px" : "34px minmax(0,1fr) 26px") : "minmax(0,1fr)"}
    style:--sel="var(--tag-bg)"
    style:--sel-edge="var(--tag)"
    data-tag-color={selColor}
  >
    {#if marking}
      <div>
        <div class="sticky" style:top="{stickTop + barHeight + 12}px" style:height="{Math.max(160, viewHeight - barHeight - 28)}px">
          <MarkingRail
            {durationMs}
            {playheadMs}
            marks={view?.placed ?? []}
            selection={range}
            color={selColor}
            stops={stops.map((found) => found.ms)}
            current={stop}
            labels={wide}
            on:grab={(event) => grab(event.detail.aMs, event.detail.bMs, event.detail.handle)}
            on:pick={(event) => pickTurn(event.detail)}
            on:select={(event) => selectMark(event.detail)}
          />
        </div>
      </div>
    {/if}
    <div bind:this={textCell} class="relative min-w-0" data-tag-color={hoverMark?.color ?? selColor}>
      <slot />
      {#if marking && pins && range}
        {#each EDGES as edge (edge)}
          {@const ms = edge === "from" ? range.startMs : range.endMs}
          <span
            class="absolute -ml-px w-0 cursor-grab touch-none border-l-2 border-(--sel-edge) outline-none before:absolute before:-inset-x-2.5 before:-inset-y-1.5 before:content-[''] after:absolute after:-left-1.5 after:size-2.5 after:rounded-full after:bg-(--sel-edge) after:content-[''] focus-visible:after:ring-3 focus-visible:after:ring-(--sel) {edge === 'from' ? 'after:-top-2' : 'after:-bottom-2'}"
            style:left="{pins[edge].x}px"
            style:top="{pins[edge].y}px"
            style:height="{pins[edge].h}px"
            role="slider"
            tabindex="0"
            data-pin
            aria-label={edge === "from" ? "Where the stretch begins" : "Where the stretch ends"}
            aria-valuemin={0}
            aria-valuemax={durationMs}
            aria-valuenow={ms}
            aria-valuetext={formatPreciseTime(ms)}
            aria-keyshortcuts="ArrowLeft ArrowRight Shift+ArrowLeft Shift+ArrowRight"
            on:pointerdown={(event) => grabPin(event, edge)}
            on:pointermove={dragPin}
            on:pointerup={() => (pinDrag = null)}
            on:pointercancel={() => (pinDrag = null)}
            on:keydown={(event) => nudgePin(event, edge)}
            ><span
              class="pointer-events-none absolute left-[7px] rounded-[3px] bg-base-100 px-1 font-mono text-[10.5px] leading-[1.2] whitespace-nowrap text-(--sel-edge) ring-1 ring-(--sel-edge) {edge === 'from' ? 'bottom-[calc(100%+2px)]' : 'top-[calc(100%+2px)]'}"
              >{formatPreciseTime(ms)}</span
            ></span
          >
        {/each}
      {/if}
    </div>
    {#if marking}
      <div class="relative">
        <MarkBrackets {brackets} selectedId={selection?.itemId} bind:hoverId labels={wide} on:select={(event) => selectMark(event.detail)} />
        {#if stretchProps && wide}
          <div class="absolute right-0" style:top="{pins ? pins.to.y + pins.to.h + 6 : 0}px">
            <StretchToolbar bind:this={stretchToolbar} {...stretchProps} on:tag={tagStretch} on:save={saveMove} on:remove={removeMark} on:clear={() => (selection = null)} />
          </div>
        {/if}
      </div>
    {/if}
  </div>
</div>

<style>
  /* Rightward only: a later word's paint covers an earlier word's text, so a
     leftward bleed would clip the letter before a comma. */
  .frame :global([data-word-id]:is([data-sel], [data-lit]):not([data-active="true"])) {
    border-radius: 0;
    background: var(--hl);
    box-shadow: 0.3em 0 0 var(--hl);
  }
  .frame :global([data-word-id][data-sel]) {
    --hl: var(--sel);
  }
  .frame :global([data-word-id][data-lit]) {
    --hl: var(--tag-bg);
  }
  .frame :global([data-word-id][data-find]:not([data-active="true"])) {
    background: color-mix(in oklab, var(--color-warning) 35%, transparent);
    box-shadow: 0 0 0 1px color-mix(in oklab, var(--color-warning) 70%, transparent);
  }
  .frame :global([data-word-id][data-find="current"]:not([data-active="true"])) {
    color: var(--color-warning-content);
    background: var(--color-warning);
  }
</style>
