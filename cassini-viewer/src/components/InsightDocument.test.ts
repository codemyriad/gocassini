import { describe, expect, it } from "vitest";
import { render } from "svelte/server";

import InsightDocument from "./InsightDocument.svelte";
import insightDocumentSource from "./InsightDocument.svelte?raw";
import type { InsightRecord } from "../viewer/insights";

// Source-level assertions, for the reason MeetingList.test.ts gives: the suite
// runs in node with no DOM harness.

describe("InsightDocument", () => {
  it("reads in the order the panel exists in: question, material, answer", () => {
    const question = insightDocumentSource.indexOf('class="ins-question"');
    const sources = insightDocumentSource.indexOf('class="ins-sources"');
    const answer = insightDocumentSource.indexOf("documentHtml}");
    expect(question).toBeGreaterThan(-1);
    expect(sources).toBeGreaterThan(question);
    expect(answer).toBeGreaterThan(sources);
  });

  it("renders the stored source set rather than deriving one", () => {
    // The prototype fabricated its sources by bucketing the catalog by room and
    // taking the first four. The real set is on the record, resolved by the
    // shell to what this caller may read, and passed in.
    expect(insightDocumentSource).toContain("export let sources: MeetingCatalogEntry[] = [];");
    expect(insightDocumentSource).toContain("{#each sources as source (source.id)}");
    expect(insightDocumentSource).toContain("Context from {sources.length}");
  });

  it("opens each source as itself, in the sheet", () => {
    expect(insightDocumentSource).toContain('dispatch("openSource", source)');
  });

  it("says nothing at all about material the caller cannot see", () => {
    // The section is gated on there being a readable source: "0 meetings", or a
    // count taken from the record, would disclose that meetings exist which
    // this caller may not read.
    expect(insightDocumentSource).toContain("{#if sources.length > 0}");
    expect(insightDocumentSource).not.toContain("meetingIds");
  });

  it("shows the provenance a re-run needs to be told apart", () => {
    // The prototype rendered four of the record's fields. Creator, status and
    // the workflow that ran are what make two attempts distinguishable.
    expect(insightDocumentSource).toContain("insight.createdBy");
    expect(insightDocumentSource).toContain("formatInsightStatus(insight.status)");
    expect(insightDocumentSource).toContain("insight.workflowId");
    expect(insightDocumentSource).toContain("insight.workflowVersion");
    expect(insightDocumentSource).toContain("insight.attemptNumber > 1");
  });

  it("gives a run with no answer yet its own honest state, not an empty page", () => {
    expect(insightDocumentSource).toContain("{#if pending}");
    expect(insightDocumentSource).toContain("{:else if failure}");
    expect(insightDocumentSource).toContain("{:else if !canLoadDocument}");
    expect(insightDocumentSource).toContain("{:else if documentLoading}");
    expect(insightDocumentSource).toContain("{:else if documentError}");
  });

  it("sanitises the model's markdown before it reaches the DOM", () => {
    // The document is model output. Same two steps the sealed meeting summary
    // is rendered with.
    expect(insightDocumentSource).toContain("marked.parse(body");
    expect(insightDocumentSource).toContain("DOMPurify.sanitize(rawHtml");
  });

  it("cuts the recorder's YAML provenance rather than rendering it as prose", () => {
    // To markdown `---` is a horizontal rule, so the front matter arrived as two
    // rules with a run-on paragraph of quoted hashes and ids between them, above
    // the answer somebody asked for. The facts are shown by the header and the
    // provenance list instead. See insights.test.ts for the cut itself.
    expect(insightDocumentSource).toContain("stripInsightFrontMatter(markdown)");
  });

  it("puts what identifies the run in the header, not under the answer", () => {
    expect(insightDocumentSource).toContain("<h2>{headline}</h2>");
    expect(insightDocumentSource).toContain('class="ins-head-meta"');
    expect(insightDocumentSource).toContain("formatInsightCreated(insight)");
    expect(insightDocumentSource).toContain("{insight.model}");
  });

  it("never names a room whose meeting this caller cannot see", () => {
    // Derived from the resolved sources, not record.roomIds: a source they may
    // not read is absent, and naming its room would disclose it.
    expect(insightDocumentSource).toContain(
      "sources.map((source) => roomLabelOf(source))",
    );
    expect(insightDocumentSource).not.toContain("insight.roomIds");
  });

  describe("a failed run, rendered", () => {
    // Rendered, not read: the words a reader gets for a failure are the
    // behaviour, and the token the operator puts on `error` is the one thing
    // that must not reach them.
    function failedRun(error: string): InsightRecord {
      return {
        id: "ins_0123456789abcdef",
        status: "failed",
        createdBy: "alice",
        attemptNumber: 2,
        workflowId: "summarise",
        workflowVersion: "v0",
        workflowSha256: "abc",
        meetingIds: ["m1"],
        roomIds: ["r1"],
        question: "What was decided?",
        provider: "hosted",
        model: "",
        documentPath: "",
        error,
        createdAt: "2026-09-03T10:00:00Z",
        updatedAt: "2026-09-03T10:00:00Z",
      };
    }
    const plainText = (html: string) => html.replace(/<[^>]*>/g, " ").replace(/\s+/g, " ");

    it("says why in sentences, and never prints the operator's token", () => {
      const html = render(InsightDocument, {
        props: { insight: failedRun("provider-refused: HTTP 401 Unauthorized") },
      }).body;
      const text = plainText(html);
      expect(text).toContain("The endpoint rejected the request");
      expect(text).toContain("The operator reported: HTTP 401 Unauthorized");
      expect(text).not.toContain("provider-refused");
    });

    it("offers Retry exactly where the provider can perform one", () => {
      const withRetry = render(InsightDocument, {
        props: { insight: failedRun("model-failed: timeout"), canRetry: true },
      }).body;
      expect(withRetry).toMatch(/<button[^>]*>\s*Retry\s*<\/button>/);
      expect(plainText(withRetry)).toContain("Runs again on the endpoint this insight asked for.");

      const without = render(InsightDocument, {
        props: { insight: failedRun("model-failed: timeout"), canRetry: false },
      }).body;
      expect(without).not.toMatch(/>\s*Retry\s*</);
    });

    it("locks the button while a retry is in flight and shows what a refused one said", () => {
      const html = render(InsightDocument, {
        props: {
          insight: failedRun("model-failed: timeout"),
          canRetry: true,
          retrying: true,
          retryError: "That insight is already running.",
        },
      }).body;
      expect(html).toMatch(/<button[^>]*disabled[^>]*>\s*Retrying…\s*<\/button>/);
      expect(plainText(html)).toContain("That insight is already running.");
    });
  });

  it("dispatches retry rather than performing one", () => {
    expect(insightDocumentSource).toContain('dispatch("retry")');
    expect(insightDocumentSource).not.toContain("fetch(");
  });
});
