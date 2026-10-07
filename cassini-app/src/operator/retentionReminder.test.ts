import { describe, expect, it, vi } from "vitest";
import { get } from "svelte/store";
import { OperatorHttpError } from "./client";
import type { RetentionSettings } from "./retention";
import { dismissRetentionReminder, retentionMutationBusy, withRetentionMutation } from "./retentionReminder";

const policy = (): RetentionSettings => ({
  version: 3, revision: 0, schedule: { time: "15:45", timezone: "Europe/Rome" },
  recordings: { forever: false, count: 45, unit: "days" },
  current: { forever: true }, logs: { forever: true },
  history: { mode: "group", fine_initialized: true, policy: { forever: true },
    fine: { failed_capture: { forever: false, count: 7, unit: "days" }, failed_build: { forever: true },
      superseded: { forever: true }, failed_publish: { forever: true } } },
});

describe("installation-wide retention dismissal", () => {
  it("saves the current snapshot unchanged, including schedule and inactive policies", async () => {
    const current = policy();
    const before = structuredClone(current);
    const client = { getRetention: vi.fn().mockResolvedValue(current),
      putRetention: vi.fn().mockResolvedValue({ ...current, revision: 1 }) };
    expect(await dismissRetentionReminder(client)).toEqual({ ...before, revision: 1 });
    expect(client.putRetention).toHaveBeenCalledExactlyOnceWith(before);
    expect(current).toEqual(before);
    expect(get(retentionMutationBusy)).toBe(false);
  });

  it("does not write when another administrator has already acknowledged", async () => {
    const latest = { ...policy(), revision: 9 };
    const client = { getRetention: vi.fn().mockResolvedValue(latest), putRetention: vi.fn() };
    expect(await dismissRetentionReminder(client)).toBe(latest);
    expect(client.putRetention).not.toHaveBeenCalled();
  });

  it("reconciles a concurrent policy change without replaying the stale PUT", async () => {
    const latest = { ...policy(), revision: 1, recordings: { forever: true } };
    const client = { getRetention: vi.fn().mockResolvedValueOnce(policy()).mockResolvedValueOnce(latest),
      putRetention: vi.fn().mockRejectedValue(new OperatorHttpError(412, "Settings changed")) };
    expect(await dismissRetentionReminder(client)).toEqual(latest);
    expect(client.putRetention).toHaveBeenCalledTimes(1);
  });

  it.each(["initial read", "save", "conflict read", "unresolved conflict"])("leaves %s failure retryable", async (stage) => {
    const client = { getRetention: vi.fn().mockResolvedValue(policy()), putRetention: vi.fn() };
    if (stage === "initial read") client.getRetention.mockRejectedValue(new Error("Read failed"));
    if (stage === "save") client.putRetention.mockRejectedValue(new Error("Write failed"));
    if (stage.includes("conflict")) {
      client.putRetention.mockRejectedValue(new OperatorHttpError(412, "Settings changed"));
      if (stage === "conflict read") client.getRetention.mockResolvedValueOnce(policy()).mockRejectedValueOnce(new Error("Read failed"));
    }
    await expect(dismissRetentionReminder(client)).rejects.toThrow();
    expect(get(retentionMutationBusy)).toBe(false);
    expect(client.putRetention.mock.calls.length).toBeLessThanOrEqual(1);
  });

  it("discovers a committed write after a lost response without writing again", async () => {
    let current = policy();
    const client = { getRetention: vi.fn(async () => current), putRetention: vi.fn(async () => {
      current = { ...current, revision: 1 };
      throw new Error("Connection lost");
    }) };
    await expect(dismissRetentionReminder(client)).rejects.toThrow("Connection lost");
    expect((await dismissRetentionReminder(client)).revision).toBe(1);
    expect(client.putRetention).toHaveBeenCalledTimes(1);
  });

  it("excludes a second local mutation until the first finishes", async () => {
    let release!: () => void;
    const first = withRetentionMutation(() => new Promise<void>(resolve => { release = resolve; }));
    const write = vi.fn();
    expect(get(retentionMutationBusy)).toBe(true);
    await expect(withRetentionMutation(write)).rejects.toThrow("being saved");
    expect(write).not.toHaveBeenCalled();
    release();
    await first;
    expect(get(retentionMutationBusy)).toBe(false);
  });
});
