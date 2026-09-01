import { existsSync, mkdirSync, readdirSync, readFileSync, writeFileSync } from "node:fs";
import { dirname, join, resolve } from "node:path";

import { describeMeeting, describeSpeechToTextVariant } from "./export-static-meetings.mjs";

if (process.argv[1] && resolve(process.argv[1]) === new URL(import.meta.url).pathname) {
  main();
}

export function main(argv = process.argv.slice(2)) {
  const { artifactRoot, outputPath } = parseArgs(argv);
  if (!existsSync(artifactRoot)) {
    throw new Error(`artifact root not found: ${artifactRoot}`);
  }

  const comparisons = buildComparisons(artifactRoot);
  if (comparisons.length === 0) {
    throw new Error(`no comparable artifacts found in ${artifactRoot}`);
  }

  mkdirSync(dirname(outputPath), { recursive: true });
  writeFileSync(outputPath, buildHtmlDocument(comparisons), "utf8");
  console.log(`cleanup comparison -> ${outputPath}`);
}

export function parseArgs(argv) {
  let artifactRoot = "";
  let outputPath = "";

  for (let index = 0; index < argv.length; index += 1) {
    const arg = argv[index];
    if (arg === "--artifact-root") {
      artifactRoot = resolve(argv[index + 1] || "");
      index += 1;
      continue;
    }
    if (arg === "--output-path") {
      outputPath = resolve(argv[index + 1] || "");
      index += 1;
      continue;
    }
  }

  if (!artifactRoot) {
    throw new Error("missing --artifact-root");
  }
  if (!outputPath) {
    throw new Error("missing --output-path");
  }

  return { artifactRoot, outputPath };
}

export function buildComparisons(artifactRoot) {
  const entries = readdirSync(artifactRoot, { withFileTypes: true })
    .filter((entry) => entry.isDirectory() && !entry.name.startsWith("."))
    .map((entry) => buildArtifactComparison(artifactRoot, entry.name))
    .filter(Boolean)
    .sort((left, right) => left.id.localeCompare(right.id));

  return entries;
}

export function buildArtifactComparison(artifactRoot, artifactId) {
  const artifactDir = join(artifactRoot, artifactId);
  const manifestPath = join(artifactDir, "manifest.json");
  const transcriptPath = join(artifactDir, "transcript.words.v1.json");
  const readablePath = join(artifactDir, "transcript.readable.v1.json");

  if (!existsSync(manifestPath) || !existsSync(transcriptPath) || !existsSync(readablePath)) {
    return null;
  }

  const manifest = JSON.parse(readFileSync(manifestPath, "utf8"));
  const transcript = JSON.parse(readFileSync(transcriptPath, "utf8"));
  const readable = JSON.parse(readFileSync(readablePath, "utf8"));
  const speakerLabels = new Map(
    Array.isArray(transcript.speakers)
      ? transcript.speakers.map((speaker) => [safeString(speaker?.id), safeString(speaker?.label) || safeString(speaker?.id)])
      : [],
  );
  const transcriptSegments = Array.isArray(transcript.segments) ? transcript.segments : [];
  const segmentById = new Map(
    transcriptSegments
      .filter((segment) => typeof segment?.id === "string" && segment.id.trim() !== "")
      .map((segment) => [segment.id, segment]),
  );
  const readableSegments = Array.isArray(readable.segments) ? readable.segments : [];
  const meeting = describeMeeting(artifactId);
  const variantLabel = describeSpeechToTextVariant(manifest) || "Unknown";
  const comparisons = readableSegments.map((segment, index) => {
    const sourceSegmentIds = Array.isArray(segment?.sourceSegmentIds)
      ? segment.sourceSegmentIds.filter((value) => typeof value === "string" && value.trim() !== "")
      : [];
    const sourceSegments = resolveSourceSegments({
      sourceSegmentIds,
      segmentById,
      transcriptSegments,
      startMs: safeInt(segment?.startMs, 0),
      endMs: safeInt(segment?.endMs, 0),
    });
    const rawText = normalizeRawText(sourceSegments.map((sourceSegment) => safeString(sourceSegment?.text)).join(" "));
    const cleanedText = safeString(segment?.text);
    return {
      id: safeString(segment?.id) || `cmp_${String(index).padStart(4, "0")}`,
      speakerLabel: speakerLabels.get(safeString(segment?.speaker)) || "Unknown speaker",
      startMs: safeInt(segment?.startMs, 0),
      endMs: safeInt(segment?.endMs, 0),
      rawText,
      cleanedText,
    };
  });

  return {
    id: artifactId,
    title: meeting.title,
    dateLabel: meeting.dateLabel,
    variantLabel,
    speakerCount: safeInt(manifest?.speakerCount, 0),
    segmentCount: safeInt(manifest?.segmentCount, transcriptSegments.length),
    digestDurationMs: safeInt(manifest?.digestDurationMs, transcript?.media?.durationMs || 0),
    stt: manifest?.provenance?.speechToText || {},
    cleanup: manifest?.provenance?.readableCleanup || {},
    viewerPath: `./?meeting=${encodeURIComponent(artifactId)}`,
    comparisons,
  };
}

export function resolveSourceSegments({ sourceSegmentIds, segmentById, transcriptSegments, startMs, endMs }) {
  const byId = sourceSegmentIds
    .map((segmentId) => segmentById.get(segmentId))
    .filter(Boolean);
  if (byId.length > 0) {
    return byId;
  }
  return transcriptSegments.filter((segment) => {
    const segmentStart = safeInt(segment?.startMs, 0);
    const segmentEnd = safeInt(segment?.endMs, segmentStart);
    return segmentEnd >= startMs && segmentStart <= endMs;
  });
}

export function normalizeRawText(text) {
  return safeString(text)
    .replace(/\s+/g, " ")
    .replace(/\s+([,.;!?])/g, "$1")
    .trim();
}

export function buildHtmlDocument(comparisons) {
  const dataJson = JSON.stringify(comparisons);
  return `<!doctype html>
<html lang="en">
  <head>
    <meta charset="UTF-8" />
    <meta name="viewport" content="width=device-width, initial-scale=1.0" />
    <title>Cassini Cleanup Comparison</title>
    <style>
      :root {
        color-scheme: light;
        --paper: #f8f3e7;
        --card: #fffdfa;
        --ink: #2f2418;
        --muted: #7d6d5e;
        --line: rgba(120, 92, 54, 0.18);
        --accent: #b56e2a;
        --raw-bg: #fcf0e6;
        --clean-bg: #eef6ea;
        --raw-border: #e2b087;
        --clean-border: #97c28d;
      }

      * { box-sizing: border-box; }

      body {
        margin: 0;
        font-family: "Iowan Old Style", "Palatino Linotype", "Book Antiqua", serif;
        color: var(--ink);
        background:
          radial-gradient(circle at top right, rgba(181, 110, 42, 0.12), transparent 26rem),
          linear-gradient(180deg, #fbf7ef 0%, var(--paper) 100%);
      }

      a { color: inherit; }

      .shell {
        width: min(1420px, calc(100vw - 32px));
        margin: 24px auto 48px;
      }

      .masthead {
        display: grid;
        gap: 12px;
        padding: 20px 22px;
        border: 1px solid var(--line);
        border-radius: 22px;
        background: rgba(255, 253, 250, 0.94);
        box-shadow: 0 18px 40px rgba(95, 70, 42, 0.08);
      }

      .eyebrow {
        margin: 0;
        font: 600 0.76rem/1.2 ui-monospace, SFMono-Regular, Menlo, monospace;
        letter-spacing: 0.18em;
        text-transform: uppercase;
        color: var(--accent);
      }

      h1 {
        margin: 0;
        font-size: clamp(2rem, 4vw, 3.2rem);
        line-height: 0.95;
      }

      .summary {
        margin: 0;
        max-width: 72ch;
        color: var(--muted);
        font-size: 1rem;
        line-height: 1.45;
      }

      .toolbar {
        display: grid;
        gap: 12px;
        grid-template-columns: minmax(0, 460px) auto;
        align-items: end;
      }

      label {
        display: grid;
        gap: 6px;
        font: 600 0.78rem/1.2 ui-monospace, SFMono-Regular, Menlo, monospace;
        letter-spacing: 0.08em;
        text-transform: uppercase;
        color: var(--muted);
      }

      select {
        width: 100%;
        padding: 12px 14px;
        border: 1px solid var(--line);
        border-radius: 14px;
        background: var(--card);
        color: var(--ink);
        font: 500 1rem/1.2 "Avenir Next", "Segoe UI", sans-serif;
      }

      .artifact-link {
        justify-self: start;
        padding: 12px 14px;
        border-radius: 14px;
        border: 1px solid var(--line);
        background: var(--card);
        text-decoration: none;
        font: 600 0.9rem/1.2 "Avenir Next", "Segoe UI", sans-serif;
      }

      .meta {
        display: flex;
        flex-wrap: wrap;
        gap: 8px;
      }

      .pill {
        display: inline-flex;
        align-items: center;
        padding: 8px 12px;
        border-radius: 999px;
        border: 1px solid var(--line);
        background: rgba(255, 255, 255, 0.72);
        color: var(--muted);
        font: 600 0.8rem/1 "Avenir Next", "Segoe UI", sans-serif;
      }

      .pill strong {
        margin-right: 6px;
        color: var(--ink);
      }

      .grid {
        display: grid;
        gap: 14px;
        margin-top: 18px;
      }

      .row {
        display: grid;
        gap: 14px;
        grid-template-columns: 132px minmax(0, 1fr) minmax(0, 1fr);
        align-items: start;
      }

      .stamp {
        position: sticky;
        top: 20px;
        display: grid;
        gap: 8px;
        padding: 12px;
        border-radius: 16px;
        border: 1px solid var(--line);
        background: rgba(255, 252, 247, 0.92);
      }

      .speaker {
        display: inline-flex;
        align-items: center;
        justify-content: center;
        width: fit-content;
        padding: 6px 10px;
        border-radius: 999px;
        border: 1px solid var(--line);
        background: #f5ede1;
        font: 700 0.82rem/1 "Avenir Next", "Segoe UI", sans-serif;
      }

      .time {
        font: 700 1rem/1 ui-monospace, SFMono-Regular, Menlo, monospace;
      }

      .column {
        min-width: 0;
        padding: 18px 18px 16px;
        border-radius: 18px;
        border: 1px solid var(--line);
        background: var(--card);
        box-shadow: 0 12px 26px rgba(95, 70, 42, 0.06);
      }

      .column.raw {
        background: linear-gradient(180deg, #fff8f2 0%, var(--raw-bg) 100%);
        border-color: var(--raw-border);
      }

      .column.clean {
        background: linear-gradient(180deg, #f8fff6 0%, var(--clean-bg) 100%);
        border-color: var(--clean-border);
      }

      .column h2 {
        margin: 0 0 10px;
        font: 700 0.84rem/1.2 ui-monospace, SFMono-Regular, Menlo, monospace;
        letter-spacing: 0.14em;
        text-transform: uppercase;
      }

      .column p {
        margin: 0;
        font-size: 1.03rem;
        line-height: 1.52;
        white-space: pre-wrap;
      }

      .empty {
        padding: 18px;
        border-radius: 18px;
        border: 1px dashed var(--line);
        color: var(--muted);
      }

      @media (max-width: 980px) {
        .toolbar {
          grid-template-columns: 1fr;
        }

        .row {
          grid-template-columns: 1fr;
        }

        .stamp {
          position: static;
          grid-template-columns: auto auto;
          align-items: center;
        }
      }
    </style>
  </head>
  <body>
    <div class="shell">
      <section class="masthead">
        <p class="eyebrow">Cassini Comparison</p>
        <h1>Raw ASR vs Cleaned Transcript</h1>
        <p class="summary">
          Left column: raw ASR reconstructed from the exact source spans used by the cleaned transcript.
          Right column: readable cleanup generated with the Cassini cleanup model.
        </p>
        <div class="toolbar">
          <label>
            Artifact
            <select id="artifact-select"></select>
          </label>
          <a id="viewer-link" class="artifact-link" href="./">Open Meeting Viewer</a>
        </div>
        <div id="meta" class="meta"></div>
      </section>
      <main id="rows" class="grid"></main>
    </div>
    <script id="comparison-data" type="application/json">${escapeScriptJson(dataJson)}</script>
    <script>
      const data = JSON.parse(document.getElementById("comparison-data").textContent);
      const artifactSelect = document.getElementById("artifact-select");
      const viewerLink = document.getElementById("viewer-link");
      const meta = document.getElementById("meta");
      const rows = document.getElementById("rows");

      for (const artifact of data) {
        const option = document.createElement("option");
        option.value = artifact.id;
        option.textContent = \`\${artifact.title} | \${artifact.dateLabel} | \${artifact.variantLabel}\`;
        artifactSelect.append(option);
      }

      artifactSelect.addEventListener("change", () => renderArtifact(artifactSelect.value));
      renderArtifact(data[0]?.id || "");

      function renderArtifact(artifactId) {
        const artifact = data.find((entry) => entry.id === artifactId);
        if (!artifact) {
          rows.innerHTML = '<div class="empty">No artifact selected.</div>';
          meta.innerHTML = "";
          viewerLink.href = "./";
          return;
        }

        document.title = \`\${artifact.title} (\${artifact.variantLabel}) | Cassini Cleanup Comparison\`;
        viewerLink.href = artifact.viewerPath;
        meta.innerHTML = [
          pill("Meeting", \`\${artifact.title} · \${artifact.dateLabel}\`),
          pill("Variant", artifact.variantLabel),
          pill("Speakers", String(artifact.speakerCount)),
          pill("Passages", String(artifact.comparisons.length)),
          pill("STT", [artifact.stt.engine, artifact.stt.model].filter(Boolean).join(" · ") || "Unknown"),
          pill("Cleanup", [artifact.cleanup.model, artifact.cleanup.host].filter(Boolean).join(" · ") || "Unknown"),
        ].join("");

        rows.innerHTML = artifact.comparisons.map((item) => {
          return \`
            <section class="row">
              <aside class="stamp">
                <div class="speaker">\${escapeHtml(item.speakerLabel)}</div>
                <div class="time">\${formatClock(item.startMs)}</div>
              </aside>
              <article class="column raw">
                <h2>Raw ASR</h2>
                <p>\${escapeHtml(item.rawText || "(empty)")}</p>
              </article>
              <article class="column clean">
                <h2>Cleaned</h2>
                <p>\${escapeHtml(item.cleanedText || "(empty)")}</p>
              </article>
            </section>
          \`;
        }).join("");
      }

      function pill(label, value) {
        return \`<span class="pill"><strong>\${escapeHtml(label)}:</strong>\${escapeHtml(value)}</span>\`;
      }

      function formatClock(ms) {
        const totalSeconds = Math.max(0, Math.floor(ms / 1000));
        const hours = Math.floor(totalSeconds / 3600);
        const minutes = Math.floor((totalSeconds % 3600) / 60);
        const seconds = totalSeconds % 60;
        if (hours > 0) {
          return [hours, minutes, seconds].map((part, index) => String(part).padStart(index === 0 ? 1 : 2, "0")).join(":");
        }
        return \`\${String(minutes).padStart(1, "0")}:\${String(seconds).padStart(2, "0")}\`;
      }

      function escapeHtml(value) {
        return String(value)
          .replaceAll("&", "&amp;")
          .replaceAll("<", "&lt;")
          .replaceAll(">", "&gt;")
          .replaceAll('"', "&quot;");
      }
    </script>
  </body>
</html>
`;
}

export function escapeScriptJson(json) {
  return json.replaceAll("<", "\\u003c");
}

export function safeString(value) {
  return typeof value === "string" ? value.trim() : "";
}

export function safeInt(value, fallback) {
  return Number.isFinite(Number(value)) ? Number(value) : fallback;
}
