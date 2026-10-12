import { describe, expect, it } from "vitest";

import panelSource from "./RecordingSetup.svelte?raw";

// The panel does not check, and does not refresh itself.
//
// The operator establishes a baseline when its container boots; after that the
// findings change only when somebody asks for a check. So there is nothing for
// a background refresh to discover, and a page that triggers work on load or on
// a timer is how this panel twice ended up probing without being asked.
describe("the panel never checks on its own", () => {
  // The rule was "no timer" while the panel had nothing that moved by itself.
  // A test recording does: Talk starts it, and the job records, uploads, builds
  // and publishes without the reader touching anything, so the panel sat stale
  // until somebody reloaded it. The rule is what the old timer actually got
  // wrong — probing on a clock, and disabling controls while it did — not the
  // existence of a timer.
  it("runs no probe on a timer", () => {
    expect(panelSource).not.toContain("polling");
    // checkReadiness is the POST that probes; a timer must never reach it.
    const timerBody = panelSource.slice(panelSource.indexOf("setInterval"));
    expect(timerBody.slice(0, 200)).not.toContain("checkReadiness");
    expect(timerBody.slice(0, 200)).not.toContain("load(true");
  });

  it("only follows a recording or a re-index that is already under way", () => {
    // The timer's whole body: a guarded, quiet read. It stops on its own when
    // the test reaches a state only a person can move on from.
    expect(panelSource).toContain("if (testInFlight(report) || report?.checks.some(check => check.running)) void refreshQuietly()");
  });

  it("follows quietly, so no control flickers", () => {
    // refreshQuietly must not touch busy — that is what disabled every button
    // twelve times a minute and dropped clicks that landed on it.
    const quiet = panelSource.slice(
      panelSource.indexOf("async function refreshQuietly"),
      panelSource.indexOf("async function load"));
    expect(quiet).not.toContain("busy = true");
    expect(quiet).not.toContain("checking = ");
  });

  it("reads on mount rather than checking", () => {
    // load(false) is GET /health; load(true) posts a check.
    expect(panelSource).toContain("void load(false)");
    expect(panelSource).not.toContain("void load(true)");
  });

  it("reacts to a setup change by re-reading, not re-probing", () => {
    expect(panelSource).toContain("onSetupChanged(() => void load(false))");
  });

  it("never disables a control for a background refresh", () => {
    expect(panelSource).not.toContain("busy || polling");
    expect(panelSource).toContain("disabled={busy}");
  });
});

// Whose room the link points at.
//
// Talk's Start recording action belongs to a conversation's moderators, and the
// connection check creates this room as Cassini's own provisioning user — it
// only needs a token to read Talk's recording settings with. Linking to it sent
// an administrator into a call where the record button was not theirs to press,
// directly under steps telling them to press it. Reported from staging.
describe("the test room is offered only to an administrator who can record in it", () => {
  it("builds no link unless the operator says the room is this reader's", () => {
    const derivation = panelSource.slice(panelSource.indexOf("$: testRoomHref ="));
    expect(derivation.slice(0, 260)).toContain("report?.test_room_mine");
  });

  it("builds the link in one place, so no branch can offer an unowned room", () => {
    // Both "Open test room" affordances hang off testRoomHref. An href built
    // straight from test_room_url anywhere would reintroduce the bug.
    expect(panelSource).toContain("Open test room");
    expect(panelSource).not.toMatch(/href=\{[^}]*test_room_url/);
  });
});
