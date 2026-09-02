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
    expect(storagePanelSource).toContain("on:click={() => requestSwitch(option)}");
    expect(storagePanelSource).toContain("on:click={confirmSwitch}");
    const putCalls = storagePanelSource.match(/operatorClient\.putStorage\(/g) ?? [];
    expect(putCalls).toHaveLength(1);
    expect(storagePanelSource).toContain("aria-label=\"Confirm storage mode change\"");
  });

  it("cannot switch to a mode the operator did not call available", () => {
    expect(storagePanelSource).toContain("if (option.active || !option.available || switching)");
    expect(storagePanelSource).toContain("disabled={option.active || !option.available || switching}");
  });

  it("leaves the mode alone when a switch fails, and says the mode is unchanged", () => {
    // The catch must NOT write `status`: the operator moves nothing it cannot
    // finish, so the buttons have to snap back to the mode really in force.
    expect(storagePanelSource).toContain("switchError = asMessage(error);");
    expect(storagePanelSource).toContain("The storage mode was not changed.");
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
