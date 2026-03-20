import { execFileSync } from "node:child_process";
import { existsSync, readFileSync, unlinkSync } from "node:fs";
import { tmpdir } from "node:os";
import { resolve, basename, join } from "node:path";
import { gunzipSync } from "node:zlib";

import {
  buildDisplayTranscriptFromArtifacts,
  buildReadableTranscriptFromPortable,
  buildTranscriptWordsFromPortable,
} from "./export-static-meetings.mjs";

const DEFAULT_BEFORE_MS = 600;
const DEFAULT_AFTER_MS = 1400;
const DEFAULT_MODEL = "gpt-4o-mini-transcribe";

if (process.argv[1] && resolve(process.argv[1]) === new URL(import.meta.url).pathname) {
  await main();
}

export async function main(argv = process.argv.slice(2)) {
  const options = parseArgs(argv);
  const manifest = loadPortableManifestWithDisplay(options.audioPath);
  const match = findTargetToken(manifest, options.snippet, options.word, options.occurrence);
  if (!match.hasTiming) {
    printAudit({ options, manifest, match, transcription: null });
    return;
  }
  const clipPath = extractAudioClip({
    audioPath: options.audioPath,
    startMs: Math.max(0, match.token.startMs - options.beforeMs),
    durationMs: options.beforeMs + options.afterMs,
  });

  try {
    const transcription = await transcribeClip(clipPath, options.model);
    printAudit({ options, manifest, match, transcription });
  } finally {
    if (existsSync(clipPath)) {
      unlinkSync(clipPath);
    }
  }
}

function parseArgs(argv) {
  let audioPath = "";
  let snippet = "";
  let word = "";
  let occurrence = 1;
  let beforeMs = DEFAULT_BEFORE_MS;
  let afterMs = DEFAULT_AFTER_MS;
  let model = DEFAULT_MODEL;

  for (let index = 0; index < argv.length; index += 1) {
    const arg = argv[index];
    if (arg === "--audio") {
      audioPath = resolveRequiredValue(argv, index, "--audio");
      index += 1;
      continue;
    }
    if (arg === "--snippet") {
      snippet = resolveRequiredValue(argv, index, "--snippet");
      index += 1;
      continue;
    }
    if (arg === "--word") {
      word = resolveRequiredValue(argv, index, "--word");
      index += 1;
      continue;
    }
    if (arg === "--occurrence") {
      occurrence = Number.parseInt(resolveRequiredValue(argv, index, "--occurrence"), 10);
      index += 1;
      continue;
    }
    if (arg === "--before-ms") {
      beforeMs = Number.parseInt(resolveRequiredValue(argv, index, "--before-ms"), 10);
      index += 1;
      continue;
    }
    if (arg === "--after-ms") {
      afterMs = Number.parseInt(resolveRequiredValue(argv, index, "--after-ms"), 10);
      index += 1;
      continue;
    }
    if (arg === "--model") {
      model = resolveRequiredValue(argv, index, "--model");
      index += 1;
      continue;
    }
    throw new Error(`unknown argument: ${arg}`);
  }

  if (!audioPath) {
    throw new Error("--audio is required");
  }
  if (!snippet) {
    throw new Error("--snippet is required");
  }
  if (!word) {
    throw new Error("--word is required");
  }
  if (!Number.isInteger(occurrence) || occurrence <= 0) {
    throw new Error("--occurrence must be a positive integer");
  }
  if (!Number.isInteger(beforeMs) || beforeMs < 0) {
    throw new Error("--before-ms must be a non-negative integer");
  }
  if (!Number.isInteger(afterMs) || afterMs <= 0) {
    throw new Error("--after-ms must be a positive integer");
  }

  return {
    audioPath: resolve(audioPath),
    snippet,
    word,
    occurrence,
    beforeMs,
    afterMs,
    model,
  };
}

function resolveRequiredValue(argv, index, flag) {
  const value = argv[index + 1];
  if (!value) {
    throw new Error(`missing value for ${flag}`);
  }
  return value;
}

function loadPortableManifest(audioPath) {
  if (!existsSync(audioPath)) {
    throw new Error(`audio file not found: ${audioPath}`);
  }
  const report = JSON.parse(execFileSync(
    "ffprobe",
    ["-v", "error", "-show_entries", "format_tags:stream_tags", "-of", "json", audioPath],
    { encoding: "utf8" },
  ));
  const tags = { ...(report.format?.tags || {}) };
  for (const stream of report.streams || []) {
    Object.assign(tags, stream.tags || {});
  }
  const chunkCount = Number(tags.CASSINI_PAYLOAD_CHUNK_COUNT || 0);
  if (!Number.isInteger(chunkCount) || chunkCount <= 0) {
    throw new Error("missing or invalid CASSINI_PAYLOAD_CHUNK_COUNT");
  }
  let encoded = "";
  for (let index = 0; index < chunkCount; index += 1) {
    const key = `CASSINI_PAYLOAD_${String(index).padStart(3, "0")}`;
    const chunk = tags[key];
    if (!chunk) {
      throw new Error(`missing payload chunk ${key}`);
    }
    encoded += chunk;
  }
  return JSON.parse(gunzipSync(Buffer.from(encoded, "base64url")).toString("utf8"));
}

function loadPortableManifestWithDisplay(audioPath) {
  const manifest = loadPortableManifest(audioPath);
  if (Array.isArray(manifest?.displayTranscript?.blocks) && manifest.displayTranscript.blocks.length > 0) {
    return manifest;
  }
  const transcript = buildTranscriptWordsFromPortable(manifest);
  const readable = buildReadableTranscriptFromPortable(manifest, transcript);
  return {
    ...manifest,
    displayTranscript: buildDisplayTranscriptFromArtifacts(transcript, readable),
  };
}

function findTargetToken(manifest, snippet, word, occurrence) {
  const blocks = Array.isArray(manifest?.displayTranscript?.blocks) ? manifest.displayTranscript.blocks : [];
  const normalizedTarget = normalizeAlignmentToken(word);
  let seen = 0;

  for (let blockIndex = 0; blockIndex < blocks.length; blockIndex += 1) {
    const block = blocks[blockIndex];
    if (typeof block?.text !== "string" || !block.text.includes(snippet)) {
      continue;
    }
    const tokens = Array.isArray(block?.tokens) ? block.tokens : [];
    for (let tokenIndex = 0; tokenIndex < tokens.length; tokenIndex += 1) {
      const token = tokens[tokenIndex];
      if (normalizeAlignmentToken(token?.text) !== normalizedTarget) {
        continue;
      }
      seen += 1;
      if (seen !== occurrence) {
        continue;
      }
      return {
        block,
        blockIndex,
        token,
        tokenIndex,
        hasTiming: Number.isInteger(token?.startMs) && Number.isInteger(token?.endMs),
        contextTokens: tokens.slice(Math.max(0, tokenIndex - 6), Math.min(tokens.length, tokenIndex + 7)),
        nearbyTranscriptItems: findNearbyTranscriptItems(
          manifest,
          Number.isInteger(token?.startMs) ? token.startMs : safeToInt(block?.startMs, 0),
          1200,
        ),
      };
    }
  }

  throw new Error(`could not find occurrence ${occurrence} of ${JSON.stringify(word)} inside snippet ${JSON.stringify(snippet)}`);
}

function findNearbyTranscriptItems(manifest, centerMs, radiusMs) {
  const items = Array.isArray(manifest?.transcript?.items) ? manifest.transcript.items : [];
  return items.filter((item) => {
    const startMs = safeToInt(item?.startMs, Number.NaN);
    const endMs = safeToInt(item?.endMs, startMs);
    return Number.isFinite(startMs) && Number.isFinite(endMs) && endMs >= centerMs - radiusMs && startMs <= centerMs + radiusMs;
  });
}

function extractAudioClip({ audioPath, startMs, durationMs }) {
  const clipPath = join(tmpdir(), `cassini-audit-${process.pid}-${Date.now()}-${basename(audioPath, ".opus")}.wav`);
  execFileSync("ffmpeg", [
    "-v",
    "error",
    "-y",
    "-ss",
    (startMs / 1000).toFixed(3),
    "-t",
    (durationMs / 1000).toFixed(3),
    "-i",
    audioPath,
    "-ac",
    "1",
    "-ar",
    "16000",
    clipPath,
  ]);
  return clipPath;
}

async function transcribeClip(clipPath, model) {
  const apiKey = process.env.OPENAI_API_KEY;
  if (!apiKey) {
    throw new Error("OPENAI_API_KEY is not set");
  }
  const buffer = readFileSync(clipPath);
  const file = new File([buffer], basename(clipPath), { type: "audio/wav" });
  const form = new FormData();
  form.set("model", model);
  form.set("file", file);

  const response = await fetch("https://api.openai.com/v1/audio/transcriptions", {
    method: "POST",
    headers: {
      Authorization: `Bearer ${apiKey}`,
    },
    body: form,
  });
  if (!response.ok) {
    throw new Error(`transcription request failed: ${response.status} ${await response.text()}`);
  }
  return await response.json();
}

function printAudit({ options, match, transcription }) {
  const normalizedTarget = normalizeAlignmentToken(options.word);
  const heardText = typeof transcription?.text === "string" ? transcription.text.trim() : "";
  const heardWords = extractNormalizedWords(heardText);
  const heardContainsTarget = heardWords.includes(normalizedTarget);
  const contextWords = extractNormalizedWords(match.contextTokens.map((token) => token?.text ?? "").join(" "));
  const contextOverlap = computeWordOverlap(heardWords, contextWords);

  console.log(`audio: ${options.audioPath}`);
  console.log(`snippet: ${JSON.stringify(options.snippet)}`);
  console.log(`target: ${JSON.stringify(options.word)} occurrence=${options.occurrence}`);
  console.log(`block: ${match.blockIndex} ${match.block.startMs}-${match.block.endMs}`);
  console.log(
    `token: [${match.tokenIndex}] ${JSON.stringify(match.token.text)} ${match.token.startMs}-${match.token.endMs} alignment=${match.token.alignment ?? "unset"} source=${match.token.sourceWordIds?.join(",") || "-"}`,
  );
  if (!match.hasTiming) {
    console.log("token_is_timed: no");
    console.log(`context_tokens: ${match.contextTokens.map((token) => token?.text ?? "").join(" ")}`);
    console.log("nearby_transcript_items:");
    for (const item of match.nearbyTranscriptItems) {
      console.log(`  ${item.startMs}-${item.endMs} ${JSON.stringify(item.text ?? "")}`);
    }
    return;
  }
  console.log("token_is_timed: yes");
  console.log(`heard: ${JSON.stringify(heardText)}`);
  console.log(`heard_contains_target: ${heardContainsTarget ? "yes" : "no"}`);
  console.log(`heard_context_overlap: ${contextOverlap.matched}/${contextOverlap.total}`);
  console.log(`context_tokens: ${match.contextTokens.map((token) => token?.text ?? "").join(" ")}`);
  console.log("nearby_transcript_items:");
  for (const item of match.nearbyTranscriptItems) {
    console.log(`  ${item.startMs}-${item.endMs} ${JSON.stringify(item.text ?? "")}`);
  }
}

function extractNormalizedWords(text) {
  return String(text ?? "")
    .split(/\s+/)
    .map((part) => normalizeAlignmentToken(part))
    .filter(Boolean);
}

function computeWordOverlap(heardWords, expectedWords) {
  const expectedSet = new Set(expectedWords);
  const matched = heardWords.filter((word) => expectedSet.has(word)).length;
  return {
    matched,
    total: Math.max(heardWords.length, 1),
  };
}

function normalizeAlignmentToken(token) {
  return String(token ?? "")
    .replaceAll("’", "'")
    .toLowerCase()
    .replace(/^[^\w']+|[^\w']+$/gu, "");
}

function safeToInt(value, fallback) {
  const number = typeof value === "number" ? value : Number.parseInt(String(value ?? ""), 10);
  return Number.isFinite(number) ? Math.trunc(number) : fallback;
}
