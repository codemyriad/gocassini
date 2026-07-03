import { cpSync, existsSync, mkdirSync, readdirSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { execFileSync } from "node:child_process";
import { gunzipSync } from "node:zlib";
import { dirname, join, resolve, extname } from "node:path";
import { fileURLToPath } from "node:url";

const CATALOG_VERSION = "cassini.viewer.catalog.v1";

const scriptDir = dirname(fileURLToPath(import.meta.url));
const viewerDir = resolve(scriptDir, "..");
const distDir = resolve(viewerDir, "dist");
const defaultSourceDir = resolve(viewerDir, "public", "demo");
const defaultOutputDir = resolve(viewerDir, "exports", "static-meetings");

if (process.argv[1] && resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  main();
}

export function main(argv = process.argv.slice(2)) {
  const { outputDir, sourceDir, recordingsBaseUrl } = parseArgs(argv);
  const distIndexPath = join(distDir, "index.html");

  if (!existsSync(distIndexPath)) {
    throw new Error(`Missing ${distIndexPath}. Run "npm run build" first.`);
  }
  if (!existsSync(sourceDir)) {
    throw new Error(
      `Missing meeting source directory: ${sourceDir}. Pass --source-dir <artifact-root> when artifacts are stored outside this repo.`,
    );
  }

  const sourceEntries = readdirSync(sourceDir, { withFileTypes: true });
  const selected = [];
  const seen = new Set();
  const byName = (a, b) => a.name.localeCompare(b.name);

  for (const entry of sourceEntries.sort(byName)) {
    if (entry.name.startsWith(".")) {
      continue;
    }
    if (entry.isDirectory()) {
      if (seen.has(entry.name)) {
        continue;
      }
      selected.push({
        meetingId: entry.name,
        sourcePath: join(sourceDir, entry.name),
        sourceType: "directory",
      });
      seen.add(entry.name);
      continue;
    }

    if (entry.isFile() && isPortableMeeting(entry.name)) {
      const meetingId = entry.name.slice(0, -extname(entry.name).length) || "meeting";
      if (seen.has(meetingId)) {
        continue;
      }
      selected.push({
        meetingId,
        sourcePath: join(sourceDir, entry.name),
        sourceType: "portable",
      });
      seen.add(meetingId);
    }
  }

  if (selected.length === 0) {
    throw new Error(`No meeting directories or .opus files found in ${sourceDir}.`);
  }

  rmSync(outputDir, { recursive: true, force: true });
  mkdirSync(outputDir, { recursive: true });
  mkdirSync(join(outputDir, "meetings"), { recursive: true });

  const builtIndexHtml = readFileSync(distIndexPath, "utf8");
  writeFileSync(join(outputDir, "index.html"), rewriteIndexHtmlForCatalog(builtIndexHtml), "utf8");
  cpSync(join(distDir, "assets"), join(outputDir, "assets"), { recursive: true });

  const meetings = selected.map(({ meetingId, sourcePath, sourceType }) =>
    exportMeeting({
      meetingId,
      sourcePath,
      sourceType,
      outputDir,
      recordingsBaseUrl,
    }),
  );

  writeFileSync(
    join(outputDir, "catalog.json"),
    `${JSON.stringify({ version: CATALOG_VERSION, meetings }, null, 2)}\n`,
    "utf8",
  );

  console.log(`viewer index -> ${join(outputDir, "index.html")}`);
  console.log(`viewer catalog -> ${join(outputDir, "catalog.json")}`);
  for (const meeting of meetings) {
    const meetingRef = meeting.audioPath ? `${meeting.id}.opus` : meeting.id;
    console.log(`${meeting.id} -> ${join(outputDir, "meetings", meetingRef)}`);
  }
}

export function parseArgs(argv) {
  let outputDir = defaultOutputDir;
  let sourceDir = defaultSourceDir;
  let recordingsBaseUrl = null;
  for (let index = 0; index < argv.length; index += 1) {
    if (argv[index] === "--output-dir") {
      const next = argv[index + 1];
      if (!next) {
        throw new Error("missing value for --output-dir");
      }
      outputDir = resolve(viewerDir, next);
      index += 1;
      continue;
    }
    if (argv[index] === "--source-dir") {
      const next = argv[index + 1];
      if (!next) {
        throw new Error("missing value for --source-dir");
      }
      sourceDir = resolve(viewerDir, next);
      index += 1;
      continue;
    }
    if (argv[index] === "--recordings-base-url") {
      const next = argv[index + 1];
      if (!next) {
        throw new Error("missing value for --recordings-base-url");
      }
      recordingsBaseUrl = next.endsWith("/") ? next : `${next}/`;
      index += 1;
    }
  }
  return { outputDir, sourceDir, recordingsBaseUrl };
}

export function rewriteIndexHtmlForCatalog(indexHtml) {
  // Vite is configured with base: "./" so asset paths are already relative.
  // This function is kept for compatibility but performs no transformation.
  return indexHtml;
}

// In v2 portable meetings the transcript items live in separate
// CASSINI_TX_<id>_PAYLOAD_* chunk sets, not the main manifest, so
// extractPortableManifest() (which decodes only the main manifest) returns no
// inline transcript.items — buildTranscriptWordsFromPortable() then yields zero
// segments and the catalog would ship segmentCount: 0 for every fresh v2 .opus.
// The per-item count is mirrored as `wordCount` on each transcript entry
// (wordCount === len(items) === segmentCount for cassini.words.v1 bodies, see
// portable_meeting_v2.go), so fall back to the default transcript's wordCount
// without having to decode the chunk-set body here.
export function portableDefaultSegmentCount(portable) {
  const entries = Array.isArray(portable?.transcripts) ? portable.transcripts : [];
  if (entries.length === 0) return 0;
  const def = entries.find((entry) => entry?.default) ?? entries[0];
  return typeof def?.wordCount === "number" ? def.wordCount : 0;
}

export function exportMeeting({ meetingId, sourcePath, sourceType, outputDir, recordingsBaseUrl = null }) {
  const { title, dateLabel } = describeMeeting(meetingId);

  if (sourceType === "portable") {
    const portable = extractPortableManifest(sourcePath);
    const transcript = buildTranscriptWordsFromPortable(portable);
    // A real embedded title (e.g. the Talk room name the operator captured
    // at recording time) beats anything derived from the file name; packer
    // defaults that merely echo the id fall through to describeMeeting.
    const meetingTitle = preferredPortableTitle(portable, meetingId) || title;
    const targetFileName = `${meetingId}.opus`;
    if (!recordingsBaseUrl) {
      cpSync(sourcePath, join(outputDir, "meetings", targetFileName));
    }
    const sttVariantLabel = describeSpeechToTextVariant({ provenance: portable.provenance }) || describeVariantSuffix(meetingId);
    return {
      id: meetingId,
      audioPath: recordingsBaseUrl ? `${recordingsBaseUrl}meetings/${targetFileName}` : `./meetings/${targetFileName}`,
      title: sttVariantLabel ? `${meetingTitle} (${sttVariantLabel})` : meetingTitle,
      dateLabel,
      speakerCount: transcript.speakers?.length ?? 0,
      // For a v2 .opus the inline items are absent (see portableDefaultSegmentCount);
      // fall back to the default transcript's wordCount so the catalog does not
      // ship segmentCount: 0 (which the viewer's hydration gate would treat as
      // "already populated" and never correct).
      segmentCount: transcript.segments?.length || portableDefaultSegmentCount(portable),
      digestDurationMs: transcript.media?.durationMs ?? 0,
    };
  }

  if (recordingsBaseUrl) {
    const manifest = readManifest(sourcePath, meetingId);
    const sttVariantLabel = describeSpeechToTextVariant(manifest) || describeVariantSuffix(meetingId);
    return {
      id: meetingId,
      artifactPath: `${recordingsBaseUrl}meetings/${meetingId}`,
      title: sttVariantLabel ? `${title} (${sttVariantLabel})` : title,
      dateLabel,
      speakerCount: manifest.speakerCount ?? 0,
      segmentCount: manifest.segmentCount ?? 0,
      digestDurationMs: manifest.digestDurationMs ?? 0,
    };
  }

  const targetMeetingDir = join(outputDir, "meetings", meetingId);
  mkdirSync(targetMeetingDir, { recursive: true });
  const manifest = readManifest(sourcePath, meetingId);
  copyPublicMeetingFiles(sourcePath, targetMeetingDir, manifest);
  ensureDisplayTranscript(targetMeetingDir);
  const targetManifest = readManifest(targetMeetingDir, meetingId);
  const sttVariantLabel = describeSpeechToTextVariant(targetManifest) || describeVariantSuffix(meetingId);
  return {
    id: meetingId,
    artifactPath: `./meetings/${meetingId}`,
    title: sttVariantLabel ? `${title} (${sttVariantLabel})` : title,
    dateLabel,
    speakerCount: targetManifest.speakerCount ?? 0,
    segmentCount: targetManifest.segmentCount ?? 0,
    digestDurationMs: targetManifest.digestDurationMs ?? 0,
  };
}

export function extractPortableManifest(path) {
  const output = execFileSync("ffprobe", [
    "-v",
    "error",
    "-show_entries",
    "format_tags:stream_tags",
    "-of",
    "json",
    path,
  ], { encoding: "utf8" });

  const report = JSON.parse(output);
  const tags = {};
  for (const [key, value] of Object.entries(report.format?.tags || {})) {
    tags[key] = String(value ?? "");
  }
  for (const stream of report.streams || []) {
    for (const [key, value] of Object.entries(stream.tags || {})) {
      tags[key] = String(value ?? "");
    }
  }

  const chunkCount = safeToInt(tags.CASSINI_PAYLOAD_CHUNK_COUNT, 0);
  if (chunkCount <= 0) {
    throw new Error(`Missing or invalid CASSINI_PAYLOAD_CHUNK_COUNT in ${path}`);
  }

  let encoded = "";
  for (let index = 0; index < chunkCount; index += 1) {
    const key = `CASSINI_PAYLOAD_${String(index).padStart(3, "0")}`;
    if (typeof tags[key] !== "string" || tags[key].trim() === "") {
      throw new Error(`Missing payload chunk ${key} in ${path}`);
    }
    encoded += tags[key];
  }

  const rawPayload = Buffer.from(encoded, "base64url");
  const manifestJSON = gunzipSync(rawPayload);
  return JSON.parse(manifestJSON.toString("utf8"));
}

export function buildTranscriptWordsFromPortable(portable) {
  const speakers = normalizeSpeakers(portable.speakers || []);
  const items = Array.isArray(portable.transcript?.items) ? portable.transcript.items : [];
  const segments = items.map((item, index) => {
    const segmentId = typeof item?.id === "string" && item.id.trim() !== "" ? item.id : `seg_${String(index).padStart(6, "0")}`;
    const startMs = safeToInt(item?.startMs, 0);
    const endMs = safeToInt(item?.endMs, startMs);
    const text = typeof item?.text === "string" ? item.text : "";
    // Portable meetings may contain either true word-level transcript items or
    // older segment-level text spans. Only the single-word case is safe to turn
    // back into a timed transcript word. Multi-word spans would fabricate
    // uniform word timings that were never produced by ASR.
    const words = isSinglePortableWord(text) ? splitTextIntoWords(text, startMs, endMs) : [];
    const speaker = typeof item?.speaker === "string" && item.speaker.trim() !== "" ? item.speaker : undefined;

    return {
      id: segmentId,
      speaker: speaker || undefined,
      startMs,
      endMs,
      text,
      words: words.map((word) => ({
        ...word,
        id: `${segmentId}:${word.id}`,
      })),
    };
  });

  return {
    version: "transcript.words.v1",
    media: {
      src: "meeting.opus",
      durationMs: safeToInt(portable.meeting?.durationMs, 0),
      sha256: safeToString(portable.audio?.sha256),
    },
    speakers,
    segments,
  };
}

function isSinglePortableWord(text) {
  return typeof text === "string" && text.trim().split(/\s+/).filter(Boolean).length <= 1;
}

function extractPortableReadableWords(segment, segmentId) {
  if (!Array.isArray(segment?.words)) {
    return [];
  }
  return segment.words.flatMap((word, index) => {
    const text = safeToString(word?.text).trim();
    if (!text) {
      return [];
    }
    const startMs = safeToInt(word?.startMs, NaN);
    const endMs = safeToInt(word?.endMs, startMs);
    if (!Number.isFinite(startMs) || !Number.isFinite(endMs)) {
      return [];
    }
    return [{
      id: typeof word?.id === "string" && word.id.trim() !== "" ? word.id : `${segmentId}:w_${index}`,
      text,
      startMs,
      endMs,
    }];
  });
}

function extractTranscriptArtifactWords(segment, segmentId) {
  // Go transcript artifacts do not currently ship per-word IDs. Synthesize
  // stable IDs here so cleaned display tokens can align back to exact source
  // words and preserve word seek in exported sites.
  if (!Array.isArray(segment?.words)) {
    return [];
  }
  return segment.words.flatMap((word, index) => {
    const text = safeToString(word?.text).trim();
    if (!text) {
      return [];
    }
    const startMs = safeToInt(word?.startMs, NaN);
    const endMs = safeToInt(word?.endMs, startMs);
    if (!Number.isFinite(startMs) || !Number.isFinite(endMs)) {
      return [];
    }
    return [{
      id: typeof word?.id === "string" && word.id.trim() !== "" ? word.id : `${segmentId}:w_${index}`,
      text,
      startMs,
      endMs,
    }];
  });
}

export function buildReadableTranscriptFromPortable(portable, transcript) {
  const provided = portable.readableTranscript;
  const speakers = normalizeSpeakers(portable.speakers || transcript.speakers || []);
  const validSpeakerIds = new Set(speakers.map((speaker) => speaker.id));

  // Some already-published portable meetings stored cleaned transcript payloads
  // under the readableTranscript field but stamped them with the raw transcript
  // version. We still want to recover that cleaned text during export.
  if (
    provided &&
    (provided.version === "transcript.readable.v1" || provided.version === "transcript.words.v1") &&
    Array.isArray(provided.segments)
  ) {
    return {
      version: "transcript.readable.v1",
      media: {
        src: transcript.media.src,
        durationMs: transcript.media.durationMs,
        sha256: transcript.media.sha256,
      },
      speakers: normalizeSpeakers(provided.speakers || speakers),
      segments: provided.segments.map((segment, index) => {
        const segmentId =
          typeof segment?.id === "string" && segment.id.trim() !== ""
            ? segment.id
            : `readable_${String(index).padStart(6, "0")}`;
        const sourceSegmentIds = Array.isArray(segment?.sourceSegmentIds)
          ? segment.sourceSegmentIds.filter((value) => typeof value === "string" && value.trim() !== "")
          : [];
        const speaker =
          typeof segment?.speaker === "string" && segment.speaker.trim() !== "" && validSpeakerIds.has(segment.speaker)
            ? segment.speaker
            : undefined;
        const words = extractPortableReadableWords(segment, segmentId);
        return {
          id: segmentId,
          speaker,
          startMs: safeToInt(segment?.startMs, 0),
          endMs: safeToInt(segment?.endMs, safeToInt(segment?.startMs, 0)),
          text: typeof segment?.text === "string" ? segment.text : "",
          sourceSegmentIds,
          ...(words.length > 0 ? { words } : {}),
        };
      }),
      sourceTranscriptVersion: provided.sourceTranscriptVersion ?? "transcript.words.v1",
    };
  }

  return {
    version: "transcript.readable.v1",
    media: {
      src: transcript.media.src,
      durationMs: transcript.media.durationMs,
      sha256: transcript.media.sha256,
    },
    speakers,
    sourceTranscriptVersion: "transcript.words.v1",
    segments: groupTranscriptSegmentsAsReadable(transcript.segments || []),
  };
}

function buildReadableDisplaySourceBlocks(readable, speakerLabels, transcriptSegments) {
  const segmentById = new Map((Array.isArray(transcriptSegments) ? transcriptSegments : []).map((segment) => [segment.id, segment]));
  const baseBlocks = readable.segments.map((segment, index) => {
    const blockId =
      typeof segment?.id === "string" && segment.id.trim() !== ""
        ? segment.id
        : `rseg_${String(index).padStart(6, "0")}`;
    const sourceSegmentIds = Array.isArray(segment?.sourceSegmentIds) ? [...segment.sourceSegmentIds] : [];
    return {
      id: blockId,
      speaker: segment?.speaker,
      speakerLabel: segment?.speaker ? speakerLabels.get(segment.speaker) || segment.speaker : "Unknown speaker",
      startMs: safeToInt(segment?.startMs, 0),
      endMs: safeToInt(segment?.endMs, safeToInt(segment?.startMs, 0)),
      text: typeof segment?.text === "string" ? segment.text : "",
      sourceSegmentIds,
      words: extractPortableReadableWords(segment, blockId),
      sourceWords: collectReadableSourceWords({
        segment,
        sourceSegmentIds,
        segmentById,
        transcriptSegments,
      }),
    };
  });
  return splitReadableBlocksOnInterruptions(baseBlocks);
}

function collectReadableSourceWords({ segment, sourceSegmentIds, segmentById, transcriptSegments }) {
  const resolved = sourceSegmentIds
    .map((segmentId) => segmentById.get(segmentId))
    .filter(Boolean);
  const candidates = resolved.length > 0
    ? resolved
    : (Array.isArray(transcriptSegments) ? transcriptSegments : []).filter((candidate) => {
        const startMs = safeToInt(candidate?.startMs, 0);
        const endMs = safeToInt(candidate?.endMs, startMs);
        if (endMs < safeToInt(segment?.startMs, 0) || startMs > safeToInt(segment?.endMs, startMs)) {
          return false;
        }
        if (typeof segment?.speaker === "string" && candidate?.speaker && candidate.speaker !== segment.speaker) {
          return false;
        }
        return true;
      });
  return candidates.flatMap((candidate) =>
    Array.isArray(candidate?.words)
      ? candidate.words
          .filter((word) => word && typeof word === "object")
          .map((word, index) => ({
            id: typeof word.id === "string" && word.id.trim() !== "" ? word.id : `${candidate.id}:w_${index}`,
            text: safeToString(word.text).trim(),
            startMs: safeToInt(word.startMs, Number.NaN),
            endMs: safeToInt(word.endMs, safeToInt(word.startMs, Number.NaN)),
          }))
          .filter((word) => word.text && Number.isFinite(word.startMs) && Number.isFinite(word.endMs))
      : [],
  );
}

function splitReadableBlocksOnInterruptions(blocks) {
  return blocks.flatMap((block, blockIndex) => splitReadableBlockOnInterruptions(block, blockIndex, blocks));
}

function splitReadableBlockOnInterruptions(block, blockIndex, allBlocks) {
  if (!Array.isArray(block.words) || block.words.length === 0 || !block.speaker) {
    return [block];
  }

  const interruptions = mergeTimeWindows(
    allBlocks
      .filter((other, index) => index !== blockIndex && other.speaker && other.speaker !== block.speaker)
      .map((other) => ({
        startMs: Math.max(block.startMs, other.startMs),
        endMs: Math.min(block.endMs, other.endMs),
      }))
      .filter((window) => window.endMs - window.startMs >= 400),
  );
  const windows = subtractTimeWindows(
    { startMs: block.startMs, endMs: block.endMs },
    interruptions,
  ).filter((window) => window.endMs - window.startMs >= 250);

  if (windows.length <= 1) {
    return [block];
  }

  const transcriptWordCounts =
    Array.isArray(block.sourceWords) && block.sourceWords.length > 0
      ? assignWordsToWindows(block.sourceWords, windows)
      : null;
  const uniformlyInterpolated = wordsLookUniformlyInterpolated(block.words, block.startMs, block.endMs);
  const counts = transcriptWordCounts && transcriptWordCounts.some((count) => count > 0)
    ? transcriptWordCounts
    : uniformlyInterpolated
      ? allocateWordCountsAcrossWindows(block.words.length, windows)
      : assignWordsToWindows(block.words, windows);
  const plan = windows
    .map((window, index) => ({ window, count: counts[index] ?? 0 }))
    .filter((entry) => entry.count > 0);

  if (plan.length <= 1) {
    return [block];
  }

  const textParts = splitTextByWordCounts(block.text, plan.map((entry) => entry.count));
  const derivedBlocks = [];
  let wordOffset = 0;
  let sourceWordOffset = 0;
  for (let index = 0; index < plan.length; index += 1) {
    const { window, count } = plan[index];
    const words = block.words.slice(wordOffset, wordOffset + count);
    const sourceWords = Array.isArray(block.sourceWords)
      ? block.sourceWords.slice(sourceWordOffset, sourceWordOffset + count)
      : [];
    wordOffset += count;
    sourceWordOffset += count;
    if (words.length === 0) {
      continue;
    }
    const timedWords = uniformlyInterpolated ? retimeWordsWithinWindow(words, window) : words;
    derivedBlocks.push({
      ...block,
      id: `${block.id}__split_${String(index).padStart(2, "0")}`,
      startMs: sourceWords[0]?.startMs ?? timedWords[0]?.startMs ?? window.startMs,
      endMs: sourceWords[sourceWords.length - 1]?.endMs ?? timedWords[timedWords.length - 1]?.endMs ?? window.endMs,
      text: textParts[index] ?? rebuildTextFromWords(timedWords),
      words: timedWords,
      sourceWords,
      sourceSegmentIds: sourceWords.length > 0
        ? [...new Set(sourceWords.map((word) => String(word.id ?? "").split(":")[0]).filter(Boolean))]
        : block.sourceSegmentIds,
    });
  }

  return derivedBlocks.length > 0 ? derivedBlocks : [block];
}

function mergeTimeWindows(windows) {
  const sorted = [...windows].sort((left, right) => left.startMs - right.startMs);
  const merged = [];
  for (const window of sorted) {
    const previous = merged.at(-1);
    if (!previous || window.startMs > previous.endMs) {
      merged.push({ ...window });
      continue;
    }
    previous.endMs = Math.max(previous.endMs, window.endMs);
  }
  return merged;
}

function subtractTimeWindows(range, removals) {
  const windows = [];
  let cursor = range.startMs;
  for (const removal of removals) {
    if (removal.endMs <= cursor) {
      continue;
    }
    if (removal.startMs > cursor) {
      windows.push({
        startMs: cursor,
        endMs: Math.min(removal.startMs, range.endMs),
      });
    }
    cursor = Math.max(cursor, removal.endMs);
    if (cursor >= range.endMs) {
      break;
    }
  }
  if (cursor < range.endMs) {
    windows.push({ startMs: cursor, endMs: range.endMs });
  }
  return windows.filter((window) => window.endMs > window.startMs);
}

function wordsLookUniformlyInterpolated(words, startMs, endMs) {
  if (!Array.isArray(words) || words.length === 0) {
    return false;
  }
  const span = Math.max(0, endMs - startMs);
  const toleranceMs = Math.max(25, Math.floor(span / Math.max(words.length, 1) / 5));
  return words.every((word, index) => {
    const expectedStart = words.length <= 1
      ? startMs
      : startMs + Math.floor((span * index) / words.length);
    const expectedEnd = words.length <= 1
      ? endMs
      : startMs + Math.floor((span * (index + 1)) / words.length);
    return (
      Math.abs(safeToInt(word.startMs, expectedStart) - expectedStart) <= toleranceMs &&
      Math.abs(safeToInt(word.endMs, expectedEnd) - expectedEnd) <= toleranceMs
    );
  });
}

function allocateWordCountsAcrossWindows(totalWords, windows) {
  const totalDuration = windows.reduce((sum, window) => sum + Math.max(0, window.endMs - window.startMs), 0);
  if (totalDuration <= 0) {
    return windows.map((_, index) => (index === windows.length - 1 ? totalWords : 0));
  }
  const counts = windows.map((window) =>
    Math.floor((totalWords * Math.max(0, window.endMs - window.startMs)) / totalDuration),
  );
  let assigned = counts.reduce((sum, count) => sum + count, 0);
  const rankedRemainders = windows
    .map((window, index) => ({
      index,
      remainder:
        ((totalWords * Math.max(0, window.endMs - window.startMs)) / totalDuration) - (counts[index] ?? 0),
    }))
    .sort((left, right) => right.remainder - left.remainder);
  for (const { index } of rankedRemainders) {
    if (assigned >= totalWords) {
      break;
    }
    counts[index] = (counts[index] ?? 0) + 1;
    assigned += 1;
  }
  if (assigned < totalWords) {
    counts[counts.length - 1] = (counts[counts.length - 1] ?? 0) + (totalWords - assigned);
  }
  return counts;
}

function assignWordsToWindows(words, windows) {
  const counts = windows.map(() => 0);
  for (const word of words) {
    const midpoint = Math.floor((safeToInt(word.startMs, 0) + safeToInt(word.endMs, 0)) / 2);
    let winnerIndex = windows.findIndex((window) => midpoint >= window.startMs && midpoint < window.endMs);
    if (winnerIndex < 0) {
      winnerIndex = windows.reduce((bestIndex, window, index) => {
        const bestWindow = windows[bestIndex];
        const bestDistance = distanceToWindow(midpoint, bestWindow);
        const nextDistance = distanceToWindow(midpoint, window);
        return nextDistance < bestDistance ? index : bestIndex;
      }, 0);
    }
    counts[winnerIndex] = (counts[winnerIndex] ?? 0) + 1;
  }
  return counts;
}

function distanceToWindow(value, window) {
  if (value < window.startMs) {
    return window.startMs - value;
  }
  if (value > window.endMs) {
    return value - window.endMs;
  }
  return 0;
}

function retimeWordsWithinWindow(words, window) {
  if (!Array.isArray(words) || words.length === 0) {
    return [];
  }
  const span = Math.max(0, window.endMs - window.startMs);
  return words.map((word, index) => ({
    ...word,
    startMs: words.length <= 1 ? window.startMs : window.startMs + Math.floor((span * index) / words.length),
    endMs: words.length <= 1 ? window.endMs : window.startMs + Math.floor((span * (index + 1)) / words.length),
  }));
}

function splitTextByWordCounts(text, counts) {
  const tokens = tokenizeDisplayText(text);
  const parts = [];
  let tokenStart = 0;
  for (const count of counts) {
    let tokenEnd = tokenStart;
    let wordsSeen = 0;
    while (tokenEnd < tokens.length && wordsSeen < count) {
      if (tokens[tokenEnd]?.kind === "word") {
        wordsSeen += 1;
      }
      tokenEnd += 1;
    }
    while (tokenEnd < tokens.length && tokens[tokenEnd]?.kind !== "word") {
      tokenEnd += 1;
    }
    parts.push(rebuildTextFromTokens(tokens.slice(tokenStart, tokenEnd)));
    tokenStart = tokenEnd;
  }
  if (parts.length > 0 && tokenStart < tokens.length) {
    parts[parts.length - 1] = `${parts[parts.length - 1]}${rebuildTextFromTokens(tokens.slice(tokenStart))}`;
  }
  return parts;
}

function rebuildTextFromTokens(tokens) {
  let text = "";
  for (const token of tokens) {
    if (token.spaceBefore && text !== "") {
      text += " ";
    }
    text += token.text;
  }
  return text;
}

function rebuildTextFromWords(words) {
  return words.map((word) => word.text).join(" ");
}

export function groupTranscriptSegmentsAsReadable(segments) {
  const grouped = [];
  let current = null;
  const hardGapMs = 4200;
  const softGapMs = 2200;
  const targetParagraphWords = 96;
  const targetParagraphDurationMs = 45000;
  const maxParagraphWords = 140;
  const maxParagraphDurationMs = 90000;
  const minStandaloneWords = 18;
  const minStandaloneDurationMs = 8000;

  const flush = () => {
    if (!current || current.sourceSegments.length === 0) {
      current = null;
      return;
    }
    grouped.push({
      id: `r_${current.sourceSegments[0].id}`,
      speaker: current.speaker,
      startMs: current.startMs,
      endMs: current.endMs,
      text: joinSegmentTexts(current.sourceSegments),
      sourceSegmentIds: current.sourceSegments.map((segment) => segment.id),
    });
    current = null;
  };

  const normalizedSegments = Array.isArray(segments) ? segments : [];
  for (const segment of normalizedSegments) {
    if (!segment || typeof segment !== "object") {
      continue;
    }
    const speaker = typeof segment.speaker === "string" && segment.speaker.trim() !== "" ? segment.speaker : undefined;
    const startMs = safeToInt(segment.startMs, 0);
    const endMs = safeToInt(segment.endMs, startMs);
    const segmentWordCount = Array.isArray(segment.words) && segment.words.length > 0
      ? segment.words.length
      : countWordsInText(segment.text);

    if (!current) {
      current = {
        speaker,
        startMs,
        endMs,
        wordCount: segmentWordCount,
        text: safeToString(segment.text).trim(),
        sourceSegments: [segment],
      };
      continue;
    }

    const gapMs = Math.max(0, startMs - current.endMs);
    const durationMs = Math.max(0, endMs - current.startMs);
    const nextWordCount = current.wordCount + segmentWordCount;
    const speakerChanged = current.speaker !== speaker;
    const paragraphTargetReached = (
      current.wordCount >= targetParagraphWords ||
      (current.endMs - current.startMs) >= targetParagraphDurationMs
    );
    const naturalBoundary = endsSentence(current.text) || gapMs >= softGapMs;
    const shouldFlush = (
      speakerChanged ||
      gapMs > hardGapMs ||
      durationMs > maxParagraphDurationMs ||
      nextWordCount > maxParagraphWords ||
      (paragraphTargetReached && naturalBoundary)
    );

    if (shouldFlush) {
      flush();
      current = {
        speaker,
        startMs,
        endMs,
        wordCount: segmentWordCount,
        text: safeToString(segment.text).trim(),
        sourceSegments: [segment],
      };
      continue;
    }

    current.sourceSegments.push(segment);
    current.endMs = endMs;
    current.wordCount = nextWordCount;
    current.text = joinSegmentTexts(current.sourceSegments);
  }

  flush();
  return mergeSmallReadableSegments(grouped, {
    maxParagraphWords,
    maxParagraphDurationMs,
    hardGapMs,
    minStandaloneWords,
    minStandaloneDurationMs,
  });
}

function mergeSmallReadableSegments(grouped, options) {
  const merged = [];
  const items = Array.isArray(grouped) ? grouped : [];
  for (const segment of items) {
    const previous = merged.at(-1);
    if (!previous) {
      merged.push(segment);
      continue;
    }

    const previousWordCount = countWordsInText(previous.text);
    const currentWordCount = countWordsInText(segment.text);
    const previousDurationMs = Math.max(0, safeToInt(previous.endMs, 0) - safeToInt(previous.startMs, 0));
    const currentDurationMs = Math.max(0, safeToInt(segment.endMs, 0) - safeToInt(segment.startMs, 0));
    const gapMs = Math.max(0, safeToInt(segment.startMs, 0) - safeToInt(previous.endMs, 0));
    const combinedWordCount = previousWordCount + currentWordCount;
    const combinedDurationMs = Math.max(0, safeToInt(segment.endMs, 0) - safeToInt(previous.startMs, 0));
    const sameSpeaker = previous.speaker === segment.speaker;
    const oneSideTooSmall = (
      previousWordCount < options.minStandaloneWords ||
      currentWordCount < options.minStandaloneWords ||
      previousDurationMs < options.minStandaloneDurationMs ||
      currentDurationMs < options.minStandaloneDurationMs
    );

    if (
      sameSpeaker &&
      gapMs <= options.hardGapMs &&
      combinedWordCount <= options.maxParagraphWords &&
      combinedDurationMs <= options.maxParagraphDurationMs &&
      oneSideTooSmall
    ) {
      previous.endMs = segment.endMs;
      previous.text = joinSegmentTexts([{ text: previous.text }, { text: segment.text }]);
      previous.sourceSegmentIds = [...(previous.sourceSegmentIds || []), ...(segment.sourceSegmentIds || [])];
      continue;
    }

    merged.push(segment);
  }
  return merged;
}

function joinSegmentTexts(segments) {
  let text = "";
  for (const segment of segments) {
    const part = safeToString(segment?.text).trim();
    if (!part) {
      continue;
    }
    if (!text) {
      text = part;
      continue;
    }
    if (/^[,.;:!?)]/.test(part)) {
      text += part;
      continue;
    }
    text += ` ${part}`;
  }
  return text;
}

function countWordsInText(text) {
  return safeToString(text)
    .trim()
    .split(/\s+/)
    .filter(Boolean)
    .length;
}

function endsSentence(text) {
  return /[.!?]["')\]]*$/.test(safeToString(text).trim());
}

export function buildDisplayTranscriptFromArtifacts(transcript, readable) {
  const speakers = normalizeSpeakers(transcript.speakers || []);
  const speakerLabels = new Map(speakers.map((speaker) => [speaker.id, speaker.label]));
  const transcriptSegments = Array.isArray(transcript.segments) ? transcript.segments : [];
  const segmentById = new Map(transcriptSegments.map((segment) => [segment.id, segment]));
  const sourceBlocks =
    readable && readable.version === "transcript.readable.v1" && Array.isArray(readable.segments)
      ? buildReadableDisplaySourceBlocks(readable, speakerLabels, transcriptSegments)
      : transcriptSegments.map((segment) => ({
          id: `d_${segment.id}`,
          speaker: segment.speaker,
          speakerLabel: segment.speaker ? speakerLabels.get(segment.speaker) || segment.speaker : "Unknown speaker",
          startMs: segment.startMs,
          endMs: segment.endMs,
          text: segment.text,
          sourceSegmentIds: [segment.id],
          words: [],
          sourceWords: Array.isArray(segment.words) ? segment.words : [],
        }));

  return {
    version: "transcript.display.v1",
    media: { ...transcript.media },
    speakers,
    blocks: sourceBlocks.map((block, blockIndex) => {
      const sourceSegmentIds = Array.isArray(block.sourceSegmentIds)
        ? block.sourceSegmentIds.filter((value) => typeof value === "string" && value.trim() !== "")
        : [];
      const sourceSegments = resolveDisplaySourceSegments({
        block,
        sourceSegmentIds,
        segmentById,
        transcriptSegments,
      });
      const sourceWordsFromTranscript = sourceSegments.flatMap((segment, index) =>
        extractTranscriptArtifactWords(
          segment,
          typeof segment?.id === "string" && segment.id.trim() !== ""
            ? segment.id
            : `seg_${String(index).padStart(6, "0")}`,
        ),
      );
      const sourceWordsFromReadable = extractPortableReadableWords(
        block,
        `block_${String(blockIndex).padStart(6, "0")}`,
      );
      const sourceWords = sourceWordsFromTranscript.length > 0 ? sourceWordsFromTranscript : sourceWordsFromReadable;
      const sourceWordById = new Map(
        sourceWords
          .filter((word) => typeof word.id === "string" && word.id.trim() !== "")
          .map((word) => [word.id, word]),
      );
      const sourceWordIndexById = new Map(
        sourceWords
          .filter((word) => typeof word.id === "string" && word.id.trim() !== "")
          .map((word, index) => [word.id, index]),
      );
      const alignment = alignReadableTokensToSourceWords(sourceWords, typeof block.text === "string" ? block.text : "");
      const fallbackStartMs = sourceWords.length > 0
        ? safeToInt(sourceWords[0]?.startMs, safeToInt(block.startMs, 0))
        : sourceSegments.length > 0
          ? safeToInt(sourceSegments[0]?.startMs, safeToInt(block.startMs, 0))
          : safeToInt(block.startMs, 0);
      const fallbackEndMs = sourceWords.length > 0
        ? safeToInt(sourceWords[sourceWords.length - 1]?.endMs, safeToInt(block.endMs, fallbackStartMs))
        : sourceSegments.length > 0
          ? safeToInt(sourceSegments[sourceSegments.length - 1]?.endMs, safeToInt(block.endMs, fallbackStartMs))
          : safeToInt(block.endMs, fallbackStartMs);
      const exactTokens = alignment.tokens.map((token) => {
        const matchedWords = token.sourceWordIds
          .map((wordId) => sourceWordById.get(wordId))
          .filter(Boolean);
        if (matchedWords.length > 0) {
          return {
            text: token.text,
            spaceBefore: token.spaceBefore,
            kind: token.kind,
            sourceWordIds: [...token.sourceWordIds],
            startMs: Math.min(...matchedWords.map((word) => safeToInt(word.startMs, fallbackStartMs))),
            endMs: Math.max(...matchedWords.map((word) => safeToInt(word.endMs, fallbackEndMs))),
            alignment: "source",
          };
        }
        return {
          text: token.text,
          spaceBefore: token.spaceBefore,
          kind: token.kind,
          sourceWordIds: [...token.sourceWordIds],
          alignment: token.kind === "punctuation" ? "none" : "none",
        };
      });
      const tokens = interpolateUntimedWordRuns(
        exactTokens,
        fallbackStartMs,
        fallbackEndMs,
        sourceWords,
        sourceWordIndexById,
      );
      const wordCount = tokens.filter((token) => token.kind === "word").length;
      const timedWordCount = tokens.filter(
        (token) =>
          token.kind === "word" &&
          Number.isInteger(token.startMs) &&
          Number.isInteger(token.endMs),
      ).length;
      const timedTokens = tokens.filter(
        (token) =>
          Number.isInteger(token.startMs) &&
          Number.isInteger(token.endMs),
      );
      return {
        id:
          typeof block.id === "string" && block.id.trim() !== ""
            ? block.id
            : `dseg_${String(blockIndex).padStart(6, "0")}`,
        speaker:
          typeof block.speaker === "string" && speakerLabels.has(block.speaker) ? block.speaker : undefined,
        speakerLabel:
          typeof block.speakerLabel === "string" && block.speakerLabel.trim() !== ""
            ? block.speakerLabel
            : speakerLabels.get(block.speaker) || "Unknown speaker",
        startMs: timedTokens.length > 0 ? safeToInt(timedTokens[0].startMs, fallbackStartMs) : fallbackStartMs,
        endMs: timedTokens.length > 0
          ? safeToInt(timedTokens[timedTokens.length - 1].endMs, fallbackEndMs)
          : fallbackEndMs,
        text: typeof block.text === "string" ? block.text : "",
        sourceSegmentIds,
        wordCount,
        timedWordCount,
        timingCoverage: wordCount === 0 ? 1 : timedWordCount / wordCount,
        tokens,
      };
    }),
    sourceTranscriptVersion: transcript.version,
    sourceReadableTranscriptVersion: readable?.version,
  };
}

function interpolateUntimedWordRuns(tokens, fallbackStartMs, fallbackEndMs, sourceWords, sourceWordIndexById) {
  const next = tokens.map((token) => ({ ...token, sourceWordIds: [...token.sourceWordIds] }));
  const wordTokenIndexes = next
    .map((token, index) => (token.kind === "word" ? index : -1))
    .filter((index) => index >= 0);

  let cursor = 0;
  while (cursor < wordTokenIndexes.length) {
    const tokenIndex = wordTokenIndexes[cursor];
    if (tokenHasTiming(next[tokenIndex])) {
      cursor += 1;
      continue;
    }

    const runStart = cursor;
    while (cursor < wordTokenIndexes.length && !tokenHasTiming(next[wordTokenIndexes[cursor]])) {
      cursor += 1;
    }
    const runTokenIndexes = wordTokenIndexes.slice(runStart, cursor);
    const prevTimedToken = runStart > 0 ? next[wordTokenIndexes[runStart - 1]] : null;
    const nextTimedToken = cursor < wordTokenIndexes.length ? next[wordTokenIndexes[cursor]] : null;
    const hasPrevAnchor = tokenHasTiming(prevTimedToken);
    const hasNextAnchor = tokenHasTiming(nextTimedToken);
    if (!hasPrevAnchor || !hasNextAnchor) {
      continue;
    }
    if (!shouldInterpolateUntimedRun({
      tokens: next,
      runTokenIndexes,
      prevTimedToken,
      nextTimedToken,
      sourceWords,
      sourceWordIndexById,
    })) {
      continue;
    }
    const { startMs, endMs } = resolveInterpolatedSpan({
      prevTimedToken,
      nextTimedToken,
      fallbackStartMs,
      fallbackEndMs,
    });
    const span = Math.max(0, endMs - startMs);

    // Preserve exact source matches when we have them, but keep rewritten runs
    // seekable by spreading them across the surrounding source span.
    for (let index = 0; index < runTokenIndexes.length; index += 1) {
      const runTokenIndex = runTokenIndexes[index];
      const tokenStart = runTokenIndexes.length <= 1
        ? startMs
        : startMs + Math.floor((span * index) / runTokenIndexes.length);
      const tokenEnd = runTokenIndexes.length <= 1
        ? endMs
        : startMs + Math.floor((span * (index + 1)) / runTokenIndexes.length);
      next[runTokenIndex] = {
        ...next[runTokenIndex],
        startMs: tokenStart,
        endMs: Math.max(tokenEnd, tokenStart),
        alignment: "interpolated",
      };
    }
  }

  return next;
}

function shouldInterpolateUntimedRun({
  tokens,
  runTokenIndexes,
  prevTimedToken,
  nextTimedToken,
  sourceWords,
  sourceWordIndexById,
}) {
  const prevIndexes = resolveTokenSourceIndexes(prevTimedToken, sourceWordIndexById);
  const nextIndexes = resolveTokenSourceIndexes(nextTimedToken, sourceWordIndexById);
  if (prevIndexes.length === 0 || nextIndexes.length === 0) {
    return false;
  }

  const prevEndIndex = Math.max(...prevIndexes);
  const nextStartIndex = Math.min(...nextIndexes);
  if (nextStartIndex <= prevEndIndex) {
    return false;
  }

  const runWords = runTokenIndexes
    .map((tokenIndex) => normalizeAlignmentToken(tokens[tokenIndex]?.text ?? ""))
    .filter(Boolean);
  if (runWords.length === 0) {
    return false;
  }

  const sourceGapWords = sourceWords
    .slice(prevEndIndex + 1, nextStartIndex)
    .map((word) => normalizeAlignmentToken(word?.text ?? ""))
    .filter(Boolean);

  if (sourceGapWords.length === 0) {
    return false;
  }

  const sourceGapSet = new Set(sourceGapWords);
  const intersectionCount = runWords.filter((word) => sourceGapSet.has(word)).length;
  const overlap = intersectionCount / Math.max(runWords.length, sourceGapWords.length);
  return intersectionCount > 0 && overlap >= 0.5 && Math.abs(runWords.length - sourceGapWords.length) <= 2;
}

function resolveTokenSourceIndexes(token, sourceWordIndexById) {
  if (!token || !Array.isArray(token.sourceWordIds)) {
    return [];
  }
  return token.sourceWordIds
    .map((wordId) => sourceWordIndexById.get(wordId))
    .filter((index) => Number.isInteger(index));
}

function resolveInterpolatedSpan({ prevTimedToken, nextTimedToken, fallbackStartMs, fallbackEndMs }) {
  let startMs = tokenHasTiming(prevTimedToken) ? safeToInt(prevTimedToken.endMs, fallbackStartMs) : fallbackStartMs;
  let endMs = tokenHasTiming(nextTimedToken) ? safeToInt(nextTimedToken.startMs, fallbackEndMs) : fallbackEndMs;

  if (endMs <= startMs) {
    const altStartMs = tokenHasTiming(prevTimedToken)
      ? safeToInt(prevTimedToken.startMs, fallbackStartMs)
      : fallbackStartMs;
    const altEndMs = tokenHasTiming(nextTimedToken)
      ? safeToInt(nextTimedToken.endMs, fallbackEndMs)
      : fallbackEndMs;
    startMs = Math.min(altStartMs, altEndMs);
    endMs = Math.max(altStartMs, altEndMs);
  }

  return {
    startMs,
    endMs: Math.max(endMs, startMs),
  };
}

function tokenHasTiming(token) {
  return Boolean(token) && Number.isInteger(token.startMs) && Number.isInteger(token.endMs);
}

export function splitTextIntoWords(text, startMs, endMs) {
  const parts = typeof text === "string" ? text.trim().split(/\s+/).filter(Boolean) : [];
  if (parts.length === 0) {
    return [];
  }

  const span = Math.max(0, endMs - startMs);
  return parts.map((part, index) => {
    const from = parts.length <= 1 ? startMs : startMs + Math.floor((span * index) / parts.length);
    const to = parts.length <= 1
      ? endMs
      : startMs + Math.floor((span * (index + 1)) / parts.length);
    return {
      id: `w_${index}`,
      text: part,
      startMs: Math.min(Math.max(from, startMs), endMs),
      endMs: Math.max(Math.min(to, endMs), startMs),
    };
  });
}

export function ensureDisplayTranscript(targetMeetingDir) {
  const displayPath = join(targetMeetingDir, "transcript.display.v1.json");
  const transcriptPath = join(targetMeetingDir, "transcript.words.v1.json");
  if (existsSync(displayPath) || !existsSync(transcriptPath)) {
    return;
  }
  const transcript = JSON.parse(readFileSync(transcriptPath, "utf8"));
  const readablePath = join(targetMeetingDir, "transcript.readable.v1.json");
  const readable = existsSync(readablePath) ? JSON.parse(readFileSync(readablePath, "utf8")) : null;
  const display = buildDisplayTranscriptFromArtifacts(transcript, readable);
  writeFileSync(displayPath, `${JSON.stringify(display, null, 2)}\n`, "utf8");
}

export function normalizeSpeakers(value) {
  if (!Array.isArray(value)) {
    return [];
  }

  const speakers = [];
  const seen = new Set();
  for (const item of value) {
    if (!item || typeof item !== "object") {
      continue;
    }
    const id = safeToString(item.id);
    const label = safeToString(item.label) || id;
    if (id.trim() === "" || seen.has(id)) {
      continue;
    }
    seen.add(id);
    speakers.push({ id, label });
  }
  return speakers;
}

export function readManifest(sourceDir, meetingId) {
  const manifestPath = join(sourceDir, "manifest.json");
  if (!existsSync(manifestPath)) {
    return {};
  }
  try {
    return JSON.parse(readFileSync(manifestPath, "utf8"));
  } catch (error) {
    throw new Error(`could not parse manifest for ${meetingId}: ${error}`);
  }
}

export function copyPublicMeetingFiles(sourceMeetingDir, targetMeetingDir, manifest) {
  const filesToCopy = new Set([
    "manifest.json",
    "meeting.webm",
    "transcript.words.v1.json",
    "transcript.display.v1.json",
    "transcript.readable.v1.json",
    "captions.vtt",
    "chapters.vtt",
    "timeline.map.v1.json",
  ]);

  if (manifest && typeof manifest === "object" && manifest.files && typeof manifest.files === "object") {
    for (const value of Object.values(manifest.files)) {
      if (typeof value === "string" && value.trim() !== "") {
        filesToCopy.add(value);
      }
    }
  }

  for (const optionalFile of ["transcript.display.v1.json", "transcript.readable.v1.json", "chapters.vtt"]) {
    const optionalPath = join(sourceMeetingDir, optionalFile);
    if (existsSync(optionalPath)) {
      filesToCopy.add(optionalFile);
    }
  }

  for (const relativePath of filesToCopy) {
    const sourcePath = join(sourceMeetingDir, relativePath);
    if (!existsSync(sourcePath)) {
      continue;
    }
    const targetPath = join(targetMeetingDir, relativePath);
    mkdirSync(dirname(targetPath), { recursive: true });
    cpSync(sourcePath, targetPath, { recursive: true });
  }
}

export function isPortableMeeting(fileName) {
  return extname(fileName).toLowerCase() === ".opus";
}

// preferredPortableTitle returns the title embedded in a portable meeting's
// manifest when it is a real human-readable name (e.g. the Talk room name the
// operator resolved at recording time, D-462). Packer defaults — the meeting
// id echoed back or the generic "Cassini Meeting" — yield "" so the caller
// falls back to id-derived naming.
export function preferredPortableTitle(portable, meetingId) {
  const raw = typeof portable?.meeting?.title === "string" ? portable.meeting.title.trim() : "";
  if (raw === "") {
    return "";
  }
  if (raw === meetingId || raw === stripVariantSuffix(meetingId)) {
    return "";
  }
  if (raw === "Cassini Meeting") {
    return "";
  }
  return raw;
}

export function describeMeeting(meetingId) {
  const normalizedMeetingId = stripVariantSuffix(meetingId);
  const colonTimeStamp = parseTimestampFromDoubledDashParts(normalizedMeetingId, "--");
  if (colonTimeStamp) {
    return colonTimeStamp;
  }

  const modernStamp = /^(.*)--(\d{8})T(\d{2})(\d{2})(\d{2})$/.exec(normalizedMeetingId);
  if (modernStamp) {
    const [, rawTitle, yyyymmdd, hour, minute] = modernStamp;
    return {
      title: toTitleCase(rawTitle),
      dateLabel: `${yyyymmdd.slice(0, 4)}-${yyyymmdd.slice(4, 6)}-${yyyymmdd.slice(6, 8)} ${hour}:${minute}`,
    };
  }

  const legacyStamp = /^(.*)--(\d{4})-(\d{2})-(\d{2})--(\d{2})-(\d{2})-(\d{2})$/.exec(normalizedMeetingId);
  if (legacyStamp) {
    const [, rawTitle, year, month, day, hour, minute] = legacyStamp;
    return {
      title: toTitleCase(rawTitle),
      dateLabel: `${year}-${month}-${day} ${hour}:${minute}`,
    };
  }

  // Talk recordings are named by the operator's ULID job id, which carries no
  // human-readable name but does encode the recording start time. Surface that
  // timestamp instead of showing the raw id as both title and date (D-462).
  const ulidDateLabel = dateLabelFromUlid(normalizedMeetingId);
  if (ulidDateLabel) {
    return {
      title: "Untitled meeting",
      dateLabel: ulidDateLabel,
    };
  }

  return {
    title: toTitleCase(normalizedMeetingId),
    dateLabel: normalizedMeetingId,
  };
}

// Canonical 26-char Crockford base32 ULID. The first character of a real ULID
// never exceeds 7 (48-bit timestamp bound). Deliberately uppercase-only —
// operator job ids are always uppercase, and rejecting lowercase keeps
// human-chosen meeting names from ever matching.
const ULID_PATTERN = /^[0-7][0-9ABCDEFGHJKMNPQRSTVWXYZ]{25}$/;
const CROCKFORD_ALPHABET = "0123456789ABCDEFGHJKMNPQRSTVWXYZ";
// Plausibility window for a decoded ULID timestamp; a 26-char Crockford-only
// word that is not a ULID almost never decodes into it.
const ULID_TIMESTAMP_MIN_MS = Date.UTC(2015, 0, 1);
const ULID_TIMESTAMP_MAX_MS = Date.UTC(2100, 0, 1);

// dateLabelFromUlid returns "YYYY-MM-DD HH:MM" (UTC) decoded from a ULID's
// 48-bit timestamp prefix, or "" when the id is not a plausible ULID.
function dateLabelFromUlid(meetingId) {
  if (!ULID_PATTERN.test(meetingId)) {
    return "";
  }
  let ms = 0;
  for (const char of meetingId.slice(0, 10)) {
    ms = ms * 32 + CROCKFORD_ALPHABET.indexOf(char);
  }
  if (ms < ULID_TIMESTAMP_MIN_MS || ms > ULID_TIMESTAMP_MAX_MS) {
    return "";
  }
  const date = new Date(ms);
  const pad = (value) => String(value).padStart(2, "0");
  return `${date.getUTCFullYear()}-${pad(date.getUTCMonth() + 1)}-${pad(date.getUTCDate())} ${pad(
    date.getUTCHours(),
  )}:${pad(date.getUTCMinutes())}`;
}

function stripVariantSuffix(meetingId) {
  return meetingId.replace(/--stt-[A-Za-z0-9._-]+$/, "");
}

export function describeSpeechToTextVariant(manifest) {
  const step = manifest?.provenance?.speechToText;
  if (!step || typeof step !== "object") {
    return "";
  }

  const engine = safeToString(step.engine);
  const model = safeToString(step.model);
  const backend = safeToString(step.backend);

  if (backend === "local-whisper" || engine === "faster-whisper" || engine === "whisper") {
    if (model) {
      return `Whisper ${model}`;
    }
    return "Whisper";
  }

  if (engine.includes("parakeet") || model.includes("parakeet")) {
    return describeParakeetVariant(model);
  }

  if (engine) {
    if (model && model !== engine) {
      return `${toTitleCase(engine)} ${model}`;
    }
    return toTitleCase(engine);
  }

  if (model) {
    return model;
  }

  return "";
}

function describeParakeetVariant(model) {
  if (!model) {
    return "Parakeet";
  }

  const normalized = model.replace(/^nvidia\//, "").replace(/^parakeet-/, "");
  return `Parakeet ${normalized}`;
}

export function describeVariantSuffix(meetingId) {
  const match = /--stt-([A-Za-z0-9._-]+)$/.exec(meetingId);
  if (!match) {
    return "";
  }
  return toTitleCase(match[1].replace(/\./g, "-"));
}

function parseTimestampFromDoubledDashParts(meetingId, joiner = "--") {
  const parts = meetingId.split(joiner);
  if (parts.length < 2) {
    return null;
  }

  const timeParts = parts.at(-1);
  const timeMatch = /^([0-9]{2}):([0-9]{2})(?:\:([0-9]{2}))?$/.exec(timeParts);
  if (!timeMatch) {
    return null;
  }
  const hour = timeMatch[1];
  const minute = timeMatch[2];
  const dateCandidate = parts.at(-2);

  const dateMatch = /^(\d{4})-(\d{2})-(\d{2})$/.exec(dateCandidate);
  if (dateMatch) {
    const [, year, month, day] = dateMatch;
    const rawTitle = parts.slice(0, -2).join(joiner);
    if (!rawTitle) {
      return null;
    }
    return {
      title: toTitleCase(rawTitle),
      dateLabel: `${year}-${month}-${day} ${hour}:${minute}`,
    };
  }

  if (parts.length !== 2) {
    return null;
  }

  const legacyDateMatch = /^(.*)-(\d{4})-(\d{2})-(\d{2})$/.exec(parts[0]);
  if (!legacyDateMatch) {
    return null;
  }
  const [, rawTitle, year, month, day] = legacyDateMatch;
  return {
    title: toTitleCase(rawTitle),
    dateLabel: `${year}-${month}-${day} ${hour}:${minute}`,
  };
}

export function toTitleCase(text) {
  return text
    .split("-")
    .filter(Boolean)
    .map((part) => part.charAt(0).toUpperCase() + part.slice(1))
    .join(" ");
}

function safeToInt(value, fallback) {
  if (typeof value === "number" && Number.isFinite(value)) {
    return Math.trunc(value);
  }
  if (typeof value === "string") {
    const parsed = Number.parseInt(value, 10);
    if (Number.isFinite(parsed)) {
      return parsed;
    }
  }
  return fallback;
}

function resolveDisplaySourceSegments({ block, sourceSegmentIds, segmentById, transcriptSegments }) {
  const resolved = sourceSegmentIds.map((segmentId) => segmentById.get(segmentId)).filter(Boolean);
  if (
    resolved.length > 0 &&
    resolvedSegmentsLookCompatible({
      block,
      resolvedSegments: resolved,
    })
  ) {
    return resolved;
  }
  const blockStartMs = safeToInt(block?.startMs, 0);
  const blockEndMs = safeToInt(block?.endMs, blockStartMs);
  return transcriptSegments.filter((segment) => {
    const startMs = safeToInt(segment?.startMs, 0);
    const endMs = safeToInt(segment?.endMs, startMs);
    if (endMs < blockStartMs || startMs > blockEndMs) {
      return false;
    }
    if (typeof block?.speaker === "string" && segment?.speaker && segment.speaker !== block.speaker) {
      return false;
    }
    return true;
  });
}

function resolvedSegmentsLookCompatible({ block, resolvedSegments }) {
  const targetWordCount = tokenizeDisplayText(typeof block?.text === "string" ? block.text : "").filter(
    (token) => token.kind === "word",
  ).length;
  const resolvedWordCount = resolvedSegments.flatMap((segment) =>
    Array.isArray(segment?.words) ? segment.words : [],
  ).length;
  if (targetWordCount > 0 && resolvedWordCount < targetWordCount) {
    return false;
  }

  const blockStartMs = safeToInt(block?.startMs, 0);
  const blockEndMs = safeToInt(block?.endMs, blockStartMs);
  const blockDurationMs = Math.max(0, blockEndMs - blockStartMs);
  if (blockDurationMs <= 0) {
    return true;
  }
  const resolvedStartMs = Math.min(...resolvedSegments.map((segment) => safeToInt(segment?.startMs, blockStartMs)));
  const resolvedEndMs = Math.max(...resolvedSegments.map((segment) => safeToInt(segment?.endMs, blockEndMs)));
  const resolvedDurationMs = Math.max(0, resolvedEndMs - resolvedStartMs);
  return resolvedDurationMs >= Math.max(1000, Math.floor(blockDurationMs / 2));
}

function tokenizeDisplayText(text) {
  const tokenPattern = /[A-Za-z0-9]+(?:[.'’/_-][A-Za-z0-9]+)*|[^\w\s]/gu;
  const tokens = [];
  let match;
  let cursor = 0;
  while ((match = tokenPattern.exec(text)) !== null) {
    const prefix = text.slice(cursor, match.index);
    const tokenText = match[0];
    tokens.push({
      text: tokenText,
      spaceBefore: /\s/u.test(prefix),
      kind: isWordToken(tokenText) ? "word" : "punctuation",
    });
    cursor = match.index + tokenText.length;
  }
  return tokens;
}

function normalizeAlignmentToken(token) {
  return String(token ?? "")
    .replaceAll("’", "'")
    .toLowerCase()
    .replace(/^[^\w']+|[^\w']+$/gu, "");
}

function isWordToken(token) {
  return normalizeAlignmentToken(token) !== "";
}

function alignReadableTokensToSourceWords(sourceWords, readableText) {
  const tokens = tokenizeDisplayText(readableText);
  const targetWordPositions = [];
  const targetNorms = [];
  for (let index = 0; index < tokens.length; index += 1) {
    if (tokens[index].kind !== "word") {
      continue;
    }
    targetWordPositions.push(index);
    targetNorms.push(normalizeAlignmentToken(tokens[index].text));
  }
  const sourceNorms = sourceWords.map((word) => normalizeAlignmentToken(word?.text ?? ""));
  const dp = buildLcsTable(sourceNorms, targetNorms);
  const targetIndexToSourceWordIds = reconstructLcsAlignment({
    sourceWords,
    sourceNorms,
    targetWordPositions,
    targetNorms,
    dp,
  });
  return {
    tokens: tokens.map((token, index) => ({
      ...token,
      sourceWordIds: targetIndexToSourceWordIds.get(index) ?? [],
    })),
  };
}

function buildLcsTable(sourceNorms, targetNorms) {
  const rows = sourceNorms.length + 1;
  const cols = targetNorms.length + 1;
  const dp = Array.from({ length: rows }, () => Array(cols).fill(0));
  for (let sourceIndex = sourceNorms.length - 1; sourceIndex >= 0; sourceIndex -= 1) {
    for (let targetIndex = targetNorms.length - 1; targetIndex >= 0; targetIndex -= 1) {
      let best = Math.max(dp[sourceIndex + 1][targetIndex], dp[sourceIndex][targetIndex + 1]);
      if (
        sourceNorms[sourceIndex] &&
        sourceNorms[sourceIndex] === targetNorms[targetIndex]
      ) {
        best = Math.max(best, 1 + dp[sourceIndex + 1][targetIndex + 1]);
      }
      dp[sourceIndex][targetIndex] = best;
    }
  }
  return dp;
}

function reconstructLcsAlignment({ sourceWords, sourceNorms, targetWordPositions, targetNorms, dp }) {
  const matched = new Map();
  let sourceIndex = 0;
  let targetIndex = 0;
  while (sourceIndex < sourceNorms.length && targetIndex < targetNorms.length) {
    const sourceNorm = sourceNorms[sourceIndex];
    const targetNorm = targetNorms[targetIndex];
    if (
      sourceNorm &&
      sourceNorm === targetNorm &&
      dp[sourceIndex][targetIndex] === 1 + dp[sourceIndex + 1][targetIndex + 1]
    ) {
      const wordId = typeof sourceWords[sourceIndex]?.id === "string" ? sourceWords[sourceIndex].id : "";
      if (wordId) {
        matched.set(targetWordPositions[targetIndex], [wordId]);
      }
      sourceIndex += 1;
      targetIndex += 1;
      continue;
    }
    if (dp[sourceIndex + 1][targetIndex] >= dp[sourceIndex][targetIndex + 1]) {
      sourceIndex += 1;
    } else {
      targetIndex += 1;
    }
  }
  return matched;
}

function safeToString(value) {
  if (typeof value === "string") {
    return value;
  }
  return "";
}
