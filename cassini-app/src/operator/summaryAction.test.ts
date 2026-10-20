import { describe, expect, it } from "vitest";
import {
  isLocalEndpoint,
  summaryActionLabel,
  summaryConfirmTitle,
  summaryCostText,
  summaryEndpointText,
  summaryReplaceText,
  summaryStepCostText,
  summaryUnavailableText,
} from "./summaryAction";
import type { SummaryPlan } from "./types";

const plan: SummaryPlan = {
  available: true, has_summary: false, calls: 1, words: 5213, estimated_input_tokens: 7480,
  provider: "Open Router", model: "deepseek/deepseek-v4.1-flash", external: true,
};

describe("summary action", () => {
  it("writes a missing summary and regenerates an existing one", () => {
    expect(summaryActionLabel(plan)).toBe("Write summary");
    expect(summaryConfirmTitle(plan)).toBe("Write a summary for this meeting?");
    expect(summaryReplaceText(plan)).toBeNull();
    const existing = { ...plan, has_summary: true };
    expect(summaryActionLabel(existing)).toBe("Regenerate summary");
    expect(summaryConfirmTitle(existing)).toBe("Replace this meeting's summary?");
    expect(summaryReplaceText(existing)).toContain("instead of the current one");
  });

  it("says the calls, the model and the size before anything is sent", () => {
    expect(summaryCostText(plan)).toBe("1 call to deepseek/deepseek-v4.1-flash (Open Router) · about 7.5k input tokens, 5,213 words");
    expect(summaryCostText({ ...plan, model: "", provider: "Local", estimated_input_tokens: 640, words: 300 })).toBe(
      "1 call to Local · about 640 input tokens, 300 words",
    );
  });

  it("says whether the transcript leaves the network", () => {
    expect(summaryEndpointText(plan)).toContain("external service, which may bill");
    expect(summaryEndpointText({ ...plan, external: false })).toBe("The model runs on your own network.");
  });

  it("explains only the reasons an administrator can act on", () => {
    expect(summaryUnavailableText({ ...plan, available: false, reason: "summaries_off" })).toContain("Turn them on");
    expect(summaryUnavailableText({ ...plan, available: false, reason: "no_source_audio" })).toContain("source recording is gone");
    expect(summaryUnavailableText({ ...plan, available: false, reason: "no_transcript" })).toBeNull();
    expect(summaryUnavailableText({ ...plan, available: false, reason: "busy" })).toBeNull();
  });
});

describe("isLocalEndpoint", () => {
  it("follows the operator's rule", () => {
    for (const [url, local] of [
      ["http://127.0.0.1:11434/v1", true],
      ["http://localhost:8080/v1", true],
      ["http://192.168.1.222:8000/v1", true],
      ["http://10.0.0.5/v1", true],
      ["http://172.20.0.3/v1", true],
      ["http://ollama:11434/v1", true],
      ["http://llm.lan.local/v1", true],
      ["http://[::1]:8080/v1", true],
      ["https://openrouter.ai/api/v1", false],
      ["https://api.example.com/v1", false],
      ["http://172.40.0.3/v1", false],
      ["not a url", false],
    ] as const) {
      expect(isLocalEndpoint(url), url).toBe(local);
    }
  });

  it("says what the summary step costs per recording", () => {
    expect(summaryStepCostText("Open Router", "https://openrouter.ai/api/v1")).toBe("Open Router is an external service, which may bill for each call.");
    expect(summaryStepCostText("Ollama", "http://ollama:11434/v1")).toBe("Ollama runs on your own network.");
  });
});
