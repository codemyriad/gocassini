import { execFileSync } from "node:child_process";
import { existsSync } from "node:fs";
import { resolve } from "node:path";
import { gunzipSync } from "node:zlib";

function main(argv = process.argv.slice(2)) {
  const { audioPath, snippets } = parseArgs(argv);
  const manifest = loadPortableManifest(audioPath);
  const blocks = manifest.displayTranscript?.blocks;
  if (!Array.isArray(blocks) || blocks.length === 0) {
    throw new Error("portable meeting does not contain displayTranscript.blocks");
  }

  if (snippets.length === 0) {
    console.log(`audio: ${audioPath}`);
    console.log(`display blocks: ${blocks.length}`);
    blocks.forEach((block, index) => {
      console.log(`${index}: ${JSON.stringify(block.text ?? "")}`);
    });
    return;
  }

  console.log(`audio: ${audioPath}`);
  console.log(`transcript items: ${manifest.transcript?.items?.length ?? 0}`);
  console.log(`display blocks: ${blocks.length}`);

  for (const snippet of snippets) {
    inspectSnippet(manifest, snippet);
  }
}

function parseArgs(argv) {
  let audioPath = "";
  const snippets = [];
  for (let index = 0; index < argv.length; index += 1) {
    const arg = argv[index];
    if (arg === "--audio") {
      audioPath = resolveRequiredValue(argv, index, "--audio");
      index += 1;
      continue;
    }
    if (arg === "--snippet") {
      snippets.push(resolveRequiredValue(argv, index, "--snippet"));
      index += 1;
      continue;
    }
    throw new Error(`unknown argument: ${arg}`);
  }
  if (!audioPath) {
    throw new Error("--audio is required");
  }
  return { audioPath: resolve(audioPath), snippets };
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

function inspectSnippet(manifest, snippet) {
  const blocks = manifest.displayTranscript.blocks;
  const matchingBlocks = blocks
    .map((block, index) => ({ block, index }))
    .filter(({ block }) => typeof block.text === "string" && block.text.includes(snippet));

  console.log("");
  console.log(`snippet: ${JSON.stringify(snippet)}`);
  if (matchingBlocks.length === 0) {
    console.log("no display block contains that snippet");
    return;
  }

  const snippetTokens = tokenizeDisplayText(snippet).map((token) => token.text);
  for (const { block, index } of matchingBlocks) {
    const blockTokens = Array.isArray(block.tokens) ? block.tokens : [];
    const startIndex = findTokenSequence(blockTokens, snippetTokens);
    console.log(`block ${index}: ${block.startMs}-${block.endMs}`);
    if (startIndex < 0) {
      console.log("  block text contains the snippet, but token-sequence matching failed");
      continue;
    }
    const endIndex = startIndex + snippetTokens.length - 1;
    console.log(`  token span: ${startIndex}-${endIndex}`);
    for (let tokenIndex = startIndex; tokenIndex <= endIndex; tokenIndex += 1) {
      const token = blockTokens[tokenIndex];
      const source = describeTokenSource(manifest, token);
      console.log(
        [
          `  [${tokenIndex}]`,
          JSON.stringify(token.text),
          `kind=${token.kind}`,
          `timing=${formatTiming(token.startMs, token.endMs)}`,
          `alignment=${token.alignment ?? "unset"}`,
          `source=${token.sourceWordIds?.join(",") || "-"}`,
          source ? `origin=${source}` : "",
        ].filter(Boolean).join(" "),
      );
    }
  }
}

function formatTiming(startMs, endMs) {
  if (!Number.isInteger(startMs) || !Number.isInteger(endMs)) {
    return "-";
  }
  return `${startMs}-${endMs}`;
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
      kind: normalizeAlignmentToken(tokenText) === "" ? "punctuation" : "word",
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

function findTokenSequence(tokens, snippetTokenTexts) {
  if (snippetTokenTexts.length === 0) {
    return -1;
  }
  for (let startIndex = 0; startIndex <= tokens.length - snippetTokenTexts.length; startIndex += 1) {
    let matches = true;
    for (let offset = 0; offset < snippetTokenTexts.length; offset += 1) {
      if (tokens[startIndex + offset]?.text !== snippetTokenTexts[offset]) {
        matches = false;
        break;
      }
    }
    if (matches) {
      return startIndex;
    }
  }
  return -1;
}

function describeTokenSource(manifest, token) {
  if (!Array.isArray(token?.sourceWordIds) || token.sourceWordIds.length !== 1) {
    return "";
  }
  const match = /^seg_(\d{6}):w_(\d+)$/.exec(token.sourceWordIds[0]);
  if (!match) {
    return "non-generated";
  }
  const segmentIndex = Number(match[1]);
  const wordIndex = Number(match[2]);
  const item = manifest.transcript?.items?.[segmentIndex];
  if (!item || typeof item.text !== "string") {
    return `generated[segment=${segmentIndex} word=${wordIndex}]`;
  }
  const words = item.text.trim().split(/\s+/).filter(Boolean);
  const span = Math.max(0, Number(item.endMs || 0) - Number(item.startMs || 0));
  const synthetic = words.length > 1;
  const expectedStart = words.length <= 1
    ? Number(item.startMs || 0)
    : Number(item.startMs || 0) + Math.floor((span * wordIndex) / words.length);
  const expectedEnd = words.length <= 1
    ? Number(item.endMs || 0)
    : Number(item.startMs || 0) + Math.floor((span * (wordIndex + 1)) / words.length);
  const wordText = words[wordIndex] ?? "";
  return [
    synthetic ? "synthetic-even-split" : "single-word-item",
    `segment=${segmentIndex}`,
    `word=${wordIndex}/${Math.max(0, words.length - 1)}`,
    `segmentSpan=${item.startMs}-${item.endMs}`,
    `slot=${expectedStart}-${expectedEnd}`,
    wordText ? `wordText=${JSON.stringify(wordText)}` : "",
  ].filter(Boolean).join(" ");
}

if (process.argv[1] && resolve(process.argv[1]) === new URL(import.meta.url).pathname) {
  main();
}
