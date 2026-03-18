import { mkdirSync, readFileSync, readdirSync, writeFileSync } from "node:fs";
import { dirname, extname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";

import {
  buildDisplayTranscriptFromArtifacts,
  buildReadableTranscriptFromPortable,
  buildTranscriptWordsFromPortable,
  extractPortableManifestFromArrayBuffer,
} from "../src/viewer/portable.ts";

const scriptDir = dirname(fileURLToPath(import.meta.url));
const viewerDir = resolve(scriptDir, "..");

if (process.argv[1] && resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  await main();
}

export async function main(argv = process.argv.slice(2)) {
  const { sourceDir, outputBasePath, limit } = parseArgs(argv);
  const meetings = readdirSync(sourceDir)
    .filter((name) => extname(name).toLowerCase() === ".opus")
    .sort();

  const examples = [];
  for (const name of meetings) {
    const meetingId = name.slice(0, -extname(name).length) || name;
    const portable = await extractPortableManifestFromArrayBuffer(readFileSync(join(sourceDir, name)));
    const transcript = buildTranscriptWordsFromPortable(portable, name);
    const readable = buildReadableTranscriptFromPortable(portable, transcript);
    const display = portable.displayTranscript ?? buildDisplayTranscriptFromArtifacts(transcript, readable);
    const segmentById = new Map(transcript.segments.map((segment) => [segment.id, segment]));

    for (const block of display.blocks) {
      const sourceSegments = block.sourceSegmentIds
        .map((segmentId) => segmentById.get(segmentId))
        .filter(Boolean);
      const sourceText = sourceSegments.map((segment) => segment.text).join(" ");
      const unmatchedWordTokens = block.tokens
        .filter((token) => token.kind === "word" && token.alignment !== "source")
        .map((token) => token.text);
      const score = computeReviewScore({
        sourceText,
        cleanedText: block.text,
        unmatchedWordCount: unmatchedWordTokens.length,
      });

      examples.push({
        meetingId,
        blockId: block.id,
        speakerLabel: block.speakerLabel,
        startMs: block.startMs,
        endMs: block.endMs,
        sourceText,
        cleanedText: block.text,
        wordCount: block.wordCount,
        timedWordCount: block.timedWordCount,
        timingCoverage: block.timingCoverage,
        unmatchedWordTokens,
        sourceSegmentIds: block.sourceSegmentIds,
        score,
      });
    }
  }

  const selected = examples
    .sort((left, right) => right.score - left.score || left.meetingId.localeCompare(right.meetingId))
    .slice(0, limit);

  mkdirSync(dirname(outputBasePath), { recursive: true });
  writeFileSync(`${outputBasePath}.json`, `${JSON.stringify(selected, null, 2)}\n`, "utf8");
  writeFileSync(`${outputBasePath}.md`, renderMarkdown(selected), "utf8");

  console.log(`cleanup review json -> ${outputBasePath}.json`);
  console.log(`cleanup review markdown -> ${outputBasePath}.md`);
  console.log(`examples -> ${selected.length}`);
}

function parseArgs(argv) {
  let sourceDir = resolve(viewerDir, "exports", "viewer-demo", "meetings");
  let outputBasePath = resolve(viewerDir, "exports", "cleanup-review-samples");
  let limit = 24;

  for (let index = 0; index < argv.length; index += 1) {
    if (argv[index] === "--source-dir") {
      sourceDir = resolve(viewerDir, argv[index + 1]);
      index += 1;
      continue;
    }
    if (argv[index] === "--output") {
      outputBasePath = resolve(viewerDir, argv[index + 1]);
      index += 1;
      continue;
    }
    if (argv[index] === "--limit") {
      limit = Number.parseInt(argv[index + 1] || "", 10) || limit;
      index += 1;
    }
  }

  return {
    sourceDir,
    outputBasePath,
    limit,
  };
}

function computeReviewScore({ sourceText, cleanedText, unmatchedWordCount }) {
  const normalizedSource = normalizeText(sourceText);
  const normalizedCleaned = normalizeText(cleanedText);
  if (!normalizedSource) {
    return 0;
  }

  let score = 0;
  score += unmatchedWordCount * 50;
  if (/\b(um|uh|i mean|you know|like)\b/i.test(sourceText)) {
    score += 15;
  }
  if (/\b(\w+)\s+\1\b/i.test(sourceText)) {
    score += 20;
  }
  if (sourceText.trim() !== cleanedText.trim()) {
    score += 10;
  }
  score += Math.min(30, Math.abs(countWords(sourceText) - countWords(cleanedText)) * 4);
  score += Math.min(30, estimateTokenDifference(normalizedSource, normalizedCleaned));
  score += Math.min(10, Math.max(0, countWords(sourceText) - 24));
  return score;
}

function normalizeText(value) {
  return String(value || "")
    .toLowerCase()
    .replace(/[^\p{L}\p{N}'\s]+/gu, " ")
    .replace(/\s+/g, " ")
    .trim();
}

function countWords(value) {
  return normalizeText(value).split(/\s+/).filter(Boolean).length;
}

function estimateTokenDifference(left, right) {
  const leftWords = left.split(/\s+/).filter(Boolean);
  const rightWords = right.split(/\s+/).filter(Boolean);
  const shared = new Set(leftWords.filter((word) => rightWords.includes(word)));
  return (leftWords.length - shared.size) + (rightWords.length - shared.size);
}

function renderMarkdown(examples) {
  const lines = [
    "# Cleanup Review Samples",
    "",
    "Review the cleaned text against the source ASR text. These are selected from local portable `.opus` files.",
    "",
  ];

  examples.forEach((example, index) => {
    lines.push(`## ${index + 1}. ${example.meetingId} @ ${formatClockTime(example.startMs)}`);
    lines.push("");
    lines.push(`- Speaker: ${example.speakerLabel}`);
    lines.push(`- Timing coverage: ${(example.timingCoverage * 100).toFixed(1)}% (${example.timedWordCount}/${example.wordCount})`);
    lines.push(`- Unmatched cleaned tokens: ${example.unmatchedWordTokens.length > 0 ? example.unmatchedWordTokens.join(", ") : "none"}`);
    lines.push("");
    lines.push("### Source ASR");
    lines.push("");
    lines.push(example.sourceText);
    lines.push("");
    lines.push("### Cleaned");
    lines.push("");
    lines.push(example.cleanedText);
    lines.push("");
  });

  return `${lines.join("\n")}\n`;
}

function formatClockTime(ms) {
  const totalSeconds = Math.max(0, Math.floor(ms / 1000));
  const minutes = Math.floor(totalSeconds / 60);
  const seconds = totalSeconds % 60;
  return `${minutes}:${String(seconds).padStart(2, "0")}`;
}
