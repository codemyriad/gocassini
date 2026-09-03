<script lang="ts">
  import { createEventDispatcher } from "svelte";
  import { Copy, Download, FileText, TriangleAlert, X } from "@lucide/svelte";
  import type { MeetingCatalogEntry } from "../viewer/catalog";
  import {
    formatSelectionWordCount,
    formatWordCount,
    lacksPortableAudio,
    type SelectionTotals,
  } from "../viewer/selectionModel";

  // Prepare (D-626): the review step between picking meetings and handing them
  // to an agent or a person. It shows what was picked, how much text that is,
  // and — the point of the panel — what the bundle will NOT contain, stated
  // here rather than discovered downstream.
  //
  // Every disclosure comes from the catalog (D-716), never from fetching a
  // recording: the panel must be able to describe a selection before anything
  // has been assembled, and a panel that downloaded five `.opus` files to
  // answer a yes/no question would be unusable on the archives that need it.

  // In pick order — the order the bundle prints in.
  export let entries: MeetingCatalogEntry[] = [];
  export let totals: SelectionTotals;
  // Sentences from selectionModel.describeSelectionGaps. Empty when the
  // selection has nothing missing, and the section disappears with it: a
  // reassuring "no gaps" note would be a claim of its own.
  export let gaps: string[] = [];
  // Assembles the bundle. A function rather than the provider itself so the
  // panel stays ignorant of the data seam: the shell decides which meetings and
  // which implementation, and the panel only knows there are bytes at the end
  // of it. It is not called until Copy or Download is pressed.
  export let loadBundle: () => Promise<string>;

  const dispatch = createEventDispatcher<{ close: void }>();

  type StatusTone = "ok" | "warn" | "error";
  let status: { tone: StatusTone; text: string } | null = null;
  let busy = false;

  // The assembled bytes, kept for the second action. Copy and Download hand
  // over exactly the same document, so pressing both must not risk asking twice
  // and getting two answers — and a second press is instant.
  let bundleText: string | null = null;
  let bundleKey = "";

  const WORD_COUNT_FORMAT = new Intl.NumberFormat("en-GB");

  // The picked set, as one string. When it changes — the list is still live
  // behind this panel — the cached bytes describe a set nobody asked for any
  // more, and so does "Copied 12,000 characters".
  $: selectionKey = entries.map((entry) => entry.id).join("\n");
  $: if (selectionKey !== bundleKey) {
    bundleText = null;
    status = null;
  }

  $: downloadName = `cassini-context-${new Date().toISOString().slice(0, 10)}.md`;

  async function ensureBundle(): Promise<string> {
    if (bundleText !== null && bundleKey === selectionKey) {
      return bundleText;
    }
    const text = await loadBundle();
    bundleText = text;
    bundleKey = selectionKey;
    return text;
  }

  function describeError(error: unknown): string {
    return error instanceof Error ? error.message : String(error);
  }

  async function handleCopy() {
    busy = true;
    status = { tone: "ok", text: "Preparing…" };
    // The clipboard is not available everywhere this runs: the embedded app
    // lives in a shadow root inside Nextcloud, an insecure-origin deployment
    // has no navigator.clipboard at all, and a browser may simply refuse. Each
    // one gets said out loud and names the way through, because a Copy that
    // silently did nothing would look like a bundle that came out empty.
    const clipboard = navigator.clipboard;
    if (!clipboard || typeof clipboard.writeText !== "function") {
      status = { tone: "warn", text: "Clipboard unavailable here — use Download." };
      busy = false;
      return;
    }
    // Started, not awaited. Safari spends the click's activation on the first
    // await, and the bundle is assembled by the operator — one round trip, over
    // recordings — so a Copy that waited for it before reaching the clipboard
    // would be refused on the first press every time, and the reader would be
    // sent to Download for a browser that can copy perfectly well. Handing the
    // ClipboardItem the promise keeps the write inside the gesture.
    const pending = ensureBundle();
    if (typeof ClipboardItem === "function" && typeof clipboard.write === "function") {
      try {
        await clipboard.write([
          new ClipboardItem({
            "text/plain": pending.then((bundle) => new Blob([bundle], { type: "text/plain" })),
          }),
        ]);
        const copied = await pending;
        status = {
          tone: "ok",
          text: `Copied ${WORD_COUNT_FORMAT.format(copied.length)} characters.`,
        };
        busy = false;
        return;
      } catch {
        // Either the bundle failed, which the await below reports with its own
        // error, or this browser will not take a promise it has to wait on.
        // Both fall through to the plain write.
      }
    }
    let text: string;
    try {
      text = await pending;
    } catch (error) {
      status = { tone: "error", text: describeError(error) };
      busy = false;
      return;
    }
    try {
      await clipboard.writeText(text);
      status = {
        tone: "ok",
        text: `Copied ${WORD_COUNT_FORMAT.format(text.length)} characters.`,
      };
    } catch {
      // The bytes are assembled and cached by now, so a second press writes
      // them inside its own gesture — which is exactly what a browser that
      // refused this one is asking for. Download stays the way out.
      status = { tone: "warn", text: "Clipboard blocked here — press Copy again, or use Download." };
    } finally {
      busy = false;
    }
  }

  async function handleDownload() {
    busy = true;
    status = { tone: "ok", text: "Preparing…" };
    try {
      const text = await ensureBundle();
      const url = URL.createObjectURL(new Blob([text], { type: "text/markdown;charset=utf-8" }));
      const anchor = document.createElement("a");
      anchor.href = url;
      anchor.download = downloadName;
      // In the host document, not the shadow root: the download is a navigation
      // the page performs, and an anchor inside a shadow tree does not reliably
      // get one.
      document.body.appendChild(anchor);
      anchor.click();
      anchor.remove();
      // After the click has been dispatched, not during it — revoking the URL
      // in the same task cancels the save in some browsers.
      setTimeout(() => URL.revokeObjectURL(url), 0);
      status = { tone: "ok", text: `Downloaded ${downloadName}.` };
    } catch (error) {
      status = { tone: "error", text: describeError(error) };
    } finally {
      busy = false;
    }
  }
</script>

<!-- A drawer over the list, like the meeting sheet but narrower: this is a
     review step, not a reading surface. Positioned against the browse shell
     rather than the viewport for the same reason the sheet is — see App.svelte. -->
<aside class="prepare-panel" aria-label="Prepare context">
  <header class="prep-head">
    <h2>Prepare context</h2>
    <button type="button" on:click={() => dispatch("close")} aria-label="Close Prepare">
      <X size={16} aria-hidden="true" />
    </button>
  </header>

  <div class="prep-body">
    <!-- 1. What was picked. -->
    <section class="prep-section">
      <h3 class="prep-eyebrow">In this bundle</h3>
      <ul class="prep-list">
        {#each entries as entry (entry.id)}
          <li class="prep-row">
            <!-- A transcript, not a camera: what this hands over is text. -->
            <FileText size={14} aria-hidden="true" />
            <span class="prep-row-title">{entry.title}</span>
            <span class="prep-row-tail">
              <!-- The gap sentence below counts these; this is where the count
                   turns back into a meeting you can unpick. -->
              {#if lacksPortableAudio(entry)}
                <span class="prep-row-flag">
                  <TriangleAlert size={12} aria-hidden="true" />
                  Blocks Prepare
                </span>
              {/if}
              <span class="prep-row-words">{formatWordCount(entry.wordCount)}</span>
            </span>
          </li>
        {/each}
      </ul>
      <p class="prep-total">
        <span>Total</span>
        <span>{formatSelectionWordCount(totals)}</span>
      </p>
    </section>

    <!-- 2. What it will not contain. -->
    {#if gaps.length > 0}
      <section class="prep-section prep-gaps">
        {#each gaps as gap}
          <p class="prep-gap">
            <TriangleAlert size={14} aria-hidden="true" />
            <span>{gap}</span>
          </p>
        {/each}
      </section>
    {/if}

    <!-- Two equal outputs directly under the set: the same bytes either way,
         and the way out on a deployment with no model configured at all. -->
    <section class="prep-section prep-actions">
      <button type="button" class="prep-action" disabled={busy} on:click={handleCopy}>
        <Copy size={14} aria-hidden="true" />
        Copy
      </button>
      <button type="button" class="prep-action" disabled={busy} on:click={handleDownload}>
        <Download size={14} aria-hidden="true" />
        Download
      </button>
    </section>
    <p class="prep-status" data-tone={status?.tone ?? "ok"} role="status">
      {status?.text ?? ""}
    </p>

    <!-- 3. Whether this deployment can be asked a question at all (D-722).
         Empty in every build that has no operator to ask — a standalone export
         says nothing here rather than claiming "not configured", which it has
         no way to know. The shell fills it: the sentence, who may act on it and
         where the fix lives are all things only the shell knows, and a viewer
         that decided them would be a second answer to drift from /setup. -->
    <slot name="readiness" />

    <!-- 4. The in-app Generate card lands here (D-700). It is a section of this
         panel rather than a screen of its own, because asking a question of
         these meetings is the next thing you do with the set you just reviewed
         — so the slot exists now and the layout does not have to be rebuilt
         around it later. -->
    <slot name="generate" />
  </div>
</aside>

<style>
  .prepare-panel {
    display: flex;
    flex-direction: column;
    height: 100%;
    background-color: var(--color-base-100);
  }

  .prep-head {
    display: flex;
    flex: none;
    align-items: center;
    justify-content: space-between;
    gap: 0.75rem;
    padding: 1rem 1.25rem;
    border-bottom: 1px solid var(--color-base-300);
  }
  .prep-head h2 {
    font-size: 0.9375rem;
    font-weight: 650;
    color: var(--color-base-content);
  }
  .prep-head button {
    display: inline-flex;
    flex: none;
    padding: 4px;
    cursor: pointer;
    background: none;
    border: 0;
    border-radius: var(--radius-field, 0.5rem);
    color: color-mix(in oklch, var(--color-base-content) 65%, transparent);
  }
  .prep-head button:hover {
    background-color: var(--color-base-200);
    color: var(--color-base-content);
  }

  .prep-body {
    flex: 1;
    min-height: 0;
    overflow-y: auto;
    overscroll-behavior: contain;
    padding: 1rem 1.25rem 1.5rem;
  }

  .prep-section {
    margin-bottom: 1.25rem;
  }

  .prep-eyebrow {
    margin-bottom: 0.5rem;
    font-size: 10px;
    font-weight: 600;
    line-height: 1;
    letter-spacing: 0.09em;
    text-transform: uppercase;
    color: color-mix(in oklch, var(--color-base-content) 55%, transparent);
  }

  .prep-list {
    display: flex;
    flex-direction: column;
    list-style: none;
    margin: 0;
    padding: 0;
  }
  .prep-row {
    display: grid;
    grid-template-columns: auto minmax(0, 1fr) auto;
    align-items: center;
    gap: 0.5rem;
    padding: 7px 0;
    border-bottom: 1px solid var(--color-base-300);
    font-size: 0.8125rem;
    color: var(--color-base-content);
  }
  .prep-row-title {
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .prep-row-tail {
    display: flex;
    flex: none;
    align-items: center;
    gap: 0.5rem;
  }
  .prep-row-words {
    flex: none;
    font-size: 0.75rem;
    font-variant-numeric: tabular-nums;
    color: color-mix(in oklch, var(--color-base-content) 55%, transparent);
  }
  /* The warning colour of the gap sentence that counts these rows, so the two
     read as one statement rather than two unrelated warnings. */
  .prep-row-flag {
    display: inline-flex;
    flex: none;
    align-items: center;
    gap: 0.25rem;
    font-size: 0.6875rem;
    color: var(--color-warning, #b45309);
  }

  .prep-total {
    display: flex;
    align-items: center;
    justify-content: space-between;
    padding-top: 0.625rem;
    font-size: 0.8125rem;
    font-weight: 650;
    font-variant-numeric: tabular-nums;
    color: var(--color-base-content);
  }

  .prep-gaps {
    display: flex;
    flex-direction: column;
    gap: 0.5rem;
  }
  /* An unfilled callout with a warning rule: these are statements about the
     bundle rather than errors this panel has hit. Most leave the set perfectly
     preparable; the one that does not (a meeting with no single-file recording)
     says so in its own words and marks the row to unpick. */
  .prep-gap {
    display: grid;
    grid-template-columns: auto minmax(0, 1fr);
    gap: 0.5rem;
    padding: 0.5rem 0.625rem;
    border-left: 3px solid var(--color-warning, #d97706);
    background-color: color-mix(in oklch, var(--color-warning, #d97706) 10%, transparent);
    font-size: 0.75rem;
    line-height: 1.45;
    color: var(--color-base-content);
  }

  .prep-actions {
    display: grid;
    grid-template-columns: 1fr 1fr;
    gap: 0.5rem;
    margin-bottom: 0.5rem;
  }
  .prep-action {
    display: inline-flex;
    align-items: center;
    justify-content: center;
    gap: 0.375rem;
    padding: 8px 12px;
    cursor: pointer;
    background-color: var(--color-base-100);
    border: 1px solid var(--color-base-300);
    border-radius: var(--radius-field, 0.5rem);
    font-size: 0.8125rem;
    font-weight: 550;
    color: var(--color-base-content);
  }
  .prep-action:hover:not(:disabled) {
    border-color: var(--color-primary);
    color: var(--color-primary);
  }
  .prep-action:disabled {
    cursor: progress;
    opacity: 0.6;
  }

  /* Reserved whether or not there is anything to say, so a status line arriving
     does not shove the actions up under the pointer. */
  .prep-status {
    min-height: 18px;
    margin-bottom: 1.25rem;
    font-size: 0.75rem;
    color: color-mix(in oklch, var(--color-base-content) 65%, transparent);
  }
  .prep-status[data-tone="warn"] {
    color: var(--color-warning, #b45309);
  }
  .prep-status[data-tone="error"] {
    color: var(--color-error, #b91c1c);
  }
</style>
