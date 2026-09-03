import { describe, expect, it } from "vitest";

import meetingListSource from "./MeetingList.svelte?raw";

// Source-level assertions, for the reason MeetingView.transcript.test.ts gives:
// the suite runs in node with no DOM harness. The row is the product's main
// surface and D-626 rebuilt it, so what is asserted here is what that rebuild
// had to preserve.

describe("MeetingList rows", () => {
  it("keeps picking and opening as two separate controls", () => {
    // A checkbox cannot live inside a button, and demoting the row to a
    // click-handling div would cost it keyboard focus. So the row is a
    // container holding both: an input, and a button that still opens the
    // meeting from anywhere else on the row.
    expect(meetingListSource).toContain('<input\n                  type="checkbox"');
    expect(meetingListSource).toContain('class="row-open"');
    expect(meetingListSource).toContain('dispatch("select", meeting)');
    expect(meetingListSource).toContain('dispatch("pick", meeting)');
  });

  it("gives the checkbox a name of its own", () => {
    // No visible text sits beside it — the row's title is in the button next to
    // it — so the accessible name has to be stated.
    expect(meetingListSource).toContain("aria-label={`Select ${meeting.title}`}");
  });

  it("keeps the open state on the element the row's styling is keyed to", () => {
    // `.meeting-row[aria-current="page"]` is what both this file and app.css's
    // Nextcloud-theme override select on. Moving it off the row would silently
    // strip the open row's fill in the NC build.
    expect(meetingListSource).toMatch(
      /class="meeting-row"[\s\S]{0,120}aria-current=\{meeting\.id === selectedMeetingId/,
    );
  });

  it("offers no checkbox where nothing can act on it", () => {
    // A standalone export has no operator behind it and cannot assemble a
    // bundle, so the shell passes selectable=false and the row renders exactly
    // what it rendered before D-626.
    expect(meetingListSource).toContain("export let selectable = false;");
    expect(meetingListSource).toContain("{#if selectable}");
  });

  it("keeps the whole row opening the meeting, its padding included", () => {
    // Before D-626 the row WAS the button, so its 20px side padding and 9px top
    // and bottom opened the meeting. The button now sits inside that padding,
    // which would leave a strip down each side of every row that highlights on
    // hover and does nothing when clicked — so the hit area is stretched back
    // over the whole row, with the checkbox held above it.
    expect(meetingListSource).toMatch(/\.row-open::before \{[^}]*inset: 0;/);
    expect(meetingListSource).toMatch(/\.row-pick \{[^}]*z-index: 1;/);
  });

  it("reports what it is showing, because its filter is its own", () => {
    // The shell cannot otherwise say how many picked meetings the current
    // narrowing hides — the text filter never leaves this component.
    expect(meetingListSource).toContain('dispatch("visible", visibleMeetings)');
  });
});
