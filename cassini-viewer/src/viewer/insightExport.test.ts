import { describe, expect, it } from "vitest";
import type { MeetingCatalogEntry } from "./catalog";
import { formatMeetingDateWithDay } from "./catalog";
import { formatInsightCreated, type InsightRecord } from "./insights";
import { exportFileName, renderInsightExport } from "./insightExport";

function insight(overrides: Partial<InsightRecord> = {}): InsightRecord {
  return {
    id: "ins_0123456789abcdef",
    status: "succeeded",
    createdBy: "alice",
    attemptNumber: 2,
    workflowId: "summarise",
    workflowVersion: "v3",
    workflowSha256: "abc",
    meetingIds: [],
    roomIds: [],
    question: "",
    provider: "openai",
    model: "gpt-x",
    documentPath: "Cassini Insights/2026-10-12-summarise-ins_0123456789abcdef.md",
    reason: "",
    error: "",
    createdAt: "2026-10-12T09:00:00Z",
    updatedAt: "2026-10-12T09:01:00Z",
    ...overrides,
  };
}

function meeting(id: string, title = `Meeting ${id}`): MeetingCatalogEntry {
  return { id, title, dateLabel: "2026-10-05 14:30", audioPath: `${id}.opus` };
}

const DOCUMENT = "---\nworkflow: summarise\nmodel: gpt-x\n---\n\n## Answer\n\nThe gist.\n\n";
const link = (source: MeetingCatalogEntry) => `https://cloud.example/viewer#meeting=${source.id}`;
const noLink = () => "";

describe("renderInsightExport", () => {
  it("puts a readable header in place of the front matter", () => {
    const record = insight({ question: "What did we decide?" });
    const out = renderInsightExport({
      record,
      sources: [meeting("m1"), meeting("m2"), meeting("m3")],
      markdown: DOCUMENT,
      meetingUrl: link,
    });
    const day = formatMeetingDateWithDay("2026-10-05 14:30");
    expect(out).toBe(
      [
        "# What did we decide?",
        "",
        '> "What did we decide?"',
        "",
        `Written by Cassini on ${formatInsightCreated(record)}  `,
        "Template: summarise v3 · Model: gpt-x",
        "",
        "Drawn from 3 meetings:",
        "",
        `- [Meeting m1, ${day}](https://cloud.example/viewer#meeting=m1)`,
        `- [Meeting m2, ${day}](https://cloud.example/viewer#meeting=m2)`,
        `- [Meeting m3, ${day}](https://cloud.example/viewer#meeting=m3)`,
        "",
        "---",
        "",
        "## Answer",
        "",
        "The gist.",
        "",
      ].join("\n"),
    );
  });

  it("leaves out the quote when the run had no question", () => {
    const out = renderInsightExport({ record: insight(), sources: [], markdown: "Body", meetingUrl: link });
    expect(out.startsWith("# summarise\n\nWritten by Cassini on ")).toBe(true);
    expect(out).not.toContain(">");
  });

  it("quotes every line of a multi-line question", () => {
    const out = renderInsightExport({
      record: insight({ question: "First?\nSecond?" }),
      sources: [],
      markdown: "Body",
      meetingUrl: link,
    });
    expect(out).toContain('> "First?\n> Second?"');
  });

  it("leaves out the sources section when this reader can see none", () => {
    // Hidden sources are absent from the list, never counted.
    const out = renderInsightExport({ record: insight(), sources: [], markdown: "Body", meetingUrl: link });
    expect(out).not.toContain("Drawn from");
  });

  it("counts one source in the singular", () => {
    const out = renderInsightExport({ record: insight(), sources: [meeting("m1")], markdown: "Body", meetingUrl: link });
    expect(out).toContain("Drawn from 1 meeting:\n");
  });

  it("names a source as plain text where it has no link", () => {
    const out = renderInsightExport({ record: insight(), sources: [meeting("m1")], markdown: "Body", meetingUrl: noLink });
    expect(out).toContain(`\n- Meeting m1, ${formatMeetingDateWithDay("2026-10-05 14:30")}\n`);
    expect(out).not.toContain("](");
  });

  it("escapes brackets in a linked title", () => {
    const out = renderInsightExport({
      record: insight(),
      sources: [meeting("m1", "Standup [draft]")],
      markdown: "Body",
      meetingUrl: link,
    });
    expect(out).toContain("- [Standup \\[draft\\], ");
  });

  it("drops the model when the record has none, and never names provider or attempt", () => {
    const out = renderInsightExport({ record: insight({ model: "" }), sources: [], markdown: "Body", meetingUrl: link });
    expect(out).toContain("\nTemplate: summarise v3\n");
    expect(out).not.toContain("Model:");
    expect(out).not.toContain("openai");
    expect(out).not.toMatch(/attempt/i);
  });

  it("strips the front matter and keeps a document without any as it was", () => {
    const stripped = renderInsightExport({ record: insight(), sources: [], markdown: DOCUMENT, meetingUrl: link });
    expect(stripped).not.toContain("workflow:");
    const plain = renderInsightExport({ record: insight(), sources: [], markdown: "Just text.", meetingUrl: link });
    expect(plain.endsWith("---\n\nJust text.\n")).toBe(true);
  });
});

describe("exportFileName", () => {
  it("names the file after the headline", () => {
    expect(exportFileName(insight())).toBe("summarise.md");
  });

  it("strips the characters a file system refuses", () => {
    expect(exportFileName(insight({ question: 'What: "next" <steps>/now | why?\\*' }))).toBe(
      "What next steps now why.md",
    );
  });

  it("collapses whitespace and control characters", () => {
    expect(exportFileName(insight({ question: "Line one\n\tline\u0007two  " }))).toBe("Line one line two.md");
  });

  it("caps a long headline", () => {
    const name = exportFileName(insight({ question: "a".repeat(300) }));
    expect(name).toBe(`${"a".repeat(100)}.md`);
  });

  it("falls back when nothing usable is left", () => {
    expect(exportFileName(insight({ question: "???", workflowId: "" }))).toBe("insight.md");
    expect(exportFileName(insight({ question: "", workflowId: "" }))).toBe("Insight.md");
  });
});
