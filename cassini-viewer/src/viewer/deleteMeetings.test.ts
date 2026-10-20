import { describe, expect, it } from "vitest";

import appSource from "../App.svelte?raw";
import insightDocumentSource from "../components/InsightDocument.svelte?raw";
import meetingListSource from "../components/MeetingList.svelte?raw";
import meetingViewSource from "../components/MeetingView.svelte?raw";
import selectionBarSource from "../components/SelectionBar.svelte?raw";
import confirmDialogSource from "../components/ui/ConfirmDialog.svelte?raw";

// Source-level assertions, the convention this repo follows for .svelte files:
// the suite runs in node with no DOM harness. What is asserted is the delete
// wiring (D-861): every entry point passes one confirm dialog, the removal is
// optimistic but cannot be flashed back by the catalog poll, and a failure
// rolls the row back in and says so.

describe("the shell's meeting delete", () => {
  it("is offered exactly where the provider can perform one", () => {
    // The capability is the provider method's presence, the same rule every
    // optional capability follows (Prepare, insights, tagging).
    expect(appSource).toContain("$: canDelete = Boolean(dataProvider.deleteMeeting);");
    expect(appSource).toContain("deletable={canDelete}");
    expect(appSource).toContain("{canDelete}");
  });

  it("routes every entry point through the one confirm dialog", () => {
    // Sheet header and list row both land in requestDeleteMeetings, which only
    // arms the dialog; the delete itself runs from the dialog's confirm.
    expect(appSource).toContain("on:deleteMeeting={(event) => requestDeleteMeetings([event.detail])}");
    expect(appSource).toContain("on:deleteMeeting={() => selectedMeeting && requestDeleteMeetings([selectedMeeting])}");
    expect(appSource).toContain("deleteConfirm = entries;");
    expect(appSource).toContain("on:confirm={confirmDeleteMeetings}");
    // The dialog is the topmost layer: everything under it goes inert.
    expect(appSource).toContain("$: topLayer = deleteConfirm ? 5 :");
  });

  it("removes optimistically and keeps the poll from flashing the row back", () => {
    const runner = appSource.slice(
      appSource.indexOf("async function runMeetingDeletes"),
      appSource.indexOf("function confirmDeleteMeetings"),
    );
    expect(runner).toContain("deletedMeetingIds.add(entry.id);");
    expect(runner).toContain("catalogMeetings = catalogMeetings.filter((listed) => listed.id !== entry.id);");
    // The open sheet closes rather than keeping a deleted meeting on screen.
    expect(runner).toContain("handleBackToList();");
    // refreshCatalog applies the same memory to every listing it accepts.
    expect(appSource).toContain(
      "const listedMeetings = catalog.meetings.filter((entry) => !deletedMeetingIds.has(entry.id));",
    );
  });

  it("reads a 404 as the terminal answer and rolls real failures back", () => {
    const runner = appSource.slice(
      appSource.indexOf("async function runMeetingDeletes"),
      appSource.indexOf("function confirmDeleteMeetings"),
    );
    // Out of the readable set means gone — a replay or someone else's delete.
    expect(runner).toContain("error instanceof AnnotationError && error.status === 404");
    // Anything else: the id is forgotten, the entry returns to the list, and
    // the list banner offers a retry over exactly the failed entries.
    expect(runner).toContain("deletedMeetingIds.delete(entry.id);");
    expect(runner).toContain("catalogMeetings = sortMeetingCatalogEntries([...catalogMeetings, ...failed]);");
    expect(appSource).toContain("on:retryDelete={() => void runMeetingDeletes(deleteFailed)}");
  });
});

describe("the list's delete affordance", () => {
  it("shows a per-row delete only when the deployment can delete", () => {
    expect(meetingListSource).toContain("export let deletable = false;");
    expect(meetingListSource).toContain("{#if deletable}");
    expect(meetingListSource).toContain('on:click={() => dispatch("deleteMeeting", meeting)}');
  });

  it("reports a failed delete in the list, with retry and dismiss", () => {
    expect(meetingListSource).toContain("export let deleteNotice = ");
    expect(meetingListSource).toContain('on:click={() => dispatch("retryDelete")}');
    expect(meetingListSource).toContain('on:click={() => dispatch("dismissDeleteNotice")}');
  });
});

describe("the sheet's delete affordance", () => {
  it("offers delete in the header only when the shell can delete and dispatches to it", () => {
    expect(meetingViewSource).toContain("export let canDelete = false;");
    expect(meetingViewSource).toContain("{#if canDelete && meeting}");
    expect(meetingViewSource).toContain('on:click={() => dispatch("deleteMeeting")}');
  });
});

describe("the selection bar's bulk delete", () => {
  it("asks the shell to delete the picked set, behind the same dialog", () => {
    expect(selectionBarSource).toContain("export let deletable = false;");
    expect(selectionBarSource).toContain('on:click={() => dispatch("delete")}');
    expect(appSource).toContain("on:delete={() => requestDeleteMeetings([...pickedMeetings])}");
  });

  it("unpicks a deleted meeting instead of reporting it as an archive loss", () => {
    const runner = appSource.slice(
      appSource.indexOf("async function runMeetingDeletes"),
      appSource.indexOf("function confirmDeleteMeetings"),
    );
    expect(runner).toContain("selection = toggleSelected(selection, entry.id);");
  });
});

describe("the insight document's gone sources", () => {
  it("says a source is gone instead of silently shrinking the list", () => {
    // The shell hands the count down; the document renders a dead row — not a
    // button — and claims neither "deleted" nor "unshared", because it cannot
    // tell which happened.
    expect(appSource).toContain(
      "missingSourceCount={missingInsightSources(selectedInsight, insightSources.get(selectedInsight.id) ?? [])}",
    );
    expect(insightDocumentSource).toContain("export let missingSourceCount = 0;");
    expect(insightDocumentSource).toContain("{#if sources.length > 0 || missingSourceCount > 0}");
    expect(insightDocumentSource).toContain("no longer available (deleted, or no longer shared with you)");
  });
});

describe("the confirm dialog", () => {
  it("lands focus on the safe answer and answers Escape as cancel", () => {
    expect(confirmDialogSource).toContain("data-close");
    expect(confirmDialogSource).toContain('event.key === "Escape"');
    expect(confirmDialogSource).toContain("use:focusLayer");
  });
});
