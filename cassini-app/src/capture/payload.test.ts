import { describe, expect, it } from "vitest";
import { consentGranted, pickAudioSender, uploadURLFrom } from "./payload";
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
