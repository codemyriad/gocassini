import { afterEach, expect, it, vi } from "vitest";
import { readRecordingClock, retainClockSample } from "./clock";

afterEach(() => vi.restoreAllMocks());
it("retains the four timestamps and monotonic elapsed time, including stopped recordings", async () => {
  vi.spyOn(Date, "now").mockReturnValueOnce(5000).mockReturnValueOnce(5100);
  vi.spyOn(performance, "now").mockReturnValueOnce(10).mockReturnValueOnce(110);
  vi.spyOn(globalThis, "fetch").mockResolvedValue(Response.json({ serverReceiveWallMs: 1020, serverSendWallMs: 1030 }, { status: 409 }));
  const result = await readRecordingClock("/recording");
  expect(result.recordingId).toBeUndefined();
  expect(result.sample).toEqual({ clientSendWallMs: 5000, clientReceiveWallMs: 5100, serverReceiveWallMs: 1020, serverSendWallMs: 1030, elapsedMs: 100 });
});
it("supports older servers without inventing a clock measurement", async () => {
  vi.spyOn(globalThis, "fetch").mockResolvedValue(Response.json({ recordingId: "old" }));
  expect(await readRecordingClock("/recording")).toEqual({ recordingId: "old", sample: undefined });
});
it("bounds checkpoints while retaining the first observation and latest tail", () => {
  const sample = (n: number) => ({ clientSendWallMs: n, clientReceiveWallMs: n, serverReceiveWallMs: n, serverSendWallMs: n, elapsedMs: 0 });
  const samples = [sample(1)];
  for (let n = 2; n <= 300; n++) retainClockSample(samples, sample(n));
  expect(samples).toHaveLength(128);
  expect(samples[0].clientSendWallMs).toBe(1);
  expect(samples.at(-1)?.clientSendWallMs).toBe(300);
});
