import { describe, expect, it } from "vitest";
import { render } from "svelte/server";

import { tagsByMeeting, type VocabularyTag } from "../viewer/annotations";
import MeetingList from "./MeetingList.svelte";
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
    expect(meetingListSource).toMatch(/<input\n\s+type="checkbox"/);
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
    // narrowing hides — the text filter never leaves this component. The TYPE
    // filter has to count the same way: with Meetings switched off the list
    // draws no meeting rows at all, so reporting the search-filtered set would
    // leave the selection bar claiming nothing was hidden while every pick was.
    expect(meetingListSource).toContain(
      'dispatch("visible", types.meetings ? visibleMeetings : [])',
    );
  });
});

describe("MeetingList insights", () => {
  it("puts insight cards in the same stream as the meeting rows", () => {
    // Not a separate tab and not a second list: an insight belongs beside the
    // conversations it summarises, under the same month headings.
    expect(meetingListSource).toContain("{#each feedGroups as group (group.key)}");
    expect(meetingListSource).toContain("{#each group.items as item (item.key)}");
    expect(meetingListSource).toContain("<InsightCard");
  });

  it("never lets an insight card be picked into a bundle", () => {
    // A context bundle is made of meetings. The checkbox lives inside the
    // meeting branch, and the insight branch has no pick control at all.
    const insightBranch = meetingListSource.slice(
      meetingListSource.indexOf('{#if item.kind === "insight"}'),
      meetingListSource.indexOf("{@const meeting = item.meeting}"),
    );
    expect(insightBranch).not.toContain("<input");
    expect(insightBranch).not.toContain('dispatch("pick"');
    expect(insightBranch).toContain('dispatch("openInsight", item.insight)');
  });

  it("takes the type filter from the shell rather than owning one", () => {
    // The control moved into the rooms rail — both are narrowings of the same
    // archive and belong in the same column — so the shell owns the filter and
    // both surfaces read the one copy of it. See RoomsRail.test.ts for the
    // control itself.
    expect(meetingListSource).toContain("export let types: BrowseTypeFilter = ALL_BROWSE_TYPES;");
    expect(meetingListSource).not.toContain('class="typefilter"');
    expect(meetingListSource).not.toContain("toggleBrowseType");
  });

  it("reports what is behind each Show box, under the search only it knows", () => {
    // The rail draws the boxes and has to say what each one would bring back.
    // Counted BEFORE the type filter is applied: the question a box answers is
    // "what would I get if I ticked this?".
    expect(meetingListSource).toContain('dispatch("counts", {');
    expect(meetingListSource).toContain("meetings: visibleMeetings.length,");
    expect(meetingListSource).toContain("insights: visibleInsights.length,");
  });

  it("tells a failed listing apart from an empty one", () => {
    // Three states, and none of them is the other two: a count once a listing
    // has come back, the failure when it did not, and nothing while the first
    // is still in flight.
    expect(meetingListSource).toContain("{#if insightsError}");
    expect(meetingListSource).toContain("Insights could not be listed:");
    expect(meetingListSource).toMatch(
      /\{#if insightsOffered && insightsLoaded\}[\s\S]{0,240}\{:else if insightsOffered && insightsError\}/,
    );
  });

  it("claims you have no insights only where it knows that", () => {
    // The claim is about the whole archive, so every narrowing has to be off
    // and the listing has to have come back: a room selected, a search typed, a
    // listing in flight or a listing that FAILED each make it false, and each
    // has its own words in the branch below it.
    expect(meetingListSource).toContain(
      "feedItems.length === 0 && insightsOnly && !trimmedFilter && " +
        "selectedRoomName === null && insightsLoaded && !insightsError && totalInsightCount === 0",
    );
  });

  it("names the search box after the kinds it is narrowing", () => {
    // It narrows insights too, and narrows insights ALONE when the Meetings
    // toggle is off.
    expect(meetingListSource).toContain(
      "placeholder={`Search ${matchNounPlural} by name or date`}",
    );
    expect(meetingListSource).toContain(
      "aria-label={`Search ${matchNounPlural} by name or date`}",
    );
  });

  it("counts an insight's sources with a number the shell resolved", () => {
    // Not insight.meetingIds.length: a source this caller may not read is
    // absent, and a count would disclose that it exists.
    expect(meetingListSource).toContain(
      "sourceCount={insightSourceCounts.get(item.insight.id) ?? 0}",
    );
  });
});

describe("MeetingList tags", () => {
  const tag = (tagId: string): VocabularyTag => ({
    tagId,
    namespace: "ns",
    label: `tag-${tagId}`,
    meetings: 1,
    marks: 1,
    color: "teal",
    icon: "",
    changedBy: "",
    changedAtUtc: "",
  });
  const tags = ["a", "b", "c", "d"].map(tag);
  const meetingTags = tagsByMeeting({
    tags,
    meetings: [
      {
        meetingId: "m1",
        tags: [
          { tagId: "a", whole: true, stretches: 0 },
          { tagId: "b", whole: false, stretches: 3 },
          { tagId: "c", whole: true, stretches: 0 },
          { tagId: "d", whole: false, stretches: 1 },
        ],
      },
    ],
    coverage: { visible: 1, indexed: 1 },
  });
  const meetings = [{ id: "m1", title: "Hiring sync", dateLabel: "2026-09-01" }];
  const html = (props: Record<string, unknown>) =>
    render(MeetingList as never, { props: { meetings, totalCount: 1, ...props } } as never).body;

  it("shows three chips on a row, whole-meeting tags first, then how many more", () => {
    const row = html({ meetingTags, tags });
    expect(row.match(/class="tag-chip[\s"]/g)).toHaveLength(3);
    expect(row.match(/class="tag-chip\s[^"]*\bwhole\b/g)).toHaveLength(2);
    expect(row).toMatch(/>3<span class="sr-only[^"]*"> stretches</);
    expect(row).toMatch(/title="tag-d"[^>]*>\+1</);
  });

  it("offers a tag button on each row only where tagging is", () => {
    expect(html({ meetingTags, tags })).toContain('aria-label="Tag Hiring sync"');
    const plain = html({});
    expect(plain).not.toContain("row-tag");
    expect(plain).not.toContain("tag-chip");
  });

  it("toggles whole-meeting tags from the row, ticking the ones already on it", () => {
    expect(meetingListSource).toContain("selected={wholeTagState(meetingTags, [meeting.id]).selected}");
    expect(meetingListSource).toContain('dispatch("tagMeeting", { meeting, pick: event.detail })');
  });

  it("says when the tag filter is what emptied the list", () => {
    expect(html({ meetings: [], tagFilterCount: 2, tags })).toContain("No meeting here has those tags.");
  });
});
