<script lang="ts">
  import { onMount, tick } from "svelte";

  import type { FindStop } from "../../core/find";
  import {
    formatPreciseTime,
    nudge,
    rangeOfSpan,
    rowAt,
    spanForDrag,
    spanForPage,
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
  import MarkBrackets, { LABEL_STEP } from "./MarkBrackets.svelte";
  import MarkingRail from "./MarkingRail.svelte";
  import SectionsNav from "./SectionsNav.svelte";
  import StretchToolbar from "./StretchToolbar.svelte";
  import TranscriptToolbar from "./TranscriptToolbar.svelte";
  import { viewMarks, type MarksSession, type PlacedMark } from "./session";

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
  // How much of the bottom of the view the player covers.
  export let stickBottom = 0;
  export let viewHeight = 0;

  // `native`: made with the reader's own text selection, whose handles stand
  // where the pins would.
  type Selection = WordSpan & { itemId?: string; moved?: boolean; native?: boolean };
  type DomSelection = NonNullable<ReturnType<Document["getSelection"]>>;
  type Point = { x: number; y: number; h: number };
  const EDGES = ["from", "to"] as const;

  let root: HTMLElement;
  let textCell: HTMLElement;
  let toolbar: TranscriptToolbar;
  let stretchToolbar: StretchToolbar | undefined;
  let rail: MarkingRail | undefined;
  let width = 0;
  let barHeight = 0;
  let tagbarHeight = 0;
  let dockHeight = 0;
  let textHeight = 0;
  let selection: Selection | null = null;
  // The tag last put on a section here, offered again on the next one: a pass
  // tagging many sections alike is one click each, with no mode to set.
  let recent: TagPick | null = null;
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
  // What the sticky bars cover of the view: the tag bar at the top where the
  // screen is wide, the dock above the player where it is not.
  $: coverTop = barHeight + (marking && wide ? tagbarHeight : 0);
  $: coverBottom = marking && !wide ? dockHeight + 8 : 0;
  $: bracketColumns = Math.max(0, ...(view?.placed.map((mark) => mark.column) ?? [])) + 1;
  // Where a tag chip begins, measured the way MarkBrackets measures it: the
  // bracket column's 10px inset, one step per overlapping bracket, and the gap
  // that clears the arms. The floating controls line up with it.
  $: tagsLeft = 10 + bracketColumns * 12 + 22;
  // The tag column is as wide as what stands in it: the brackets, the gap, and
  // the controls that head the column, which are the widest thing in it.
  const TAG_CONTROLS = 200;
  // The rail's column where the screen is narrow: its 14px track and a clear
  // gap before the text.
  const RAIL_NARROW = 30;
  $: tagColumn = tagsLeft + TAG_CONTROLS;
  // The bracket of the section being edited, so its card can stand where its
  // tag stands.
  $: selectedBracket = brackets.find(({ mark }) => mark.item.id === selection?.itemId) ?? null;
  // Where under the bars its tag holds, and so where its card starts.
  $: cardDrop = selectedBracket ? selectedBracket.mark.column * LABEL_STEP + 26 : 0;
  $: indexById = new Map(words.map((word, index) => [word.id, index]));
  // On a narrow screen there is no column for tags to stand in, so each one
  // goes into the text in front of the first word it covers.
  $: chips = marking && !wide ? chipsByWord(view?.placed ?? []) : null;
  function chipsByWord(placed: readonly PlacedMark[]) {
    const byWord = new Map<string, PlacedMark[]>();
    for (const mark of placed) {
      const span = spanForRange(words, mark.startMs, mark.endMs);
      const id = span && words[span.from]?.id;
      if (id) byWord.set(id, [...(byWord.get(id) ?? []), mark]);
    }
    return byWord;
  }
  $: range = selection ? rangeOfSpan(words, selection) : null;
  $: selectedMark = view?.placed.find((mark) => mark.item.id === selection?.itemId) ?? null;
  $: hoverMark = view?.placed.find((mark) => mark.item.id === hoverId) ?? null;
  $: selColor = selectedMark?.color ?? "slate";
  $: stretchProps = marking &&
    range && {
      startMs: range.startMs,
      endMs: range.endMs,
      mark: selectedMark,
      moved: Boolean(selection?.moved),
      recent,
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

  // Every tagged section is underlined at rest, in its own colour: a bracket
  // says which lines a section is on, the rule which words, and on a narrow
  // screen there is no bracket at all. Where two overlap, the later one's
  // colour shows.
  $: restMarks = marking ? restByElement(view?.placed ?? [], page, pageAt) : new Map<HTMLElement, PlacedMark>();
  function restByElement(placed: readonly PlacedMark[], ..._changed: unknown[]) {
    const byElement = new Map<HTMLElement, PlacedMark>();
    for (const mark of placed) {
      for (const element of across(onPage(spanForRange(words, mark.startMs, mark.endMs), pageAt)).keys()) {
        byElement.set(element, mark);
      }
    }
    return byElement;
  }
  $: paint("data-rest", new Map([...restMarks.keys()].map((element) => [element, ""])));
  $: paint("data-tag-color", new Map([...restMarks].map(([element, mark]) => [element, mark.color])));

  // On a narrow screen a tap on underlined words opens their section where it
  // is, and still seeks, as a tap on any word does. On a wide one a click on
  // the text only seeks: a reader clicking about to listen would otherwise
  // open a card at every click, and the bracket and its tag open it there.
  function textClick(event: MouseEvent) {
    if (wide) return;
    const word = (event.target as Element | null)?.closest?.<HTMLElement>("[data-word-id]");
    const mark = word ? restMarks.get(word) : undefined;
    if (mark && mark.item.id !== selection?.itemId) selectMark(mark, false);
  }

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
    // A pin moved takes over from the reader's own highlight, which would
    // otherwise still show the old ends.
    if (selection.native) document.getSelection()?.removeAllRanges();
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

  // The reader's own selection inside this frame, as a live Range. In a shadow
  // root the document's selection is retargeted to the host, so it is asked
  // for the ranges inside this root: the standard way first, then the older
  // Safari signature, then Chrome's own.
  function readerRange(): Range | null {
    const scope = root?.getRootNode();
    const chosen = document.getSelection();
    if (!scope || !chosen || chosen.rangeCount === 0) return null;
    if (scope instanceof ShadowRoot && "getComposedRanges" in chosen) {
      const composed = chosen as DomSelection & { getComposedRanges: (...args: unknown[]) => StaticRange[] };
      let found: StaticRange | undefined;
      try {
        found = composed.getComposedRanges({ shadowRoots: [scope] })[0];
      } catch {
        found = composed.getComposedRanges(scope)[0];
      }
      if (!found || found.collapsed) return null;
      const live = document.createRange();
      try {
        live.setStart(found.startContainer, found.startOffset);
        live.setEnd(found.endContainer, found.endOffset);
      } catch {
        return null;
      }
      return live.collapsed ? null : live;
    }
    const own = scope instanceof ShadowRoot ? ((scope as ShadowRoot & { getSelection?: () => DomSelection | null }).getSelection?.() ?? chosen) : chosen;
    return own.rangeCount > 0 && !own.isCollapsed ? own.getRangeAt(0) : null;
  }

  // The words the range takes a character of, found by halving, since the
  // page is in document order: from the first whose end is past the range's
  // start to the last whose start is before its end. A range that begins at a
  // word's very end, or finishes at a very start, takes none of that word.
  const atStart = (element: HTMLElement, node: Node, offset: number) =>
    offset === 0 && (node === element || node === element.firstChild);
  const atEnd = (element: HTMLElement, node: Node, offset: number) =>
    (node === element && offset === element.childNodes.length) ||
    (node === element.lastChild && offset === (node.textContent?.length ?? 0));
  function touched(live: Range): [number, number] | null {
    const cell = textCell;
    if (!cell || page.length === 0 || !live.intersectsNode(cell)) return null;
    const firstAfter = (test: (element: HTMLElement) => boolean) => {
      let low = 0;
      let high = page.length;
      while (low < high) {
        const mid = (low + high) >> 1;
        if (test(page[mid]!)) high = mid;
        else low = mid + 1;
      }
      return low;
    };
    try {
      let first = firstAfter((element) => live.comparePoint(element, element.childNodes.length) >= 0);
      let last = firstAfter((element) => live.comparePoint(element, 0) > 0) - 1;
      if (page[first] && atEnd(page[first]!, live.startContainer, live.startOffset)) first += 1;
      if (page[last] && atStart(page[last]!, live.endContainer, live.endOffset)) last -= 1;
      return first <= last ? [first, last] : null;
    } catch {
      return null;
    }
  }

  // Selecting text is one way to make a section, at any width, and on a
  // narrow screen the only one. The selection only ever sets one; letting it
  // go (a tap on the card, on a word) leaves it in place, and Clear or tagging
  // ends it.
  // While the reader's own selection is up on a narrow screen, its handles are
  // the ones to drag; once it is let go, the pins take over.
  let nativeLive = false;
  let selectionTimer: ReturnType<typeof setTimeout> | undefined;
  function onSelectionChange() {
    clearTimeout(selectionTimer);
    selectionTimer = setTimeout(() => {
      const live = readerRange();
      const ends = live && touched(live);
      nativeLive = Boolean(ends);
      if (!marking) return;
      const span = ends && spanForPage(page.map((element) => element.dataset.wordId!), indexById, ends[0], ends[1]);
      if (!span || (selection?.native && selection.from === span.from && selection.to === span.to)) return;
      selection = { ...span, native: true };
    }, 120);
  }

  // The stretch of the meeting on screen: the first and last timed words
  // between the bars and the player, found by halving, since the page runs
  // top to bottom.
  let scroller: HTMLElement | null = null;
  // A sticky element keeps its distance from the scroll area's padding, not
  // from its edge: the sheet's own space below the transcript is taken off
  // again, so the dock stands on the player rather than above that space.
  let scrollerPad = 0;
  let seen: { startMs: number; endMs: number } | null = null;
  let seenFrame = 0;
  function findScroller(): HTMLElement | null {
    for (let node = root?.parentElement; node; node = node.parentElement) {
      if (/(auto|scroll)/.test(getComputedStyle(node).overflowY)) return node;
    }
    return null;
  }
  function measureSeen() {
    seenFrame = 0;
    if (!scroller || page.length === 0) {
      seen = null;
      return;
    }
    const top = scroller.getBoundingClientRect().top + stickTop;
    const firstWhere = (test: (box: DOMRect) => boolean) => {
      let low = 0;
      let high = page.length;
      while (low < high) {
        const mid = (low + high) >> 1;
        if (test(page[mid]!.getBoundingClientRect())) high = mid;
        else low = mid + 1;
      }
      return low;
    };
    let first = firstWhere((box) => box.bottom > top + coverTop);
    let last = firstWhere((box) => box.top >= top + viewHeight - coverBottom) - 1;
    const timed = (at: number) => indexById.get(page[at]?.dataset.wordId ?? "");
    while (first <= last && timed(first) === undefined) first += 1;
    while (last >= first && timed(last) === undefined) last -= 1;
    const one = words[timed(first) ?? -1];
    const two = words[timed(last) ?? -1];
    const next = one && two ? { startMs: Math.min(one.startMs, two.startMs), endMs: Math.max(one.endMs, two.endMs) } : null;
    if (next?.startMs !== seen?.startMs || next?.endMs !== seen?.endMs) seen = next;
  }
  function queueSeen() {
    if (scroller && !seenFrame) seenFrame = requestAnimationFrame(measureSeen);
  }
  $: page, width, viewHeight, coverTop, coverBottom, void tick().then(queueSeen);
  $: if (scroller && width) scrollerPad = parseFloat(getComputedStyle(scroller).paddingBottom) || 0;

  // The page word under a point, or the one a space under it belongs to.
  function pageIndexAt(x: number, y: number): number | undefined {
    const scope = root.getRootNode() as Document | ShadowRoot;
    for (const element of scope.elementsFromPoint(x, y)) {
      if (!(element instanceof HTMLElement) || !textCell.contains(element)) continue;
      const id = element.dataset.wordId ?? element.dataset.gapFor;
      if (id !== undefined) return pageAt.get(id);
    }
    return undefined;
  }

  // A mouse drag across the words selects them. A browser will not begin a
  // selection inside a <button>, and every timed word is one, so a drag that
  // starts on a word does it here, a whole word at a time. A touch goes to the
  // phone's own long press; a plain click still seeks.
  let textDrag: { anchor: number; x: number; y: number; moved: boolean } | null = null;
  function textDown(event: PointerEvent) {
    textDrag = null;
    dragClick = false;
    if (event.pointerType !== "mouse" || event.button !== 0 || event.shiftKey || event.metaKey || event.ctrlKey || event.altKey) return;
    const word = (event.target as Element | null)?.closest?.<HTMLElement>(".cassini-word");
    const anchor = word ? pageAt.get(word.dataset.wordId ?? "") : undefined;
    if (anchor !== undefined) textDrag = { anchor, x: event.clientX, y: event.clientY, moved: false };
  }
  function textMove(event: PointerEvent) {
    if (!textDrag) return;
    if (!(event.buttons & 1)) {
      textDrag = null;
      return;
    }
    if (!textDrag.moved) {
      if (Math.hypot(event.clientX - textDrag.x, event.clientY - textDrag.y) < 5) return;
      textDrag.moved = true;
      textCell.setPointerCapture?.(event.pointerId);
    }
    event.preventDefault();
    const at = pageIndexAt(event.clientX, event.clientY);
    if (at === undefined) return;
    const [start, end] = at >= textDrag.anchor ? [page[textDrag.anchor], page[at]] : [page[at], page[textDrag.anchor]];
    const last = end?.lastChild;
    if (start?.firstChild && last) document.getSelection()?.setBaseAndExtent(start.firstChild, 0, last, last.textContent?.length ?? 0);
  }
  // The release lands on the frame once it holds the pointer, so the word it
  // started on is not also clicked, and nothing seeks.
  function textUp() {
    dragClick = Boolean(textDrag?.moved);
    textDrag = null;
  }

  // A click in the transcript that lands on nothing belonging to the selection
  // lets it go, as a click beside any selection does. Not the click that ends
  // a drag, and not one that has just opened a section: it compares the
  // selection before and after the click went through.
  let dragClick = false;
  let selectionAtClick: Selection | null = null;
  function clickStart() {
    selectionAtClick = selection;
  }
  function clickEnd(event: MouseEvent) {
    if (dragClick) {
      dragClick = false;
      return;
    }
    if (!selection || selection !== selectionAtClick) return;
    const kept = event
      .composedPath()
      .some((node) => node instanceof HTMLElement && node.matches("[data-pin], [data-keep-selection]"));
    if (!kept) void clearSelection();
  }

  // Its start, where its tag and card stand; the arrows carry on from it.
  function selectMark(mark: PlacedMark, go = true) {
    const span = spanForRange(words, mark.startMs, mark.endMs);
    if (span) {
      selection = { ...span, itemId: mark.item.id };
      markAt = (view?.placed ?? []).indexOf(mark);
      if (go) revealWord(span.from, "center", glide());
    }
  }

  // Which tagged section the arrows last went to, or was last opened. It is
  // the reader's place among them only while it is open or on screen: once
  // they have scrolled away the count goes back to the plain total, and the
  // arrows go from what is on screen instead. Just after a step it counts as
  // on screen, while the glide to it is still under way.
  let markAt = -1;
  let steppedAt = 0;
  $: shownAt = placeShown(markAt, seen, selection?.itemId, view?.placed ?? []);
  function placeShown(at: number, onScreen: typeof seen, openId: string | undefined, placed: readonly PlacedMark[]) {
    const mark = placed[at];
    if (!mark) return -1;
    if (mark.item.id === openId || performance.now() - steppedAt < 1500) return at;
    return onScreen && mark.startMs < onScreen.endMs && mark.endMs > onScreen.startMs ? at : -1;
  }

  function stepMark(delta: 1 | -1) {
    const placed = view?.placed ?? [];
    if (placed.length === 0) return;
    if (shownAt >= 0) {
      markAt = (shownAt + delta + placed.length) % placed.length;
    } else {
      // The sections are in time order: the next is the first starting at or
      // after the top of the screen, the previous the last starting before it.
      const top = seen?.startMs ?? 0;
      const next = placed.findIndex((mark) => mark.startMs >= top);
      const before = placed.filter((mark) => mark.startMs < top).length - 1;
      markAt = delta > 0 ? Math.max(0, next) : before >= 0 ? before : placed.length - 1;
    }
    steppedAt = performance.now();
    const mark = placed[markAt]!;
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
    const written = await session.write(request, "stretch");
    if (written) void clearSelection();
    return written;
  }

  // The toolbar and the pins go with the selection; focus left with nowhere to be goes to the rail.
  async function clearSelection() {
    if (selection?.native) document.getSelection()?.removeAllRanges();
    selection = null;
    await tick();
    const active = (root.getRootNode() as Document | ShadowRoot).activeElement;
    if (!active || active === document.body) rail?.focus();
  }
  async function tagStretch(event: CustomEvent<TagPick>) {
    if (range && (await write(markRequest(event.detail, timeRange(range.startMs, range.endMs))))) recent = event.detail;
  }
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
      else return;
      event.preventDefault();
    } else if (event.key === "Enter" && selection) {
      // A word is a button, and the one a drag began on keeps the focus; its
      // own Enter would seek, where the reader means the selection.
      const inText = path.some(
        (node) => node instanceof HTMLElement && (node.hasAttribute("data-pin") || node.classList.contains("cassini-word")),
      );
      if (inText || !keyboardEventTargetsControl(event)) {
        event.preventDefault();
        stretchToolbar?.confirm();
      }
    }
  }

  onMount(() => {
    window.addEventListener("keydown", onKeydown, true);
    document.addEventListener("selectionchange", onSelectionChange);
    scroller = findScroller();
    if (scroller) scrollerPad = parseFloat(getComputedStyle(scroller).paddingBottom) || 0;
    scroller?.addEventListener("scroll", queueSeen, { passive: true });
    queueSeen();
    return () => {
      window.removeEventListener("keydown", onKeydown, true);
      document.removeEventListener("selectionchange", onSelectionChange);
      scroller?.removeEventListener("scroll", queueSeen);
      cancelAnimationFrame(seenFrame);
      clearTimeout(selectionTimer);
    };
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
    class="tf-bar sticky z-10 grid gap-2 bg-base-200 py-2"
    style:top="{Math.max(0, stickTop - 1)}px"
    style:margin-inline="calc(-1 * var(--tf-bleed, 8px))"
    style:padding-inline="var(--tf-bleed, 8px)"
  >
    <TranscriptToolbar
      bind:this={toolbar}
      bind:query
      bind:onlyMatching
      stops={stops.length}
      current={stop}
      on:step={(event) => step(event.detail)}
    />
  </div>

  {#if marking && wide}
    <!-- Sticky under the search, floating over the tag column: what is tagged
         in this transcript is a different subject from what was typed into it,
         and it stays reachable while reading, which is the point of stepping
         through it. -->
    <div
      bind:offsetHeight={tagbarHeight}
      class="tf-tagbar relative sticky z-10 flex flex-wrap items-center gap-2"
      style:top="{Math.max(0, stickTop - 1) + barHeight}px"
      style:margin-inline="calc(-1 * var(--tf-bleed, 8px))"
      style:padding-inline="var(--tf-bleed, 8px)"
      style:--tf-tags-left="{tagsLeft - 10}px"
      style:--tf-tag-column="{tagColumn}px"
    >
      <div class="tf-tagbar-inner flex items-center gap-2">
        <SectionsNav count={view?.placed.length ?? 0} current={shownAt} on:step={(event) => stepMark(event.detail)} />
      </div>
    </div>
  {/if}

  <div
    class="tf-text mt-6 grid"
    style:grid-template-columns={marking
      ? wide
        ? `68px minmax(0,1fr) ${tagColumn}px`
        : `${RAIL_NARROW}px minmax(0,1fr)`
      : "minmax(0,1fr)"}
    style:--sel="var(--tag-bg)"
    style:--sel-edge="var(--tag)"
    style:--sel-fill="color-mix(in oklch, var(--tag) 28%, transparent)"
    data-tag-color={selColor}
    data-sel-fill={selection ? "" : undefined}
    role="presentation"
    on:click|capture={clickStart}
    on:click={clickEnd}
  >
    {#if marking}
      <div data-keep-selection>
        <!-- Between the sticky bars (the tag bar has no height of its own on a
             wide screen, and on a narrow one is a dock above the player), as
             tall as the space they leave and no taller, and never taller than
             the transcript: a short one is not stretched to match a screen. -->
        <div
          class="sticky"
          style:top="{stickTop + coverTop + 12}px"
          style:height="{Math.max(0, Math.min(viewHeight - coverTop - coverBottom - 24, textHeight))}px"
        >
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
            visible={seen}
            on:grab={(event) => grab(event.detail.aMs, event.detail.bMs, event.detail.handle)}
            on:pick={(event) => pickTurn(event.detail)}
            on:select={(event) => selectMark(event.detail)}
            on:go={(event) => revealWord(spanForDrag(words, event.detail, event.detail)?.from, "center", glide())}
          />
        </div>
      </div>
    {/if}
    <!-- svelte-ignore a11y_no_static_element_interactions a11y_click_events_have_key_events -->
    <div
      bind:this={textCell}
      bind:clientHeight={textHeight}
      class="relative min-w-0 self-start"
      data-tag-color={hoverMark?.color ?? selColor}
      style:--lit-edge="var(--tag)"
      style:--lit-fill="color-mix(in oklch, var(--tag) 28%, transparent)"
      on:pointerdown={textDown}
      on:pointermove={textMove}
      on:pointerup={textUp}
      on:pointercancel={textUp}
      on:click={textClick}
    >
      <slot {chips} openMark={selectMark} />
      {#if marking && pins && range && !(nativeLive && !wide)}
        {#each EDGES as edge (edge)}
          {@const ms = edge === "from" ? range.startMs : range.endMs}
          <span
            class="absolute -ml-px w-0 cursor-grab touch-none border-l-2 border-(--sel-edge) outline-none before:absolute before:content-[''] after:absolute after:rounded-full after:bg-(--sel-edge) after:content-[''] focus-visible:after:ring-3 focus-visible:after:ring-(--sel) {wide
              ? 'before:-inset-x-2.5 before:-inset-y-1.5 after:-left-1.5 after:size-2.5'
              : 'before:-inset-x-4 before:-inset-y-3 after:-left-2 after:size-3.5'} {edge === 'from'
              ? wide ? 'after:-top-2' : 'after:-top-3'
              : wide ? 'after:-bottom-2' : 'after:-bottom-3'}"
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
              class="tf-pin-time pointer-events-none absolute left-[7px] px-1.5 py-[2px] font-mono text-[10.5px] leading-[1.2] whitespace-nowrap {edge === 'from' ? 'bottom-[calc(100%+4px)]' : 'top-[calc(100%+4px)]'}"
              >{formatPreciseTime(ms)}</span
            ></span
          >
        {/each}
      {/if}
    </div>
    {#if marking && wide}
      <!-- Inset, so the brackets and their tags sit clear of the floating
           tagged-sections controls above them. -->
      <div class="relative pl-2.5" data-keep-selection>
        <MarkBrackets
          {brackets}
          selectedId={selection?.itemId}
          bind:hoverId
          stickTop={Math.max(0, stickTop - 1) + barHeight + 44}
          on:select={(event) => selectMark(event.detail)}
        />
        {#if stretchProps && wide}
          <!-- Under that section's own tag, which never moves: the chip stays
               exactly where it was and the card opens beneath it. A new
               selection has no tag yet, so its card stands level with the
               selection's last line, where the pointer let go. -->
          <div
            class="absolute"
            style:top="{selectedBracket ? selectedBracket.top + cardDrop : pins ? pins.to.y : 0}px"
            style:height={selectedBracket ? `${Math.max(0, selectedBracket.height - cardDrop)}px` : undefined}
            style:left="{selectedBracket ? tagsLeft - 10 : 0}px"
            style:right={selectedBracket ? "auto" : "0"}
          >
            <!-- Rides its own section, like the tag it replaced: it holds the
                 line under the bars while any of that section is on screen. -->
            <div
              class="sticky"
              style:top="{selectedBracket ? Math.max(0, stickTop - 1) + barHeight + 44 + cardDrop : 0}px"
            >
              <StretchToolbar bind:this={stretchToolbar} {...stretchProps} on:tag={tagStretch} on:save={saveMove} on:remove={removeMark} on:clear={clearSelection} />
            </div>
          </div>
        {/if}
      </div>
    {/if}
  </div>
  {#if marking && !wide}
    <!-- Where the screen is narrow, the tagged sections sit in a card of their
         own above the player, over the text and clear of the rail beside it,
         its right edge on the player's controls' (the footer's 8px, the
         card's border and its 8px): in reach of a thumb, clear of the
         phone's own menu over a selection, and over the text rather than in
         its flow, so opening a section does not move what is being read. The
         frame keeps as much room below its last line, and the dock ends in it,
         so at the foot of the transcript nothing is left under it. Its surface
         is the player's card, classes and all: in Nextcloud the border comes
         from a rule on .card. -->
    <div aria-hidden="true" style:height="{coverBottom}px"></div>
    <div class="sticky z-20 h-0" style:bottom="{stickBottom - scrollerPad}px">
      <div
        bind:offsetHeight={dockHeight}
        class="tf-dock card absolute bottom-0 grid gap-2 border border-base-300 bg-base-100 p-2 shadow-2xl"
        style:left="{RAIL_NARROW}px"
        style:right="calc(17px - var(--tf-bleed, 8px))"
        role="group"
        aria-label="Tagged sections"
      >
        {#if stretchProps}
          <StretchToolbar row bind:this={stretchToolbar} {...stretchProps} on:tag={tagStretch} on:save={saveMove} on:remove={removeMark} on:clear={clearSelection} />
          <hr class="border-base-300" />
        {/if}
        <div class="flex items-center gap-2 px-1">
          <SectionsNav large count={view?.placed.length ?? 0} current={shownAt} on:step={(event) => stepMark(event.detail)} />
        </div>
      </div>
    </div>
  {/if}
</div>

<style>
  /* The pin's own time, floating over the words it sits between: it needs to
     be legible against whatever is behind it, so it carries a surface of its
     own, in the handle's colour with the time reversed out of it, so it reads
     as part of the handle it names. */
  .tf-pin-time {
    color: var(--color-base-100);
    background-color: var(--sel-edge);
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
  /* Wherever the transcript has its tag column (the bar is only drawn then),
     this is a small floating cluster over that column rather than a second
     full-width strip: the transcript is what the screen is for, and these
     controls are consulted rather than read. It follows the column, not the
     screen, so there is no width at which the column is there and the strip
     comes back. */
  .tf-tagbar {
    height: 0;
    padding-block: 0;
    overflow: visible;
  }
  .tf-tagbar-inner {
    position: absolute;
    /* The bar bleeds past the frame on both sides, and the tag column is the
       frame's last --tf-tag-column; a tag starts --tf-tags-left into it.
       These controls span from there to the frame's edge, as the tags do.
       They sit on the sheet's own ground, so the text scrolling under them
       passes behind rather than through; the box reaches 6px past them on
       each side, so what is in it stays where it was. */
    top: 4px;
    left: calc(100% - var(--tf-bleed, 8px) - var(--tf-tag-column, 196px) + var(--tf-tags-left, 0px) - 6px);
    right: calc(var(--tf-bleed, 8px) - 6px);
    padding: 4px 6px;
    flex-wrap: wrap;
    row-gap: 6px;
    background-color: var(--color-base-200);
    border-radius: 9px;
  }
  /* The count reads from the tag's edge, the arrows hold the far one. */
  .tf-tagbar-inner :global(.tf-marks-group) {
    margin-left: auto;
  }
  .tf-dock :global(.tf-marks-group) {
    margin-left: auto;
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
  /* A selection looks the same before and after the reader lets go of it:
     the browser's highlight here is this fill, and the words keep the fill,
     under their rule, once the highlight is gone. An open section, and one a
     pointer is on its bracket or tag, take a fill too, since every section
     already carries a rule at rest. */
  .frame :global(.tf-text *::selection) {
    background-color: var(--sel-fill);
  }
  /* The playing word is inverted, and a selection over it laid its fill on the
     light ground while leaving the text light: it keeps its own two colours. */
  .frame :global(.tf-text [data-active="true"]::selection) {
    color: var(--color-base-100);
    background-color: var(--color-base-content);
  }
  .frame :global([data-sel-fill] [data-word-id][data-sel]:not([data-active="true"])),
  .frame :global([data-word-id][data-lit]:not([data-sel], [data-active="true"])) {
    background-image: linear-gradient(var(--hl-edge), var(--hl-edge)), linear-gradient(var(--hl-fill), var(--hl-fill));
    background-size: 100% 2px, 100% 100%;
    background-position: 0 100%, 0 0;
    background-repeat: no-repeat, no-repeat;
  }
  /* The run-on over the next space would lie under a comma or a full stop that
     follows with no space, and that mark's own fill on top made it twice as
     strong; there the fill stops at the word, and only the rule runs on. */
  .frame :global([data-sel-fill] [data-word-id][data-sel]:has(+ [data-word-id]):not([data-active="true"])),
  .frame :global([data-word-id][data-lit]:has(+ [data-word-id]):not([data-sel], [data-active="true"])) {
    background-size: 100% 2px, calc(100% - 0.27em) 100%;
  }
  /* A tagged section at rest: its rule, a step quieter than when it is open,
     mixed with the ground rather than made transparent so it is no stronger
     where a comma's box meets a word's. */
  .frame :global([data-word-id][data-rest]:not([data-sel], [data-lit], [data-find], [data-active="true"])) {
    --rest-edge: color-mix(in oklch, var(--tag) 70%, var(--color-base-200));
    border-radius: 0;
    background: linear-gradient(var(--rest-edge), var(--rest-edge)) no-repeat 0 100% / 100% 2px;
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
  /* A word carries its own section's colour at rest, so the open and lit
     colours come from above it: the selection's from the grid, the lit
     section's from the text cell. */
  .frame :global([data-word-id][data-sel]) {
    --hl-edge: var(--sel-edge);
    --hl-fill: var(--sel-fill);
  }
  .frame :global([data-word-id][data-lit]) {
    --hl-edge: var(--lit-edge);
    --hl-fill: var(--lit-fill);
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
