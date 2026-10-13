import { describe, expect, it } from "vitest";
import type { MeetingCatalogEntry } from "./catalog";
import { formatMeetingDateWithDay } from "./catalog";
import type { InsightRecord } from "./insights";
import { exportFileName, renderInsightExport } from "./insightExport";

// The copy's own date format: the drawer's, plus the zone, because the file is
// read on somebody else's clock.
function exportCreated(iso: string): string {
  return new Intl.DateTimeFormat("en-GB", {
    day: "numeric",
    month: "short",
    year: "numeric",
    hour: "2-digit",
    minute: "2-digit",
    hour12: false,
    timeZoneName: "short",
  }).format(new Date(iso));
}

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
        `Written by Cassini on ${exportCreated(record.createdAt)}  `,
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

  it("names the zone on the date, which the drawer leaves off", () => {
    const out = renderInsightExport({ record: insight(), sources: [], markdown: DOCUMENT, meetingUrl: noLink });
    // "12 Oct 2026, 09:00 UTC", or whatever the test host's zone is called.
    expect(out).toMatch(/^Written by Cassini on \d{1,2} \w{3,4} \d{4}, \d{2}:\d{2} \S+  $/m);
  });

  it("keeps a multi-line question on one line in the heading", () => {
    const out = renderInsightExport({
      record: insight({ question: "What did we decide?\n\nAnd who owns it?" }),
      sources: [],
      markdown: DOCUMENT,
      meetingUrl: noLink,
    });
    expect(out.startsWith("# What did we decide? And who owns it?\n\nWritten by Cassini")).toBe(true);
  });

  it("keeps a title with a line break on one list item", () => {
    const out = renderInsightExport({
      record: insight(),
      sources: [meeting("m1", "Planning\n\nQ4")],
      markdown: DOCUMENT,
      meetingUrl: link,
    });
    expect(out).toContain(`- [Planning Q4, ${formatMeetingDateWithDay("2026-10-05 14:30")}](https://cloud.example/viewer#meeting=m1)`);
  });

  it("leaves out the quote when the run had no question", () => {
    const out = renderInsightExport({ record: insight(), sources: [], markdown: "Body", meetingUrl: link });
    expect(out.startsWith("# summarise\n\nWritten by Cassini on ")).toBe(true);
    expect(out).not.toContain(">");
  });

  it("quotes every line of a multi-line question", () => {
    const out = renderInsightExport({
      record: insight({ name: "Riverside", question: "First?\nSecond?" }),
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

  it("cuts a long headline between characters, never through one", () => {
    const name = exportFileName(insight({ question: `${"a".repeat(99)}😀b` }));
    expect(name).toBe(`${"a".repeat(99)}😀.md`);
  });

  it("falls back when nothing usable is left, or the name is one Windows reserves", () => {
    expect(exportFileName(insight({ question: "???", workflowId: "" }))).toBe("insight.md");
    expect(exportFileName(insight({ question: "", workflowId: "" }))).toBe("Insight.md");
    expect(exportFileName(insight({ question: "CON" }))).toBe("insight.md");
    expect(exportFileName(insight({ question: "nul?" }))).toBe("insight.md");
    expect(exportFileName(insight({ question: "Console" }))).toBe("Console.md");
  });
});

describe("renderInsightExport says the question once", () => {
  const body = "# What did we decide?\n\nDrawn from 3 meetings.\n\n## Answer\n\nYes.\n";

  it("quotes the question under the sources, only under a name that says something else", () => {
    const named = renderInsightExport({
      record: insight({ name: "Riverside Clinic", question: "What did we decide?" }),
      sources: [meeting("m1")],
      markdown: body,
      meetingUrl: noLink,
    });
    const quoteAt = named.indexOf('> "What did we decide?"');
    expect(quoteAt).toBeGreaterThan(named.indexOf("Drawn from 1 meeting:"));
    expect(quoteAt).toBeLessThan(named.indexOf("\n---\n"));
    // The document's own heading would repeat the quote right under it.
    expect(named.endsWith("---\n\nDrawn from 3 meetings.\n\n## Answer\n\nYes.\n")).toBe(true);
    expect(named.match(/What did we decide\?/g)).toHaveLength(1);
  });

  it("keeps the document's heading as its title when there is no quote", () => {
    const unnamed = renderInsightExport({
      record: insight({ question: "What did we decide?" }),
      sources: [],
      markdown: body,
      meetingUrl: noLink,
    });
    expect(unnamed).not.toContain("> ");
    expect(unnamed.endsWith("---\n\n# What did we decide?\n\nDrawn from 3 meetings.\n\n## Answer\n\nYes.\n")).toBe(true);
  });
});
