import { describe, expect, it } from "vitest";

import storagePanelSource from "./StoragePanel.svelte?raw";
import setupSource from "./Setup.svelte?raw";
import appSource from "./App.svelte?raw";

// Svelte components are not mounted in this repo, so the panel's load-bearing
// decisions are asserted against its source. What matters here is not the
// markup but three properties a refactor could quietly lose — each of which
// would make the switch either unusable or dangerous.

describe("StoragePanel", () => {
  it("confirms before switching, and only from the confirmation", () => {
    // requestSwitch stages the mode; only confirmSwitch calls the operator. A
    // button wired straight to putStorage would move every recording in the
    // instance on one click.
    expect(storagePanelSource).toContain("function requestSwitch");
    expect(storagePanelSource).toContain("function confirmSwitch");
    expect(storagePanelSource).toContain("requestSwitch(option)");
    expect(storagePanelSource).toContain("on:click={confirmSwitch}");
    const putCalls = storagePanelSource.match(/operatorClient\.putStorage\(/g) ?? [];
    expect(putCalls).toHaveLength(1);
    expect(storagePanelSource).toContain('"Confirm storage mode change"');
  });

  // The switch and the setup are different decisions with different
  // consequences, and confirmSwitch must not be able to do the wrong one.
  it("distinguishes switching a mode from building one", () => {
    expect(storagePanelSource).toContain('pendingKind === "setup"');
    expect(storagePanelSource).toContain("await runSetup(target)");
    expect(storagePanelSource).toContain("await operatorClient.putStorage(");
    expect(storagePanelSource).toContain('"Confirm Nextcloud setup changes"');
  });

  it("never switches modes as a side effect of setting one up", () => {
    // Building a mode and moving into it are separate decisions. runSetup
    // re-checks; it must not call putStorage.
    const runSetup = storagePanelSource.slice(
      storagePanelSource.indexOf("async function runSetup("),
      storagePanelSource.indexOf("async function confirmSwitch("),
    );
    expect(runSetup).toContain("recheckStorage()");
    expect(runSetup).not.toContain("putStorage(");
  });

  it("cannot switch to a mode the operator did not call available", () => {
    // An unavailable mode now offers SETUP rather than a switch, so the guard
    // moved — but a switch still requires `available`.
    expect(storagePanelSource).toContain('pendingKind = option.available ? "switch" : "setup"');
    // …and a mode with no plan is still a dead end, not a button that does nothing.
    expect(storagePanelSource).toContain("(!option.available && option.setup.length === 0)");
  });

  it("re-reads the mode after a failed switch instead of claiming nothing changed", () => {
    // A transition that fails AFTER moving the archive HAS changed the mode the
    // operator is using, and its own message says so. Asserting "the mode was
    // not changed" there would contradict the sentence printed beneath it.
    expect(storagePanelSource).toContain("switchError = asMessage(error);");
    expect(storagePanelSource).toContain("Switching the storage mode failed.");
    expect(storagePanelSource).not.toContain("The storage mode was not changed.");
    // The catch block re-reads before it finishes.
    const catchBlock = storagePanelSource.slice(
      storagePanelSource.indexOf("switchError = asMessage(error);"),
      storagePanelSource.indexOf("} finally {", storagePanelSource.indexOf("switchError = asMessage(error);")),
    );
    expect(catchBlock).toContain("operatorClient.getStorage()");
  });

  it("renders the operator's own words rather than copy of its own", () => {
    for (const field of ["option.summary", "option.blocker", "pending.consequence", "status.detail"]) {
      expect(storagePanelSource).toContain(`{${field}}`);
    }
    expect(storagePanelSource).toContain("option.instructions.join(");
  });

  // "" is a third answer, not a missing one: an unresolved mode means nothing
  // has checked the instance, and calling that "default" would claim a decision
  // nobody made.
  it("distinguishes an unresolved mode from the default one", () => {
    expect(storagePanelSource).toContain('status.mode === ""');
    expect(storagePanelSource).toContain("Not checked yet");
  });
});

describe("the Setup surface", () => {
  it("is the storage panel's only host, and builds its own client", () => {
    expect(setupSource).toContain("import StoragePanel from \"./StoragePanel.svelte\"");
    expect(setupSource).toContain("new OperatorClient(loadConfig().operatorBasePath)");
  });

  it("is a shell tab gated on the same admin probe as the operator surface", () => {
    expect(appSource).toContain('import Setup from "./Setup.svelte"');
    expect(appSource).toContain('aria-current={surface === "setup" ? "page" : undefined}');
    expect(appSource).toContain('on:click={() => selectSurface("setup")}');
    // The tab bar lives inside `{#if operatorAvailable}`, so there is exactly
    // one gate for every admin surface.
    expect(appSource.indexOf("{#if operatorAvailable}")).toBeLessThan(
      appSource.indexOf('selectSurface("setup")'),
    );
  });

  it("hides browse for any admin surface, not only the operator one", () => {
    // `surface === "operator"` here would leave the meeting list rendered
    // underneath the Setup surface.
    expect(appSource).toContain('class:cassini-shell-hidden={surface !== "browse"}');
    expect(appSource).not.toContain('class:cassini-shell-hidden={surface === "operator"}');
  });
});
