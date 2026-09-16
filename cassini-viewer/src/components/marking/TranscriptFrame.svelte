<script lang="ts">
  import { onMount, tick } from "svelte";
  import { ChevronDown, ChevronUp } from "@lucide/svelte";

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
  import {
    markRequest,
    moveStretchOps,
    removeRequest,
    timeRange,
    type AnnotationRequest,
    type TagPick,
    type VocabularyTag,
  } from "../../viewer/annotations";
  import MarkBrackets, { BRACKET_STEP_NARROW } from "./MarkBrackets.svelte";
  import MarkingRail from "./MarkingRail.svelte";
  import TagChip from "../tags/TagChip.svelte";
  import TagPicker from "../tags/TagPicker.svelte";
  import StretchToolbar from "./StretchToolbar.svelte";
  import TranscriptToolbar from "./TranscriptToolbar.svelte";
  import { pickColor, viewMarks, type MarksSession, type PlacedMark } from "./session";

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
  let rail: MarkingRail | undefined;
  let width = 0;
  let barHeight = 0;
  let selection: Selection | null = null;
  let armed: TagPick | null = null;
  let armButton: HTMLButtonElement;
  let picking = false;
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
  $: bracketColumns = Math.max(0, ...(view?.placed.map((mark) => mark.column) ?? [])) + 1;
  // Where a tag chip begins, measured the way MarkBrackets measures it: the
  // bracket column's 10px inset, one step per overlapping bracket, and the gap
  // that clears the arms. The floating controls line up with it.
  $: tagsLeft = 10 + bracketColumns * 12 + 22;
  // The tag column is as wide as what stands in it: the brackets, the gap, and
  // the controls that head the column, which are the widest thing in it.
  const TAG_CONTROLS = 200;
  $: tagColumn = tagsLeft + TAG_CONTROLS;
  // The bracket of the section being edited, so its card can stand where its
  // tag stands.
  $: selectedBracket = brackets.find(({ mark }) => mark.item.id === selection?.itemId) ?? null;
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
      error: $session.errorFrom === "stretch" ? $session.error : "",
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

  // Every word in the stretch, and the punctuation and spaces BETWEEN them:
  // painting the words alone left the rule broken at every comma.
  const across = (stretch: [number, number] | null) => {
    const marked = new Map<HTMLElement, string>();
    if (!stretch) return marked;
    const within = page.slice(stretch[0], stretch[1] + 1);
    within.forEach((element, index) => {
      marked.set(element, "");
      if (index === 0) return;
      const gap = element.previousElementSibling;
      if (gap instanceof HTMLElement && gap.classList.contains("cassini-gap")) {
        marked.set(gap, "");
      }
    });
    return marked;
  };

  $: selected = onPage(selection, pageAt);
  $: selMarks = across(selected);
  $: litMarks = across(hoverMark && onPage(spanForRange(words, hoverMark.startMs, hoverMark.endMs), pageAt));
  $: paint("data-sel", selMarks);
  $: paint("data-lit", litMarks);

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

  // Apart, so a drag re-measures its two ends and not every mark.
  function placePins() {
    const origin = textCell?.getBoundingClientRect();
    const ends = origin && edges(selected);
    const point = (rect: DOMRect, x: number): Point => ({ x: x - origin!.left, y: rect.top - origin!.top, h: rect.height });
    pins = ends ? { from: point(ends.first, ends.first.left), to: point(ends.last, ends.last.right) } : null;
  }
  $: selected, pageAt, width, void tick().then(placePins);

  function placeBrackets() {
    const origin = textCell?.getBoundingClientRect();
    brackets = (origin && view?.placed ? view.placed : []).flatMap((mark) => {
      const found = edges(onPage(spanForRange(words, mark.startMs, mark.endMs), pageAt));
      if (!found) return [];
      const top = Math.min(found.first.top, found.last.top) - origin!.top - 3;
      return [{ mark, top, height: Math.max(found.first.bottom, found.last.bottom) - origin!.top + 3 - top }];
    });
  }
  $: view, pageAt, width, void tick().then(placeBrackets);

  function reveal(wordId: string | undefined, block: ScrollLogicalPosition = "center", behavior: ScrollBehavior = "auto") {
    page[pageAt.get(wordId ?? "") ?? -1]?.scrollIntoView({ block, behavior });
  }
  const revealWord = (index: number | undefined, block?: ScrollLogicalPosition, behavior?: ScrollBehavior) =>
    reveal(words[index ?? -1]?.id, block, behavior);
  // A glide where one is welcome, so the reader sees which way they went.
  const glide = (): ScrollBehavior => (matchMedia("(prefers-reduced-motion: reduce)").matches ? "auto" : "smooth");
  const revealStop = () => reveal(stops[stop]?.id);

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

  // Which tagged section the arrows last went to, so they walk the list in
  // order rather than always from the top.
  let markAt = -1;

  function stepMark(delta: 1 | -1) {
    const placed = view?.placed ?? [];
    if (placed.length === 0) return;
    markAt = (markAt + delta + placed.length) % placed.length;
    const mark = placed[markAt];
    seek(mark.startMs);
    revealWord(spanForRange(words, mark.startMs, mark.endMs)?.from, "center", glide());
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
    if (await session.write(request, "stretch")) void clearSelection();
  }

  // The toolbar and the pins go with the selection; focus left with nowhere to be goes to the rail.
  async function clearSelection() {
    selection = null;
    await tick();
    const active = (root.getRootNode() as Document | ShadowRoot).activeElement;
    if (!active || active === document.body) rail?.focus();
  }
  const tagStretch = (event: CustomEvent<TagPick>) =>
    range && write(markRequest(event.detail, timeRange(range.startMs, range.endMs)));
  const saveMove = () =>
    selectedMark && range && write({ ops: moveStretchOps(selectedMark.item.id, selectedMark.tag, range.startMs, range.endMs) });
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
      if (selection) void clearSelection();
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
  <!-- The heading line of the section this frame is: its title, filled by the
       shell, and what is already tagged in it. Both scroll away; only the
       search below sticks. -->
  <div class="tf-head">
    <slot name="title" />
  </div>
  <div
    bind:offsetHeight={barHeight}
    class="tf-bar sticky z-10 grid gap-2 bg-base-200 py-3 {armed ? 'shadow-[inset_0_-2px_0_var(--tag)]' : ''}"
    style:top="{Math.max(0, stickTop - 1)}px"
    style:margin-inline="calc(-1 * var(--tf-bleed, 8px))"
    style:padding-inline="var(--tf-bleed, 8px)"
    data-tag-color={armed ? pickColor(armed, vocabulary) : undefined}
  >
    <TranscriptToolbar
      bind:this={toolbar}
      bind:query
      bind:onlyMatching
      stops={stops.length}
      current={stop}
      on:step={(event) => step(event.detail)}
    />
    {#if stretchProps && !wide}
      <StretchToolbar bind:this={stretchToolbar} {...stretchProps} on:tag={tagStretch} on:arm={(event) => (armed = event.detail)} on:save={saveMove} on:remove={removeMark} on:clear={clearSelection} />
    {/if}
  </div>

  {#if marking}
    <!-- Its own bar under the search: what is tagged in this transcript is a
         different subject from what was typed into it, and it stays reachable
         while reading, which is the point of stepping through it. -->
    <div
      class="tf-tagbar relative sticky z-10 flex items-center gap-2 bg-base-200 py-2"
      style:top="{Math.max(0, stickTop - 1) + barHeight}px"
      style:margin-inline="calc(-1 * var(--tf-bleed, 8px))"
      style:padding-inline="var(--tf-bleed, 8px)"
      style:--tf-tags-left="{tagsLeft - 10}px"
      style:--tf-tag-column="{tagColumn}px"
    >
      <div class="tf-tagbar-inner flex items-center gap-2">
      <span class="tf-marks">
        {view?.placed.length ?? 0}
        {(view?.placed.length ?? 0) === 1 ? "tagged section" : "tagged sections"}
      </span>
      {#if (view?.placed.length ?? 0) > 0}
        <span class="tf-marks-group">
          <button type="button" class="tf-marks-step" aria-label="Previous tagged section" title="Previous tagged section" on:click={() => stepMark(-1)}>
            <ChevronUp size={13} aria-hidden="true" />
          </button>
          <button type="button" class="tf-marks-step" aria-label="Next tagged section" title="Next tagged section" on:click={() => stepMark(1)}>
            <ChevronDown size={13} aria-hidden="true" />
          </button>
        </span>
      {/if}
      <!-- The tag held ready for the next section, said where tagging is: it
           is the one mode this transcript can be in, so it names itself and
           offers the way out. -->
      {#if armed}
        <span class="tf-armed join" role="group" aria-label="Tagging with">
          <button bind:this={armButton} type="button" class="join-item btn btn-xs" title="Change the tag" aria-haspopup="dialog" aria-expanded={picking} on:click={() => (picking = !picking)}>
            <TagChip label={armed.label} color={pickColor(armed, vocabulary)} />
          </button>
          <button type="button" class="join-item btn btn-xs" on:click={() => (armed = null)}>Stop <kbd class="kbd kbd-xs">Esc</kbd></button>
        </span>
        {#if picking}
          <TagPicker
            tags={vocabulary}
            label="Tag sections with"
            anchor={armButton}
            on:pick={(event) => ((armed = event.detail), (picking = false))}
            on:close={() => (picking = false)}
          />
        {/if}
      {/if}
      </div>
    </div>
  {/if}

  <div
    class="mt-6 grid"
    style:grid-template-columns={marking
      ? wide
        ? `68px minmax(0,1fr) ${tagColumn}px`
        : `34px minmax(0,1fr) ${Math.max(26, bracketColumns * BRACKET_STEP_NARROW + 2)}px`
      : "minmax(0,1fr)"}
    style:--sel="var(--tag-bg)"
    style:--sel-edge="var(--tag)"
    data-tag-color={selColor}
  >
    {#if marking}
      <div>
        <div class="sticky" style:top="{stickTop + barHeight + 12}px" style:height="{Math.max(160, viewHeight - barHeight - 28)}px">
          <MarkingRail
            bind:this={rail}
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
            aria-label={edge === "from" ? "Where the section begins" : "Where the section ends"}
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
              class="tf-pin-time pointer-events-none absolute left-[7px] px-1.5 py-[2px] font-mono text-[10.5px] leading-[1.2] whitespace-nowrap text-(--sel-edge) {edge === 'from' ? 'bottom-[calc(100%+4px)]' : 'top-[calc(100%+4px)]'}"
              >{formatPreciseTime(ms)}</span
            ></span
          >
        {/each}
      {/if}
    </div>
    {#if marking}
      <!-- Inset, so the brackets and their tags sit clear of the floating
           tagged-sections controls above them. -->
      <div class="relative pl-2.5">
        <MarkBrackets
          {brackets}
          selectedId={selection?.itemId}
          bind:hoverId
          labels={wide}
          stickTop={Math.max(0, stickTop - 1) + barHeight + (wide ? 44 : 8)}
          on:select={(event) => selectMark(event.detail)}
        />
        {#if stretchProps && wide}
          <!-- Under that section's own tag, which never moves: the chip stays
               exactly where it was and the card opens beneath it. -->
          <div
            class="absolute"
            style:top="{selectedBracket ? selectedBracket.top + 26 : pins ? pins.to.y + pins.to.h + 6 : 0}px"
            style:height={selectedBracket ? `${Math.max(0, selectedBracket.height - 26)}px` : undefined}
            style:left="{selectedBracket ? tagsLeft - 10 : 0}px"
            style:right={selectedBracket ? "auto" : "0"}
          >
            <!-- Rides its own section, like the tag it replaced: it holds the
                 line under the bars while any of that section is on screen. -->
            <div
              class="sticky"
              style:top="{selectedBracket ? Math.max(0, stickTop - 1) + barHeight + (wide ? 44 : 8) + 26 : 0}px"
            >
              <StretchToolbar bind:this={stretchToolbar} {...stretchProps} on:tag={tagStretch} on:arm={(event) => (armed = event.detail)} on:save={saveMove} on:remove={removeMark} on:clear={clearSelection} />
            </div>
          </div>
        {/if}
      </div>
    {/if}
  </div>
</div>

<style>
  /* The pin's own time, floating over the words it sits between: it needs to
     be legible against whatever is behind it, so it carries a surface of its
     own rather than a hairline ring. */
  .tf-pin-time {
    background-color: var(--color-base-100);
    border: 1px solid var(--sel-edge);
    border-radius: 5px;
    box-shadow: 0 2px 8px oklch(0% 0 0 / 0.35);
  }

  .tf-head {
    display: flex;
    align-items: baseline;
    justify-content: space-between;
    gap: 12px;
  }
  /* On a wide screen this is a small floating cluster in the margin beside the
     transcript rather than a second full-width strip: the transcript is what
     the screen is for, and these controls are consulted rather than read. */
  @media (min-width: 981px) {
    .tf-tagbar {
      height: 0;
      padding-block: 0;
      overflow: visible;
    }
    .tf-tagbar::after {
      display: none;
    }
    .tf-tagbar-inner {
      position: absolute;
      top: 8px;
      /* The bar bleeds past the frame on both sides, and the tag column is the
         frame's last --tf-tag-column; a tag starts --tf-tags-left into it.
         These controls span from there to the frame's edge, as the tags do. */
      left: calc(100% - var(--tf-bleed, 8px) - var(--tf-tag-column, 196px) + var(--tf-tags-left, 0px));
      right: var(--tf-bleed, 8px);
      flex-wrap: wrap;
      row-gap: 6px;
    }
    /* The count reads from the tag's edge, the arrows hold the far one. */
    .tf-marks-group {
      margin-left: auto;
    }
  }

  /* The rule stops where the content does, like the search bar's own. */
  .tf-tagbar::after {
    content: "";
    position: absolute;
    left: var(--tf-bleed, 8px);
    right: var(--tf-bleed, 8px);
    bottom: 0;
    border-bottom: 1px solid var(--color-base-300);
  }
  .tf-armed {
    margin-left: auto;
  }
  @media (min-width: 981px) {
    .tf-armed {
      flex-basis: 100%;
      margin-left: 0;
    }
  }
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
  .tf-marks-step:hover {
    background-color: color-mix(in oklch, var(--color-base-content) 8%, transparent);
    color: var(--color-base-content);
  }
  /* A count, not a control: the arrows beside it are how to reach them. */
  .tf-marks {
    flex: none;
    font-size: 12px;
    white-space: nowrap;
    color: color-mix(in oklch, var(--color-base-content) 70%, transparent);
  }

  /* The rule sits inside the bar's own padding rather than under its full
     bleed, so it lines up with the content it divides. */
  .tf-bar {
    position: sticky;
  }
  .tf-bar::after {
    content: "";
    position: absolute;
    left: var(--tf-bleed, 8px);
    right: var(--tf-bleed, 8px);
    bottom: 0;
    border-bottom: 1px solid var(--color-base-300);
  }


  /* Rightward only: a later word's paint covers an earlier word's text, so a
     leftward bleed would clip the letter before a comma. */
  /* Underlined, not filled: a tagged stretch is an annotation ON the words,
     and a block of colour behind them competes with reading them — and with
     the search highlight, which is a fill. */
  /* An estimated word timing is drawn as a dashed underline of its own, which
     inside a tagged passage reads as the tag's rule breaking up. The tag's
     line wins there; the dashes are still on every word outside it. */
  .frame :global(.cassini-word-interpolated:is([data-sel], [data-lit])) {
    text-decoration: none;
  }

  /* One rule under the passage. The words carry it — timed words are buttons
     and untimed words and punctuation are spans given the same box (app.css),
     so they share one bottom edge — and each runs it on over the space that
     follows, into the next word. The space is never painted itself: as a box
     of its own it rounded to a different device pixel from the words beside
     it, and the rule stepped. */
  .frame :global([data-word-id]:is([data-sel], [data-lit]):not([data-active="true"])) {
    border-radius: 0;
    background: linear-gradient(var(--hl-edge), var(--hl-edge)) no-repeat 0 100% / 100% 2px;
    text-decoration: none;
    box-shadow: none;
    padding-right: 0.27em;
    margin-right: -0.27em;
  }
  /* Hovering a word inside a tagged passage: the fill behind the word, and the
     rule still along the bottom, as two layers of one background. */
  .frame :global([data-word-id]:is([data-sel], [data-lit]):hover:not([data-active="true"])) {
    background-image: linear-gradient(var(--hl-edge), var(--hl-edge)),
      linear-gradient(
        color-mix(in oklch, var(--color-base-content) 14%, transparent),
        color-mix(in oklch, var(--color-base-content) 14%, transparent)
      );
    background-size: 100% 2px, calc(100% - 0.27em) 100%;
    background-position: 0 100%, 0 0;
    background-repeat: no-repeat, no-repeat;
  }
  .frame :global([data-word-id][data-sel]) {
    --hl-edge: var(--sel-edge);
  }
  .frame :global([data-word-id][data-lit]) {
    --hl-edge: var(--tag);
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
