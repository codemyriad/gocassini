import { describe, expect, it } from "vitest";
import { newRequestId, roomWarning, usageLine, type Topic } from "./topics";

const topic = (meetings: number, pending: number) => ({ usage: { meetings, pending } }) as Topic;

describe("topic helpers", () => {
  it("says what a topic covers among the caller's meetings", () => {
    expect(usageLine(topic(1, 0))).toBe("On 1 meeting");
    expect(usageLine(topic(5, 2))).toBe("On 5 meetings · 2 to review");
  });

  it("warns before a tag seen in one room is described for everyone", () => {
    expect(roomWarning(null, false)).toBe("");
    expect(roomWarning(null, true)).toContain("shows its name to everyone");
    expect(roomWarning(null, false, true)).toContain("shows its name to everyone");
    expect(roomWarning({ rooms: ["Team", "Board"], roomCount: 2 }, false, true)).toBe("");
    expect(roomWarning({ rooms: ["Leadership 1:1"], roomCount: 1 }, false, true)).toContain("only on meetings from “Leadership 1:1”");
    expect(roomWarning({ rooms: ["Team", "Board"], roomCount: 2 }, true)).toContain("from “Team”, “Board”");
  });

  it("makes request ids the operator accepts", () => {
    expect(newRequestId()).toMatch(/^req_[0-9a-f]{24}$/);
    expect(newRequestId()).not.toBe(newRequestId());
  });
});
