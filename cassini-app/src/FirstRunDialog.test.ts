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
    expect(dialogSource).toContain("Recordings will be visible to");
    expect(dialogSource).toContain("anyone with an account on this Nextcloud");
    expect(dialogSource).toContain(
      "can limit them to the people in each call at any time, in Operator › Settings.",
    );
    // The audience sentence comes first, and the account sentence second. The
    // shipped wizard led with the mechanism, which is the whole thing this
    // change reverses.
    expect(dialogSource.indexOf("Recordings will be visible to")).toBeLessThan(
      dialogSource.indexOf("To start, Cassini creates a Nextcloud account"),
    );
  });

  it("names the account it is about to create, and what Nextcloud will ask", () => {
    expect(dialogSource).toContain(
      "To start, Cassini creates a Nextcloud account called <code>cassini</code> to keep recordings",
    );
    expect(dialogSource).toContain("Nextcloud may ask for your password.");
    // Only where it is true: an install whose account already exists is not
    // about to make another one.
    expect(dialogSource).toContain("{#if plan.creates}");
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

  it("acknowledges at the operator, so the dialog is once per install", () => {
    // Both ways out of it: creating the account, and leaving to change the
    // audience first. A flag in this browser would show it again to the next
    // administrator, and again after a cleared cache.
    expect(dialogSource).toContain("await operatorClient.acknowledgeFirstRun();");
    expect(dialogSource).toContain("void operatorClient.acknowledgeFirstRun()");
    // …and the shell re-reads the instance, so the notice and the chip agree
    // with what just happened.
    expect(dialogSource).toContain("notifySetupChanged();");
  });

  it("is an inline dialog over a scrim, never a <dialog>", () => {
    // The app runs inside a shadow root on Nextcloud's embedded page, where the
    // top layer is the one element whose styling and focus behaviour do not
    // reliably follow it. StoragePanel.svelte's confirmation is inline for the
    // same reason.
    expect(dialogSource).toContain('role="dialog"');
    expect(dialogSource).not.toContain("</dialog>");
    expect(dialogSource).toContain(".first-run-scrim {");
  });

  it("says a standalone build cannot create the account, instead of offering to", () => {
    expect(dialogSource).toContain("{#if plan.unavailable}");
    expect(dialogSource).toContain("Open Cassini from Nextcloud's own menu.");
  });
});
