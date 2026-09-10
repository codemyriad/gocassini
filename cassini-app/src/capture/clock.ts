// Four timestamps, in milliseconds. Positive offset means the client is ahead.
// Keep the observations: upload time is not a substitute for capture-time skew.
export interface CaptureClockSample {
  clientSendWallMs: number;
  clientReceiveWallMs: number;
  serverReceiveWallMs: number;
  serverSendWallMs: number;
  elapsedMs: number;
}

export async function readRecordingClock(url: string): Promise<{
  recordingId?: string; sample?: CaptureClockSample;
}> {
  const clientSendWallMs = Date.now();
  const started = performance.now();
  const response = await fetch(url, {
    credentials: "same-origin", cache: "no-store", signal: AbortSignal.timeout(5_000),
  });
  const body = await response.json();
  const elapsedMs = performance.now() - started;
  const clientReceiveWallMs = Date.now();
  const sample = [body.serverReceiveWallMs, body.serverSendWallMs].every(
    value => Number.isSafeInteger(value) && value > 0,
  ) ? { clientSendWallMs, clientReceiveWallMs, serverReceiveWallMs: body.serverReceiveWallMs,
    serverSendWallMs: body.serverSendWallMs, elapsedMs } : undefined;
  return { recordingId: response.ok && typeof body.recordingId === "string" ? body.recordingId : undefined, sample };
}

export function retainClockSample(samples: CaptureClockSample[], sample: CaptureClockSample): void {
  // Keep the first observation and a bounded rolling tail, including stop.
  if (samples.length >= 128) samples.splice(1, 1);
  samples.push(sample);
}
