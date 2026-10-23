// Writing or rewriting a run's summary from the Operator (D-814): what the
// button says, and what the confirmation says it will cost. Writing a summary
// calls a language model, which may be billed, so nothing is sent before the
// administrator has seen how many calls, to which model, of what size, and
// whether it leaves this network. Pure so the wording can be tested.
import type { SummaryPlan } from "./types";

export function summaryActionLabel(plan: SummaryPlan): string {
  return plan.has_summary ? "Regenerate summary" : "Write summary";
}

export function summaryConfirmTitle(plan: SummaryPlan): string {
  return plan.has_summary ? "Replace this meeting's summary?" : "Write a summary for this meeting?";
}

// "1 call to deepseek/deepseek-v4.1-flash (Open Router) · about 7.5k input tokens, 5,213 words"
export function summaryCostText(plan: SummaryPlan): string {
  const calls = `${plan.calls} ${plan.calls === 1 ? "call" : "calls"}`;
  const target = plan.model
    ? `${plan.model}${plan.provider ? ` (${plan.provider})` : ""}`
    : plan.provider || "the summary model";
  return `${calls} to ${target} · about ${approxTokens(plan.estimated_input_tokens)} input tokens, ${plan.words.toLocaleString("en-US")} words`;
}

export function summaryEndpointText(plan: SummaryPlan): string {
  return plan.external
    ? "The transcript is sent to an external service, which may bill for it."
    : "The model runs on your own network.";
}

export function summaryReplaceText(plan: SummaryPlan): string | null {
  return plan.has_summary ? "Everyone who can open the meeting sees the new summary instead of the current one." : null;
}

// Why the action is not offered, when an administrator can do something
// about it; null when there is nothing to say (no transcript, not published,
// busy: the run detail already says so).
export function summaryUnavailableText(plan: SummaryPlan): string | null {
  switch (plan.reason) {
    case "summaries_off":
      return "Summaries are off. Turn them on in Settings › Publish pipeline to write one.";
    case "no_local_copy":
      return "This server no longer keeps a copy of the meeting, so a summary can't be written for this run.";
    default:
      return null;
  }
}

function approxTokens(tokens: number): string {
  if (tokens < 1000) return String(tokens);
  return `${(Math.round(tokens / 100) / 10).toLocaleString("en-US")}k`;
}

// Whether an LLM endpoint is on this host or network, by the operator's rule
// (localEndpoint in summarize_attempt.go): a loopback or private address, a
// .local/.localhost/.internal name, or a name with no domain (a container on
// the same Docker network). Anything else may be a billed service.
export function isLocalEndpoint(baseUrl: string): boolean {
  let host: string;
  try {
    host = new URL(baseUrl.trim()).hostname.toLowerCase().replace(/^\[|\]$/g, "");
  } catch {
    return false;
  }
  if (host === "") return false;
  if (host === "localhost" || /\.(localhost|local|internal)$/.test(host)) return true;
  const v4 = host.match(/^(\d+)\.(\d+)\.(\d+)\.(\d+)$/);
  if (v4) {
    const [a, b] = [Number(v4[1]), Number(v4[2])];
    return a === 127 || a === 10 || (a === 172 && b >= 16 && b <= 31) || (a === 192 && b === 168) || (a === 169 && b === 254);
  }
  if (host.includes(":")) return host === "::1" || /^f[cd]/.test(host) || host.startsWith("fe80:");
  return !host.includes(".");
}

// What Settings says about the summary step's cost, for the chosen provider.
export function summaryStepCostText(providerName: string, baseUrl: string): string {
  return isLocalEndpoint(baseUrl)
    ? `${providerName} runs on your own network.`
    : `${providerName} is an external service, which may bill for each call.`;
}
