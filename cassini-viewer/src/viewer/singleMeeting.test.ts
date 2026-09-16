import { describe, expect, it } from "vitest";

import { meetingIdFromUrl, resolveEmbedTheme, singleMeetingEntry } from "./singleMeeting";

describe("meetingIdFromUrl", () => {
  it("takes the file's own name", () => {
    expect(meetingIdFromUrl("https://gocassini.com/talk/Daily-Standup--2026-03-13--12-00-00.opus")).toBe(
      "Daily-Standup--2026-03-13--12-00-00",
    );
    expect(meetingIdFromUrl("./talk.opus")).toBe("talk");
    expect(meetingIdFromUrl("/a/b/c.OPUS")).toBe("c");
  });

  it("drops addressing, because the same recording behind a cache-buster is the same recording", () => {
    expect(meetingIdFromUrl("https://x.test/talk.opus?v=3")).toBe("talk");
    expect(meetingIdFromUrl("https://x.test/talk.opus#t=10ms")).toBe("talk");
  });

  it("unescapes a name, and keeps a malformed one rather than refusing the meeting", () => {
    expect(meetingIdFromUrl("/Team%20Sync.opus")).toBe("Team Sync");
    expect(meetingIdFromUrl("/100%.opus")).toBe("100%");
  });

  it("always answers something addressable", () => {
    expect(meetingIdFromUrl("")).toBe("meeting");
    expect(meetingIdFromUrl("https://x.test/")).toBe("meeting");
    expect(meetingIdFromUrl(".opus")).toBe("meeting");
  });
});

describe("singleMeetingEntry", () => {
  it("reads a title and date out of the name, as the static export does", () => {
    const entry = singleMeetingEntry("https://x.test/Daily-Standup--2026-03-13--12-00-00.opus");
    expect(entry).toMatchObject({
      id: "Daily-Standup--2026-03-13--12-00-00",
      title: "Daily Standup",
      dateLabel: "2026-03-13 12:00",
      audioPath: "https://x.test/Daily-Standup--2026-03-13--12-00-00.opus",
    });
  });

  it("lets the embedding page name the recording", () => {
    expect(singleMeetingEntry("/talk.opus", "  Cassini at NCC 2026  ").title).toBe("Cassini at NCC 2026");
    expect(singleMeetingEntry("/talk.opus", "   ").title).toBe("Talk");
  });

  it("keeps the URL verbatim as the audio path, so addressing survives the round trip", () => {
    expect(singleMeetingEntry("https://x.test/talk.opus?v=3").audioPath).toBe("https://x.test/talk.opus?v=3");
  });
});

describe("resolveEmbedTheme", () => {
  it("follows the page's reader when nothing is asked for", () => {
    expect(resolveEmbedTheme(null, true)).toBe("saturn-dark");
    expect(resolveEmbedTheme(null, false)).toBe("saturn-light");
    expect(resolveEmbedTheme("auto", true)).toBe("saturn-dark");
  });

  it("honours an explicit choice whatever the reader prefers", () => {
    expect(resolveEmbedTheme("dark", false)).toBe("saturn-dark");
    expect(resolveEmbedTheme(" LIGHT ", true)).toBe("saturn-light");
  });

  it("treats a value it does not know as no choice at all", () => {
    expect(resolveEmbedTheme("solarized", true)).toBe("saturn-dark");
  });
});
