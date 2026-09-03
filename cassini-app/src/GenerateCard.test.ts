import { describe, expect, it } from "vitest";

import generateCardSource from "./GenerateCard.svelte?raw";

// Source-level assertions, the convention this repo follows for .svelte files
// (see NeedsSetupCard.test.ts): the suite runs in node with no DOM harness.
// What is asserted here is what a reader would call a bug — the card inventing
// copy, rendering a control that 403s, promising a retry it does not perform,
// or leaving a poll running behind a closed panel.

describe("GenerateCard", () => {
  it("renders failure copy it did not write", () => {
    // Every word about why a run failed comes from buildRunFailureNotice, where
    // a test can reach it, and is rendered by the same NeedsSetupCard every
    // other unconfigured state in this app uses — so a failed run and a
    // deployment with no endpoint cannot end up describing the same fact two
    // different ways.
    expect(generateCardSource).toContain("buildRunFailureNotice({ run, isAdmin })");
    expect(generateCardSource).toContain("<NeedsSetupCard notice={buildRunFailureNotice(");
    expect(generateCardSource).not.toContain("rejected the request");
    expect(generateCardSource).not.toContain("No AI endpoint is configured");
  });

  it("has no second notion of admin", () => {
    // The client is null for exactly the people the operator boundary probe
    // denied, and `operator/settings/workflows` is ADMIN at the proxy — so the
    // template picker exists precisely where it would not 403.
    expect(generateCardSource).toContain("$: isAdmin = operatorClient !== null;");
    expect(generateCardSource).toContain("{#if isAdmin}");
    expect(generateCardSource).toContain("This runs the template your administrator configured");
  });

  it("puts the run in the list before it has done anything", () => {
    // A record exists before its content does (D-720). Without this the row
    // materialises out of nowhere a minute after the button was pressed, which
    // is the gap the prototype's 900ms "Generating…" hides.
    expect(generateCardSource).toMatch(
      /runs = \[run, \.\.\.runs\.filter\(\(existing\) => existing\.id !== run\.id\)\];/,
    );
  });

  it("stops polling when the run is terminal or the panel closes", () => {
    // Two independent stops. The timer is cleared on destroy, and `stopped` is
    // checked after every await — clearing the timer alone would still let an
    // in-flight poll reschedule itself, which is a request loop with no
    // component behind it.
    expect(generateCardSource).toContain("onDestroy(() => {");
    expect(generateCardSource).toContain("stopped = true;");
    expect(generateCardSource).toContain("clearPollTimer();");
    expect(generateCardSource).toContain("if (pendingRuns().length === 0) {");
    expect(generateCardSource).toMatch(/if \(stopped\) \{\s*return;\s*\}/);
  });

  it("backs off rather than hammering the operator", () => {
    // A run over five meetings on a local model is minutes; the interval is
    // pollDelayMs, and it resets the moment a run actually moves.
    expect(generateCardSource).toContain("pollDelayMs(pollRound)");
    expect(generateCardSource).toContain("pollRound = moved ? 0 : pollRound + 1;");
  });

  it("treats a raced retry as an answer rather than an error", () => {
    // The status is the lock: a retry against queued or running is a 409 no-op,
    // and the run is already doing what was asked.
    expect(generateCardSource).toContain("error.status === 409");
    expect(generateCardSource).toContain("void refresh(run.id);");
  });

  it("does not promise a retry replays the endpoint that failed", () => {
    // Retry re-resolves provider and model from current settings — that is what
    // makes "add a key" a fix rather than a suggestion — so the copy beside the
    // button must not describe a replay.
    expect(generateCardSource).toContain("Uses the endpoint and model configured now.");
  });

  it("distinguishes a run that failed from a question that could not be asked", () => {
    // A failed poll says nothing about the run: the run is whatever the
    // operator says it is, and painting it red because one request timed out
    // would be the app inventing a failure.
    expect(generateCardSource).toContain("pollError = failedToAsk;");
    expect(generateCardSource).toContain("let listError");
    expect(generateCardSource).toContain("let createError");
  });

  it("is contained by the panel it sits in", () => {
    // This mounts into a shadow root inside Nextcloud's own page, where a fixed
    // overlay escapes the app and covers Nextcloud's chrome (App.svelte).
    expect(generateCardSource).not.toContain("position: fixed");
    expect(generateCardSource).not.toContain("position:fixed");
  });

  it("says where the transcripts go before they go there", () => {
    // The model call uses the instance's key, so a run is attributable to the
    // deployment; the document lands in the requester's own files. Both are
    // said here rather than discovered afterwards.
    expect(generateCardSource).toContain("configured AI endpoint");
    expect(generateCardSource).toContain("written into your own Nextcloud files");
  });

  it("does not imply a typed question is saveable", () => {
    // Freeform text is user-authored prompt text at run time, not a template:
    // templates ship with Cassini and there is no PUT behind this panel.
    expect(generateCardSource).toContain("It is not saved as a template.");
  });
});
