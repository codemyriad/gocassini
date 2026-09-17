import { afterEach, describe, expect, it, vi } from "vitest";
import { get } from "svelte/store";

import { cancelLeave, confirmLeave, guardLeave, leavePrompt, unsavedChanges } from "./unsaved";

afterEach(() => {
  unsavedChanges.set(false);
  leavePrompt.set(null);
});

describe("unsaved changes guard", () => {
  it("leaves straight away when nothing is unsaved", () => {
    const leave = vi.fn();
    guardLeave(leave);
    expect(leave).toHaveBeenCalledOnce();
    expect(get(leavePrompt)).toBeNull();
  });

  it("asks before leaving unsaved edits, and stays when told to", () => {
    unsavedChanges.set(true);
    const leave = vi.fn();
    guardLeave(leave);
    expect(leave).not.toHaveBeenCalled();
    expect(get(leavePrompt)).toBe(leave);
    cancelLeave();
    expect(leave).not.toHaveBeenCalled();
    expect(get(leavePrompt)).toBeNull();
    expect(get(unsavedChanges)).toBe(true);
  });

  it("discards the edits and leaves when confirmed", () => {
    unsavedChanges.set(true);
    const leave = vi.fn();
    guardLeave(leave);
    confirmLeave();
    expect(leave).toHaveBeenCalledOnce();
    expect(get(unsavedChanges)).toBe(false);
  });
});
