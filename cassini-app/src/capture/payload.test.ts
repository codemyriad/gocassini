import { describe, expect, it } from "vitest";
import {
  captureAllowedByServer,
  consentGranted,
  enabledURLFrom,
  pickAudioSender,
  rotateSegment,
  stopSegment,
  stopWithoutRestart,
  uploadURLFrom,
} from "./payload";
import { anchorsWithin } from "./worker";

describe("uploadURLFrom", () => {
  it("targets the operator endpoint behind the AppAPI proxy", () => {
    expect(uploadURLFrom("")).toBe("/index.php/apps/app_api/proxy/gocassini/operator/capture/upload");
    expect(uploadURLFrom("/nextcloud")).toBe(
      "/nextcloud/index.php/apps/app_api/proxy/gocassini/operator/capture/upload",
    );
    expect(uploadURLFrom("/nextcloud/")).toBe(
      "/nextcloud/index.php/apps/app_api/proxy/gocassini/operator/capture/upload",
    );
  });
});

describe("pickAudioSender", () => {
  it("finds the publishing sender among subscriber connections", () => {
    expect(pickAudioSender([{ track: null }, { track: { kind: "audio", readyState: "live" } }])).toBe(1);
  });

  it("ignores video senders and ended tracks", () => {
    expect(
      pickAudioSender([
        { track: { kind: "video", readyState: "live" } },
        { track: { kind: "audio", readyState: "ended" } },
      ]),
    ).toBe(-1);
  });

  it("returns -1 for a receive-only connection", () => {
    expect(pickAudioSender([])).toBe(-1);
  });
});

describe("consentGranted", () => {
  it("defaults to no capture", () => {
    expect(consentGranted({ getItem: () => null })).toBe(false);
    expect(consentGranted({ getItem: () => "granted" })).toBe(true);
  });
});

describe("anchorsWithin", () => {
  const anchors = [
    { frameIndex: 0, rtpTimestamp: 1000, ssrc: 7, wallMs: 100 },
    { frameIndex: 50, rtpTimestamp: 49000, ssrc: 7, wallMs: 1100 },
    { frameIndex: 100, rtpTimestamp: 97000, ssrc: 7, wallMs: 2100 },
  ];

  it("slices the call-wide anchor stream to one segment", () => {
    expect(anchorsWithin(anchors, 1000, 2000).map((a) => a.frameIndex)).toEqual([50]);
  });

  it("returns nothing for a segment recorded without encoded-transform support", () => {
    expect(anchorsWithin([], 0, 10_000)).toEqual([]);
  });
});

describe("stopSegment", () => {
  // Regression: endCall used to clear the module-level capture state
  // synchronously, so MediaRecorder's final dataavailable and its onstop — both
  // of which fire AFTER stop() — found no state and silently dropped the tail of
  // the recording along with the segment-stop the worker needs to close its file
  // handle. The ordering asserted here is the fix.
  function fakeSession(posted: unknown[]) {
    let onstop: (() => void) | null = null;
    let ondata: ((event: { data: { size: number; arrayBuffer(): Promise<ArrayBuffer> } }) => void) | null = null;
    const session = {
      segmentIndex: 3,
      muteIntervals: [[10, 20]] as Array<[number, number]>,
      pendingChunks: Promise.resolve(),
      worker: { postMessage: (message: unknown) => posted.push(message) },
      recorder: {
        stop() {
          // A real MediaRecorder emits its last chunk before onstop.
          ondata?.({
            data: { size: 4, arrayBuffer: async () => new ArrayBuffer(4) },
          });
          setTimeout(() => onstop?.(), 0);
        },
        set onstop(handler: () => void) {
          onstop = handler;
        },
      },
    } as unknown as import("./payload").CaptureState;
    return {
      session,
      emitFinalChunk: () => {
        const s = session as unknown as { pendingChunks: Promise<void> };
        s.pendingChunks = s.pendingChunks.then(async () => {
          posted.push({ type: "chunk", index: 3 });
        });
      },
      setOnData: (handler: typeof ondata) => {
        ondata = handler;
      },
    };
  }

  it("posts segment-stop only after the final chunk has been handed over", async () => {
    const posted: unknown[] = [];
    const { session, emitFinalChunk, setOnData } = fakeSession(posted);
    setOnData(() => emitFinalChunk());

    await stopSegment(session);

    const kinds = posted.map((message) => (message as { type: string }).type);
    expect(kinds).toEqual(["chunk", "segment-stop"]);
  });

  it("carries the segment's mute intervals and advances the index", async () => {
    const posted: unknown[] = [];
    const { session } = fakeSession(posted);

    await stopSegment(session);

    const stop = posted.find((m) => (m as { type: string }).type === "segment-stop") as {
      index: number;
      muteIntervals: Array<[number, number]>;
    };
    expect(stop.index).toBe(3);
    expect(stop.muteIntervals).toEqual([[10, 20]]);
    expect((session as unknown as { segmentIndex: number }).segmentIndex).toBe(4);
  });

  it("does not hang when the recorder was already inactive", async () => {
    const posted: unknown[] = [];
    const { session } = fakeSession(posted);
    (session as unknown as { recorder: { stop(): void } }).recorder.stop = () => {
      throw new Error("InvalidStateError");
    };

    await expect(stopSegment(session)).resolves.toBeUndefined();
    expect(posted.map((m) => (m as { type: string }).type)).toContain("segment-stop");
  });

  it("is a no-op with no active recorder", async () => {
    const posted: unknown[] = [];
    const { session } = fakeSession(posted);
    (session as unknown as { recorder: unknown }).recorder = null;

    await stopSegment(session);

    expect(posted).toEqual([]);
  });
});

describe("rotateSegment", () => {
  // Regression: rotation used to be chained onto `pendingChunks`, the very
  // field stopSegment awaits. That made the field refer to a promise containing
  // the stopSegment call waiting on it, so one mid-call device change hung
  // capture for the rest of the meeting and no upload ever happened.
  it("completes rather than deadlocking against the chunk chain", async () => {
    const posted: Array<{ type: string }> = [];

    // Minimal stand-ins for the browser globals startSegment reaches for.
    class FakeRecorder {
      static isTypeSupported() {
        return true;
      }
      onstop: (() => void) | null = null;
      ondataavailable: unknown = null;
      start() {}
      stop() {
        setTimeout(() => this.onstop?.(), 0);
      }
    }
    const globals = globalThis as unknown as Record<string, unknown>;
    const savedRecorder = globals.MediaRecorder;
    const savedStream = globals.MediaStream;
    globals.MediaRecorder = FakeRecorder;
    globals.MediaStream = class {
      constructor(_tracks: unknown) {}
    };

    try {
      const session = {
        segmentIndex: 0,
        muteIntervals: [] as Array<[number, number]>,
        pendingChunks: Promise.resolve(),
        rotation: Promise.resolve(),
        dirName: "capture-x-1",
        segmentStartWallMs: 0,
        worker: { postMessage: (message: { type: string }) => posted.push(message) },
        recorder: new FakeRecorder(),
      } as unknown as import("./payload").CaptureState;

      const sender = {
        track: { kind: "audio", readyState: "live", enabled: true, getSettings: () => ({}) },
      } as unknown as RTCRtpSender;

      rotateSegment(session, sender);

      const rotation = (session as unknown as { rotation: Promise<void> }).rotation;
      await expect(
        Promise.race([
          rotation.then(() => "done"),
          new Promise((_, reject) => setTimeout(() => reject(new Error("rotation deadlocked")), 3000)),
        ]),
      ).resolves.toBe("done");

      // The old segment was closed and a new one opened, in that order.
      const kinds = posted.map((message) => message.type);
      expect(kinds).toContain("segment-stop");
      expect(kinds).toContain("segment-start");
      expect(kinds.indexOf("segment-stop")).toBeLessThan(kinds.lastIndexOf("segment-start"));
    } finally {
      globals.MediaRecorder = savedRecorder;
      globals.MediaStream = savedStream;
    }
  });
});

describe("stopWithoutRestart", () => {
  // replaceTrack(null) detaches the microphone. Continuing to record the old
  // track kept writing whatever that detached source still produced.
  it("closes the segment and does not open another", async () => {
    const posted: Array<{ type: string }> = [];
    const session = {
      segmentIndex: 0,
      muteIntervals: [] as Array<[number, number]>,
      pendingChunks: Promise.resolve(),
      rotation: Promise.resolve(),
      worker: { postMessage: (message: { type: string }) => posted.push(message) },
      recorder: {
        onstop: null as null | (() => void),
        stop() {
          setTimeout(() => this.onstop?.(), 0);
        },
      },
    } as unknown as import("./payload").CaptureState;

    stopWithoutRestart(session);
    await (session as unknown as { rotation: Promise<void> }).rotation;

    const kinds = posted.map((message) => message.type);
    expect(kinds).toContain("segment-stop");
    expect(kinds).not.toContain("segment-start");
  });
});

describe("captureAllowedByServer", () => {
  // The administrator switch is what makes the per-browser consent and the
  // missing upload quota acceptable, so this check fails CLOSED: the cost of a
  // false no is a missing transcript improvement, the cost of a false yes is
  // collecting audio an administrator switched off.
  it("records only on an explicit yes", async () => {
    const yes = async () => new Response(JSON.stringify({ enabled: true }), { status: 200 });
    await expect(captureAllowedByServer("", yes as never)).resolves.toBe(true);
  });

  it("refuses on an explicit no", async () => {
    const no = async () => new Response(JSON.stringify({ enabled: false }), { status: 200 });
    await expect(captureAllowedByServer("", no as never)).resolves.toBe(false);
  });

  it("refuses when the server cannot be reached", async () => {
    const boom = async () => {
      throw new Error("network down");
    };
    await expect(captureAllowedByServer("", boom as never)).resolves.toBe(false);
  });

  it("refuses on an error status or an unreadable answer", async () => {
    const err = async () => new Response("nope", { status: 503 });
    await expect(captureAllowedByServer("", err as never)).resolves.toBe(false);

    const junk = async () => new Response("not json", { status: 200 });
    await expect(captureAllowedByServer("", junk as never)).resolves.toBe(false);
  });

  it("targets the operator endpoint behind the AppAPI proxy", () => {
    expect(enabledURLFrom("/nextcloud")).toBe(
      "/nextcloud/index.php/apps/app_api/proxy/gocassini/operator/capture/enabled",
    );
  });
});
