import { afterEach, describe, expect, it, vi } from "vitest";

import { notifySetupChanged, onSetupChanged, resetSetupListeners } from "./setupSignal";

// The signal exists so that a setup performed on the Setup tab clears the
// shell's "Cassini is not configured" notice IN THE SAME SESSION, without
// reloading the page.

afterEach(() => {
  resetSetupListeners();
  vi.restoreAllMocks();
});

describe("setup signal", () => {
  it("tells every listener", () => {
    const shell = vi.fn();
    const other = vi.fn();
    onSetupChanged(shell);
    onSetupChanged(other);

    notifySetupChanged();

    expect(shell).toHaveBeenCalledTimes(1);
    expect(other).toHaveBeenCalledTimes(1);
  });

  it("is quiet when nobody is listening", () => {
    expect(() => notifySetupChanged()).not.toThrow();
  });

  // A component that mounts, unmounts and mounts again must not leave the first
  // instance's listener behind holding state nothing renders.
  it("stops telling a listener that unsubscribed", () => {
    const shell = vi.fn();
    const stop = onSetupChanged(shell);

    stop();
    notifySetupChanged();

    expect(shell).not.toHaveBeenCalled();
  });

  // One broken surface is a smaller failure than every surface staying stale,
  // which is the papercut this exists to remove.
  it("keeps going when one listener throws, and says so", () => {
    const consoleError = vi.spyOn(console, "error").mockImplementation(() => {});
    const broken = vi.fn(() => {
      throw new Error("boom");
    });
    const shell = vi.fn();
    onSetupChanged(broken);
    onSetupChanged(shell);

    expect(() => notifySetupChanged()).not.toThrow();
    expect(shell).toHaveBeenCalledTimes(1);
    expect(consoleError).toHaveBeenCalled();
  });

  // Iterating a copy: a listener that unsubscribes itself while being told must
  // not skip the listener after it.
  it("survives a listener that unsubscribes itself mid-notify", () => {
    const shell = vi.fn();
    const stop = onSetupChanged(() => stop());
    onSetupChanged(shell);

    notifySetupChanged();

    expect(shell).toHaveBeenCalledTimes(1);
  });
});
