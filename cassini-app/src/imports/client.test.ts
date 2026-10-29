import { describe, expect, it } from "vitest";
import { HEARTBEAT_MAX_MISSES, heartbeatVerdict } from "./client";

// The heartbeat's tolerance (D-828): a single failed beat must not throw away
// a multi-gigabyte PUT, because the server's lease outlives several of them;
// a server verdict that the row is no longer an upload must.
describe("heartbeatVerdict", () => {
  it("tolerates a run of transient misses shorter than the limit", () => {
    const transient = Object.assign(new Error("The import request failed (HTTP 502)."), { status: 502 });
    for (let misses = 1; misses < HEARTBEAT_MAX_MISSES; misses++) {
      expect(heartbeatVerdict(transient, misses)).toBe("continue");
      expect(heartbeatVerdict(new Error("network"), misses)).toBe("continue");
    }
  });
  it("aborts once the misses reach the limit", () => {
    expect(heartbeatVerdict(new Error("timeout"), HEARTBEAT_MAX_MISSES)).toBe("abort");
    expect(heartbeatVerdict(Object.assign(new Error("x"), { status: 503 }), HEARTBEAT_MAX_MISSES + 1)).toBe("abort");
  });
  it("aborts at once when the server says the row is no longer an upload", () => {
    expect(heartbeatVerdict(Object.assign(new Error("upload is no longer active"), { status: 409 }), 1)).toBe("abort");
    expect(heartbeatVerdict(Object.assign(new Error("not found"), { status: 404 }), 1)).toBe("abort");
  });
  it("treats a status it cannot read as transient", () => {
    expect(heartbeatVerdict({ status: "weird" }, 1)).toBe("continue");
    expect(heartbeatVerdict(undefined, 1)).toBe("continue");
  });
});
