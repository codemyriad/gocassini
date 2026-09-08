import type { JudgedDisplaySegment } from "./transcript";
import type { DisplayTranscriptToken, DisplayTranscriptTokenAlignment } from "./types";

/** Visible prose and its optional seek target, independent of acoustic evidence. */
export interface TranscriptWordPart {
  id: string;
  text: string;
  prefix: string;
  startMs?: number;
  endMs?: number;
  alignment?: DisplayTranscriptTokenAlignment;
  sourceWordsRejected?: boolean;
}

function timing(startMs: number | undefined, endMs: number | undefined) {
  return Number.isFinite(startMs) && Number.isFinite(endMs) && endMs! >= startMs!
    ? { startMs, endMs }
    : {};
}

/** Legacy generated tokens must not silently drop characters from readable prose. */
export function tokensPreserveText(text: string, tokens: readonly DisplayTranscriptToken[]): boolean {
  return text === tokens.map((token) => (token.spaceBefore ? " " : "") + token.text).join("");
}

/**
 * Display tokens own the copy; canonical words are NOT a replacement for a
 * cleaned sentence. Tokens may legitimately be rewritten, untimed, or carry
 * rejected acoustic references. Their display timing still supports seeking.
 *
 * Legacy artifacts without display tokens get word controls only if every
 * canonical word can be located in order without changing visible prose.
 * Punctuation/whitespace are kept literally, including non-Latin scripts.
 * A mismatch leaves the complete original passage readable and untimed.
 */
export function transcriptWordParts(block: JudgedDisplaySegment): TranscriptWordPart[] {
  if (block.tokens.length) {
    return block.tokens.map((token, index) => ({
      id: `${block.id}:token:${index}`,
      text: token.text,
      prefix: token.spaceBefore ? " " : "",
      ...(token.kind === "word" ? timing(token.startMs, token.endMs) : {}),
      alignment: token.alignment,
      sourceWordsRejected: "sourceWordsRejected" in token && token.sourceWordsRejected === true,
    }));
  }

  const fallback = [{ id: `${block.id}:text`, text: block.text, prefix: "" }];
  if (!block.words.length) return fallback;
  const parts: TranscriptWordPart[] = [];
  let cursor = 0;
  for (const [index, word] of block.words.entries()) {
    const text = word.text.trim();
    if (!text) return fallback;
    const start = block.text.indexOf(text, cursor);
    const prefix = block.text.slice(cursor, start);
    if (start < 0 || /[\p{L}\p{N}\p{M}]/u.test(prefix)) return fallback;
    parts.push({
      id: `${block.id}:word:${index}`,
      text,
      prefix,
      ...timing(word.startMs, word.endMs),
      alignment: "source",
    });
    cursor = start + text.length;
  }
  const suffix = block.text.slice(cursor);
  if (/[\p{L}\p{N}\p{M}]/u.test(suffix)) return fallback;
  if (suffix) parts.push({ id: `${block.id}:suffix`, text: suffix, prefix: "" });
  return parts;
}

/** Space belongs to focused controls, also when the window sees a shadow host. */
export function keyboardEventTargetsControl(event: Pick<KeyboardEvent, "composedPath">): boolean {
  return event.composedPath().some((target) =>
    target instanceof HTMLElement &&
    (target.isContentEditable || target.matches(
      "button, a[href], input, textarea, select, summary, audio[controls], video[controls], " +
      "[role='button'], [role='link'], [role='switch'], [role='checkbox'], [role='radio'], " +
      "[role='slider'], [role='tab'], [role='menuitem'], [role='option'], [role='combobox']",
    )),
  );
}
