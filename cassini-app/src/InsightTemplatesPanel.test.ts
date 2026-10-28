import { afterEach, describe, expect, it, vi } from "vitest";

import insightTemplatesPanelSource from "./InsightTemplatesPanel.svelte?raw";
import { shortHash } from "./InsightTemplatesPanel.svelte";
import { OperatorClient, runnableWorkflows } from "./operator/client";

afterEach(() => {
  vi.unstubAllGlobals();
});

function stubWorkflowsResponse(body: unknown, status = 200): void {
  vi.stubGlobal(
    "fetch",
    vi.fn(
      async () =>
        new Response(JSON.stringify(body), {
          status,
          headers: { "Content-Type": "application/json" },
        }),
    ),
  );
}

describe("insight template registry", () => {
  it("carries the id, version and hash an insight document records", async () => {
    stubWorkflowsResponse([
      {
        id: "summarise",
        version: "v0",
        sha256: "eba6bd6674d35522dddbd774d3fc6a1fbcdab025aa30a5993d49216d0c13f59f",
        name: "Meeting summary",
        question: "Summarise what happened, what was decided, and what follows.",
        description: "One document per meeting in a fixed shape.",
        origin: "Built in",
        instruction: "You are a meeting-summary editor.",
      },
    ]);

    const workflows = await new OperatorClient("https://operator.test").listInsightWorkflows();

    expect(workflows).toHaveLength(1);
    expect(workflows[0].id).toBe("summarise");
    expect(workflows[0].version).toBe("v0");
    expect(workflows[0].sha256).toHaveLength(64);
    // The bytes, not a description of them: the panel shows what is sent.
    expect(workflows[0].instruction).toBe("You are a meeting-summary editor.");
  });

  it("keeps a broken custom template with its reason, and drops a row with neither hash nor reason", async () => {
    stubWorkflowsResponse([
      { id: "summarise", version: "v0", sha256: "abc123", origin: "Built in" },
      { id: "roto", origin: "Custom", error: "the front matter needs a version" },
      { id: "mystery", origin: "Custom" },
    ]);

    const workflows = await new OperatorClient("https://operator.test").listInsightWorkflows();

    expect(workflows.map((w) => w.id)).toEqual(["summarise", "roto"]);
    expect(workflows[1].error).toBe("the front matter needs a version");
    // Nowhere a template runs offers the broken one.
    expect(runnableWorkflows(workflows).map((w) => w.id)).toEqual(["summarise"]);
  });

  it("drops a workflow with no id or no content hash rather than offering it", async () => {
    // A row an insight document could never be traced back to is not a
    // template a person can meaningfully choose, so it is not shown as one.
    stubWorkflowsResponse([
      { id: "", version: "v0", sha256: "abc" },
      { id: "todos", version: "v0", sha256: "" },
      { id: "summarise", version: "v0", sha256: "abc123" },
    ]);

    const workflows = await new OperatorClient("https://operator.test").listInsightWorkflows();

    expect(workflows.map((w) => w.id)).toEqual(["summarise"]);
  });

  it("raises a listing that is not a list, rather than calling it an empty one", async () => {
    // The endpoint never serves a non-array — it replaces a nil registry with
    // an empty one so that success cannot look like absence — so a body of
    // this shape is one nobody understood. Reading it as [] would put "This
    // build ships no templates" on the screen as a fact about the image.
    stubWorkflowsResponse({ workflows: [] });

    await expect(
      new OperatorClient("https://operator.test").listInsightWorkflows(),
    ).rejects.toThrow("the workflow registry came back in a shape this app does not understand");
  });

  it("raises a failed fetch, so the panel can say the list could not be read", async () => {
    stubWorkflowsResponse({ error: "the workflow registry could not be read" }, 502);

    await expect(
      new OperatorClient("https://operator.test").listInsightWorkflows(),
    ).rejects.toThrow("the workflow registry could not be read");
  });
});

describe("InsightTemplatesPanel", () => {
  it("shortens a hash for the row and keeps the whole of it available", () => {
    const sha = "eba6bd6674d35522dddbd774d3fc6a1fbcdab025aa30a5993d49216d0c13f59f";
    expect(shortHash(sha)).toBe("eba6bd6674d3…");
    expect(shortHash("short")).toBe("short");
    // The full value stays on the element as its title.
    expect(insightTemplatesPanelSource).toContain("title={workflow.sha256}");
  });

  it("renders the registry, opening each row onto the instruction it sends", () => {
    expect(insightTemplatesPanelSource).toContain("{#each workflows as workflow");
    // The verbatim prompt, not the description beside it.
    expect(insightTemplatesPanelSource).toContain("{workflow.instruction}");
    expect(insightTemplatesPanelSource).toContain("{workflow.version}");
    expect(insightTemplatesPanelSource).toContain("{workflow.origin}");
  });

  it("shows why a custom template cannot run instead of hiding it", () => {
    expect(insightTemplatesPanelSource).toContain("cannot run: {workflow.error}");
    expect(insightTemplatesPanelSource).toContain("`${workflow.origin}:${workflow.id}`");
  });

  it("keeps the panel's claim about where these templates are used true", () => {
    // The summary step runs a registry workflow by id, so this sentence
    // describes the code rather than an intention (D-718).
    expect(insightTemplatesPanelSource).toContain("Used for insights and meeting summaries.");
  });

  it("keeps loading, an empty registry and a failed fetch as three answers", () => {
    // Collapsing any two of these tells an administrator something false:
    // "Cassini ships no templates" when the fetch failed, or the reverse.
    expect(insightTemplatesPanelSource).toContain("Loading insight templates…");
    expect(insightTemplatesPanelSource).toContain("This build ships no templates.");
    expect(insightTemplatesPanelSource).toContain("{#if loadError}");
    expect(insightTemplatesPanelSource).toContain("Templates could not be listed.");
  });

  it("edits custom templates and starts new ones only from a built-in", () => {
    // Built-ins are compiled into the image: the only thing to do with one
    // here is copy it. A custom one is a file the operator writes.
    expect(insightTemplatesPanelSource).toContain("Start from this");
    expect(insightTemplatesPanelSource).toContain("client.saveInsightTemplate(editor.id, editor.draft)");
    expect(insightTemplatesPanelSource).toContain("client.deleteInsightTemplate(workflow.id)");
    expect(insightTemplatesPanelSource).toContain("{#if isCustom(workflow)}");
    // One textarea, for the instruction; the version is never typed.
    expect(insightTemplatesPanelSource.match(/<textarea/g)).toHaveLength(1);
    expect(insightTemplatesPanelSource).not.toContain("draft.version");
  });

  it("shows the operator's refusal of a save, which carries the recorder's reason", () => {
    expect(insightTemplatesPanelSource).toContain("saveError = error instanceof Error ? error.message : String(error);");
    expect(insightTemplatesPanelSource).toContain('<div class="err-box" role="alert">{saveError}</div>');
  });
});

describe("insight template writes", () => {
  it("PUTs a custom template by id and DELETEs it", async () => {
    const calls: Array<[string, RequestInit | undefined]> = [];
    vi.stubGlobal(
      "fetch",
      vi.fn(async (url: string, init?: RequestInit) => {
        calls.push([url, init]);
        const body =
          init?.method === "DELETE"
            ? { id: "resumen", deleted: true }
            : { id: "resumen", version: "v1", sha256: "abc", name: "Resumen", origin: "Custom", instruction: "I" };
        return new Response(JSON.stringify(body), { status: 200, headers: { "Content-Type": "application/json" } });
      }),
    );
    const client = new OperatorClient("https://operator.test");

    const saved = await client.saveInsightTemplate("resumen", {
      name: "Resumen",
      question: "Q",
      description: "",
      instruction: "I",
    });
    await client.deleteInsightTemplate("resumen");

    expect(saved.version).toBe("v1");
    expect(calls[0][0]).toBe("https://operator.test/settings/workflows/resumen");
    expect(calls[0][1]?.method).toBe("PUT");
    expect(JSON.parse(String(calls[0][1]?.body))).toEqual({ name: "Resumen", question: "Q", description: "", instruction: "I" });
    expect(calls[1][0]).toBe("https://operator.test/settings/workflows/resumen");
    expect(calls[1][1]?.method).toBe("DELETE");
  });

  it("raises a refused save with the operator's reason", async () => {
    stubWorkflowsResponse({ error: "the template was not saved: the front matter needs a name" }, 400);
    await expect(
      new OperatorClient("https://operator.test").saveInsightTemplate("x", {
        name: "",
        question: "",
        description: "",
        instruction: "I",
      }),
    ).rejects.toThrow("the front matter needs a name");
  });
});
