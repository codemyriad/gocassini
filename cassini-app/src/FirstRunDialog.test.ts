import { describe, expect, it } from "vitest";

import dialogSource from "./FirstRunDialog.svelte?raw";

// Source-level assertions, the convention this repo follows for .svelte files:
// the suite runs in node with no DOM harness. What the dialog DECIDES is in
// operator/firstRun.ts and tested there; what is asserted here is the copy,
// which is settled (D-751), and the two structural facts a refactor could
// quietly lose.

describe("the first-run dialog", () => {
  it("says who can see recordings before it says how anything works", () => {
    expect(dialogSource).toContain("Cassini is ready to record");
    expect(dialogSource).toContain(
      "Recordings, and the names of the rooms they came from, will be visible to",
    );
    expect(dialogSource).toContain("anyone with an account on this Nextcloud");
    expect(dialogSource).toContain(
      "can limit them to the people in each call at any time, in Operator › Settings.",
    );
    // The audience sentence comes first, and the account sentence second. The
    // shipped wizard led with the mechanism, which is the whole thing this
    // change reverses.
    expect(dialogSource.indexOf("Recordings, and the names of the rooms")).toBeLessThan(
      dialogSource.indexOf("To start, Cassini creates a Nextcloud account"),
    );
  });

  it("names the rooms as well as the recordings", () => {
    // The room name travels with every published recording, so an audience
    // sentence about the recordings alone describes half of what becomes
    // visible (11 September product decision). The settings section says the
    // same thing about the same mode, in the same words.
    expect(dialogSource).toContain("the names of the rooms they came from");
  });

  it("names the account it is about to create, and what Nextcloud will ask", () => {
    expect(dialogSource).toContain(
      "To start, Cassini creates a Nextcloud account called <code>cassini</code> to keep recordings",
    );
    expect(dialogSource).toContain("Nextcloud may ask for your password.");
    // Only where it is true: an install whose account already exists is not
    // about to make another one.
    expect(dialogSource).toContain("{:else if plan.creates}");
  });

  it("offers the audience before the account, and creating as the primary action", () => {
    expect(dialogSource).toContain("Change who can see first");
    expect(dialogSource).toContain("Create the account and start");
    expect(dialogSource).toContain('class="btn btn-sm btn-primary"');
  });

  it("never shows the service account's password", () => {
    // runSetupPlan mints one and returns it; nothing here reads it. The
    // operator signs in as the account through AppAPI's act-as-user header and
    // needs no password, so a value shown once and kept by an administrator was
    // a credential made out of something nothing uses (D-756).
    expect(dialogSource).not.toContain("PasswordReveal");
    expect(dialogSource).not.toContain("password}");
    expect(dialogSource).not.toContain("outcome.password");
  });

  it("acknowledges at the operator, and only once the account exists", () => {
    // The flag is the operator's rather than this browser's, so the dialog is
    // once per install rather than once per browser: a second administrator
    // does not meet a dialog the first one answered, and a cleared cache does
    // not bring it back.
    expect(dialogSource).toContain("await operatorClient.acknowledgeFirstRun();");
    // …and the shell re-reads the instance, so the notice and the chip agree
    // with what just happened.
    expect(dialogSource).toContain("notifySetupChanged();");
    // Exactly one acknowledgement, inside the action that creates the account.
    // "Change who can see first" used to fire a second one on its way out,
    // which spent this install's one dialog on a click that created nothing and
    // left an install that still could not record with nothing left to say so.
    expect(dialogSource.match(/acknowledgeFirstRun\(\)/g)).toHaveLength(1);
    const leave = dialogSource.slice(
      dialogSource.indexOf("function openSettings()"),
      dialogSource.indexOf("// start does the whole of the first run"),
    );
    expect(leave).toContain('dispatch("settings");');
    expect(leave).not.toContain("acknowledgeFirstRun");
  });

  it("claims nothing about being ready when the account cannot be made here", () => {
    // The operator says the account is missing and offered no step this page
    // could run. There is nothing to start, so there is no Start button to
    // acknowledge a first run with — one way on, to the section that holds the
    // account row and the diagnosis.
    expect(dialogSource).toContain(
      `{plan.blocked ? "Cassini can't record yet" : "Cassini is ready to record"}`,
    );
    expect(dialogSource).toContain("{#if plan.blocked}");
    expect(dialogSource).toContain(
      "Cassini needs a Nextcloud account to keep recordings in, and this page has no way to",
    );
    expect(dialogSource).toContain("Open Operator › Settings");
    const blocked = dialogSource.slice(
      dialogSource.indexOf("{#if plan.blocked}", dialogSource.indexOf("mt-1 flex flex-wrap")),
      dialogSource.indexOf("Change who can see first"),
    );
    expect(blocked).toContain("on:click={openSettings}");
    expect(blocked).not.toContain("on:click={start}");
  });

  it("is an inline dialog over a scrim, never a <dialog>", () => {
    // The app runs inside a shadow root on Nextcloud's embedded page, where the
    // top layer is the one element whose styling and focus behaviour do not
    // reliably follow it. RecordingAccessPanel.svelte's confirmations are
    // inline for the same reason.
    expect(dialogSource).toContain('role="dialog"');
    expect(dialogSource).not.toContain("</dialog>");
    expect(dialogSource).toContain(".first-run-scrim {");
  });

  it("says a standalone build cannot create the account, instead of offering to", () => {
    expect(dialogSource).toContain("{#if plan.unavailable}");
    expect(dialogSource).toContain("Open Cassini from Nextcloud's own menu.");
  });
});
