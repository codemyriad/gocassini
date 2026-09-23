import { describe, expect, it } from "vitest";

import dialogSource from "./FirstRunDialog.svelte?raw";

// Source-level assertions, the convention this repo follows for .svelte files:
// the suite runs in node with no DOM harness. What the dialog DECIDES is in
// operator/firstRun.ts and tested there; what is asserted here is the copy,
// which is settled (D-751), and the two structural facts a refactor could
// quietly lose.

describe("the first-run dialog", () => {
  it("states the single sharing rule before account setup", () => {
    expect(dialogSource).toContain(
      `{firstRunReady(plan) ? "Cassini is ready to record" : "Cassini can't record yet"}`,
    );
    expect(dialogSource).toContain(
      "Recordings use Nextcloud's built-in file sharing. Cassini keeps them in its own account and shares each meeting with its participants.",
    );
    expect(dialogSource).toContain("Room participants");
    expect(dialogSource).not.toContain('role="radiogroup"');
  });

  it("names the participants and public resharing rule", () => {
    // The room name travels with every published recording, so an audience
    // sentence about the recordings alone describes half of what becomes
    // visible (11 September product decision). The settings section says the
    // same thing about the same mode, in the same words.
    expect(dialogSource).toContain("Public meeting participants can share it onward");
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

  it("offers the single account setup action", () => {
    expect(dialogSource).toContain("Create the account and start");
    expect(dialogSource).toContain("Start recording");
    expect(dialogSource).not.toContain("Continue in Publish pipeline");
    expect(dialogSource).not.toContain("pendingAccessChoice.set(chosen);");
    expect(dialogSource).toContain('class="btn btn-primary"');
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
    expect(dialogSource.match(/acknowledgeFirstRun\(\)/g)).toHaveLength(1);
    const leave = dialogSource.slice(
      dialogSource.indexOf("function openSettings()"),
      dialogSource.indexOf("// start does the whole of the first run"),
    );
    expect(leave).toContain('dispatch("settings");');
    expect(leave).not.toContain("acknowledgeFirstRun");
  });

  it("claims nothing about being ready when the account cannot be made here", () => {
    expect(dialogSource).toContain("{#if plan.blocked}");
    expect(dialogSource).toContain(
      "Cassini needs a Nextcloud account to keep recordings in, and this page has no way to",
    );
    expect(dialogSource).toContain("Open Operator › Publish pipeline");
    const buttons = dialogSource.slice(dialogSource.indexOf("mt-1 flex flex-wrap"));
    const blocked = buttons.slice(buttons.indexOf("{#if plan.blocked || plan.unavailable}"), buttons.indexOf("{:else}"));
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

  it("says a standalone build cannot create the account, and points at the commands instead", () => {
    expect(dialogSource).toContain("{:else if plan.unavailable}");
    expect(dialogSource).toContain(
      "Cassini needs a Nextcloud account to keep recordings in. This page can't create it",
    );
    expect(dialogSource).toContain("Open Cassini from Nextcloud's own menu, or run the commands under");
    // Never Start: every write it would make is refused before it is sent, so
    // the button would acknowledge a first run that never happened.
    const buttons = dialogSource.slice(dialogSource.indexOf("mt-1 flex flex-wrap"));
    const noStart = buttons.slice(0, buttons.indexOf("{:else}"));
    expect(noStart).toContain("{#if plan.blocked || plan.unavailable}");
    expect(noStart).toContain("on:click={openSettings}");
    expect(noStart).not.toContain("on:click={start}");
  });
});
