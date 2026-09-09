import { afterEach, describe, expect, it, vi } from "vitest";

import insightTemplatesPanelSource from "./InsightTemplatesPanel.svelte?raw";
import { shortHash } from "./InsightTemplatesPanel.svelte";
import { OperatorClient } from "./operator/client";

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

  it("keeps the panel's claim about where these templates are used true", () => {
    // The summary step runs a registry workflow by id, so this sentence
    // describes the code rather than an intention (D-718).
    expect(insightTemplatesPanelSource).toContain(
      "Used when creating an insight, and by the summary step in the publish pipeline.",
    );
  });

  it("keeps loading, an empty registry and a failed fetch as three answers", () => {
    // Collapsing any two of these tells an administrator something false:
    // "Cassini ships no templates" when the fetch failed, or the reverse.
    expect(insightTemplatesPanelSource).toContain("Loading insight templates…");
    expect(insightTemplatesPanelSource).toContain("This build ships no templates.");
    expect(insightTemplatesPanelSource).toContain("{loadError}");
    expect(insightTemplatesPanelSource).toContain(
      "This says the template list could not be read, not that Cassini ships none.",
    );
  });

  it("offers no way to author or edit a template", () => {
    // Read-only for this pass: prompts are authored in the repository and
    // compiled into the image, and there is no PUT behind this panel.
    expect(insightTemplatesPanelSource).not.toContain("<textarea");
    expect(insightTemplatesPanelSource).not.toContain("<input");
    expect(insightTemplatesPanelSource).not.toMatch(/put[A-Z]/);
  });
});
