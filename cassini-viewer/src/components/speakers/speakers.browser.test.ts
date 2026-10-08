import { mount, unmount } from "svelte";
import { afterEach, beforeEach, describe, expect, it } from "vitest";
import { page } from "vitest/browser";

import MeetingView from "../MeetingView.svelte";
import "../../app.css";
import { ROOM, meeting, named, original, separated, speakersFixture, splitReport } from "./speakers.fixture";

// Screenshots for the product review land under __screenshots__ beside this
// file (git-ignored by vitest's cache directory setting) and are copied out by
// whoever runs the suite for that purpose.
const SHOTS = "../../../node_modules/.cache/vitest-screenshots/speakers";

let fixture: ReturnType<typeof speakersFixture>;
let view: ReturnType<typeof mount> | undefined;
let root: HTMLDivElement;

beforeEach(() => {
  root = document.createElement("div");
  root.className = "cassini-root";
  root.dataset.theme = "saturn-light";
  root.style.height = "100vh";
  document.body.append(root);
});

afterEach(async () => {
  if (view) await unmount(view);
  view = undefined;
  fixture?.dispose();
  root.remove();
});

function mountView(surface: "app" | "embed" = "app", provider = fixture.provider, bundled = false) {
  view = mount(MeetingView, {
    target: root,
    props: { dataProvider: provider, meeting, surface, bundled, isDesktop: true, speakerEditsPollMs: 20 },
  });
}

// The reader leaves the meeting and opens it again later in the same tab.
async function reopen() {
  await unmount(view!);
  view = undefined;
  mountView();
}

const details = () => page.getByRole("dialog", { name: "Meeting details" });
const openPeople = () => page.getByRole("button", { name: "Meeting details" }).click();
const people = () => details().getByText(/^\d+ (participants?|voices? on \d+ devices?)$/);
const nameField = (label: string) => details().getByRole("textbox", { name: `Name for ${label}` });
const voice = (n: number) => `Meeting room laptop · Speaker ${n}`;
const shot = (name: string) => page.screenshot({ element: details(), path: `${SHOTS}/${name}.png` });

async function separateRoom() {
  await openPeople();
  await details().getByRole("button", { name: "Actions for Meeting room laptop" }).click();
  await page.getByRole("menuitem", { name: "Several people used this device…" }).click();
}

async function separateAndReload() {
  await separateRoom();
  await details().getByRole("button", { name: "Separate voices" }).click();
  await expect.element(details().getByText("Separating voices…")).toBeVisible();
  fixture.applied(separated, splitReport([`${ROOM}~1`, `${ROOM}~2`, `${ROOM}~3`]));
  await details().getByRole("button", { name: "Reload" }).click();
  await expect.element(nameField(voice(1))).toBeVisible();
}

describe("separating the voices on a shared device", () => {
  it("separates a device, then shows its voices once the recording is reloaded", async () => {
    fixture = speakersFixture();
    mountView();
    await openPeople();
    await expect.element(people()).toHaveTextContent("2 participants");
    await expect.element(details().getByText("Meeting room laptop")).toBeVisible();
    await shot("01-participants-before-split");

    await details().getByRole("button", { name: "Actions for Meeting room laptop" }).click();
    await page.getByRole("menuitem", { name: "Several people used this device…" }).click();
    const confirm = details().getByRole("group", { name: "Separate the voices on Meeting room laptop" });
    await expect.element(confirm).toHaveTextContent(
      "Cassini will separate the voices in Meeting room laptop's audio. Nothing is re-transcribed; tags and marks are kept. " +
        "Names you enter are saved in the recording and visible to everyone who can open it.",
    );
    await shot("02-separate-confirm");
    expect(fixture.save).not.toHaveBeenCalled();

    await confirm.getByRole("button", { name: "Separate voices" }).click();
    await expect.element(details().getByText("Separating voices…")).toBeVisible();
    expect(fixture.save).toHaveBeenCalledExactlyOnceWith(meeting, 0, { splits: [{ speakerId: ROOM }], merges: [], labels: [] });
    await shot("03-separating-status");
    // Nothing else can be started while the recording is being updated.
    await details().getByRole("button", { name: "Actions for Meeting room laptop" }).click();
    await expect.element(page.getByRole("menuitem", { name: "Treat as one person again" })).toBeDisabled();

    fixture.applied(separated, splitReport([`${ROOM}~1`, `${ROOM}~2`, `${ROOM}~3`]));
    await expect.element(details().getByText("Voices updated ·")).toBeVisible();
    // The recording on screen is the old one until the reader asks for the new.
    await expect.element(people()).toHaveTextContent("2 participants");
    const playerBefore = root.querySelector("audio");
    expect(playerBefore).not.toBeNull();
    let release!: () => void;
    const held = fixture.loadMeeting.getMockImplementation()!;
    fixture.loadMeeting.mockImplementationOnce((...args) => new Promise((resolve) => (release = () => resolve(held(...args)))));
    await details().getByRole("button", { name: "Reload" }).click();
    // It stays readable while the new copy arrives, rather than blanking.
    await expect.poll(() => release).toBeTypeOf("function");
    await expect.element(page.getByText("Loading meeting…")).not.toBeInTheDocument();
    await expect.element(people()).toHaveTextContent("2 participants");
    release();

    await expect.element(people()).toHaveTextContent("4 voices on 2 devices");
    expect(fixture.loadMeeting).toHaveBeenLastCalledWith(meeting, { fresh: true });
    for (const n of [1, 2, 3]) await expect.element(nameField(voice(n))).toBeVisible();
    await expect.element(details().getByText("Voices updated ·")).not.toBeInTheDocument();
    // The republished file is at the same address but its bytes moved: the
    // player is a new one, opened on the new file.
    expect(root.querySelector("audio")).not.toBeNull();
    expect(root.querySelector("audio")).not.toBe(playerBefore);
    // A voice heard for only a few seconds is flagged; one heard at length is not.
    expect(root.querySelectorAll(".pp-hint")).toHaveLength(2);
    // The transcript's turns now carry the voices.
    await expect.element(page.getByText(voice(2)).first()).toBeVisible();
  });

  it("plays a voice's longest stretch through the meeting's player and stops by itself", async () => {
    fixture = speakersFixture();
    mountView();
    await separateAndReload();
    const audio = root.querySelector("audio")!;
    const play = details().getByRole("button", { name: `Play a sample of ${voice(3)}` });
    await play.click();
    await expect.element(details().getByRole("button", { name: `Stop the sample of ${voice(3)}` })).toBeVisible();
    // Voice 3's only stretch is 19-20 s; the sample stops at its end.
    await expect.poll(() => audio.paused, { timeout: 5000 }).toBe(true);
    expect(audio.currentTime).toBeGreaterThanOrEqual(19.9);
    expect(audio.currentTime).toBeLessThan(20.6);
    await expect.element(play).toBeVisible();
  });

  it("keeps a sample asked for before the player had read the file", async () => {
    fixture = speakersFixture();
    mountView();
    await separateAndReload();
    const audio = root.querySelector("audio")!;
    // The player has not read the file's header yet the moment the sample is
    // asked for (a slow network, a fresh player): the seek waits for it.
    const real = Object.getOwnPropertyDescriptor(HTMLMediaElement.prototype, "readyState")!.get!;
    let unread = true;
    Object.defineProperty(audio, "readyState", {
      configurable: true,
      get() {
        if (unread) {
          unread = false;
          return 0;
        }
        return real.call(this);
      },
    });
    await details().getByRole("button", { name: `Play a sample of ${voice(3)}` }).click();
    expect(unread).toBe(false);
    // Voice 3's only stretch is 19-20 s; the sample still stops at its end.
    await expect.poll(() => audio.paused, { timeout: 5000 }).toBe(true);
    expect(audio.currentTime).toBeGreaterThanOrEqual(19.9);
    expect(audio.currentTime).toBeLessThan(20.6);
  });

  it("offers the new recording to a reader who comes back after it was updated", async () => {
    fixture = speakersFixture();
    mountView();
    await separateRoom();
    await details().getByRole("button", { name: "Separate voices" }).click();
    await expect.element(details().getByText("Separating voices…")).toBeVisible();
    // The reader opens another meeting; the apply finishes meanwhile.
    await unmount(view!);
    view = undefined;
    fixture.applied(separated, splitReport([`${ROOM}~1`, `${ROOM}~2`, `${ROOM}~3`]));

    mountView();
    await openPeople();
    // The tab still has the copy it read before, and says a newer one exists.
    await expect.element(people()).toHaveTextContent("2 participants");
    await details().getByRole("button", { name: "Reload" }).click();
    await expect.element(people()).toHaveTextContent("4 voices on 2 devices");
    await expect.element(details().getByText("Voices updated ·")).not.toBeInTheDocument();

    // Opened again with the new copy, nothing more is offered.
    await reopen();
    await openPeople();
    await expect.element(nameField(voice(1))).toBeVisible();
    await expect.element(details().getByText("Voices updated ·")).not.toBeInTheDocument();
  });

  it("saves names and a same-person pick as one request", async () => {
    fixture = speakersFixture();
    mountView();
    await separateAndReload();
    fixture.save.mockClear();

    await nameField(voice(1)).fill("Mira");
    await nameField(voice(2)).fill("Leo");
    await details().getByRole("button", { name: `${voice(3)} is the same person as…` }).click();
    // Only the device's own other voices are offered.
    await expect.element(page.getByRole("menuitem", { name: "Ben Ortiz" })).not.toBeInTheDocument();
    await page.getByRole("menuitem", { name: "Mira" }).click();
    await expect.element(details().getByRole("button", { name: `${voice(3)} is not the same person as Mira` })).toBeVisible();
    await shot("04-voices-named");
    await details().getByRole("button", { name: "Save", exact: true }).click();

    expect(fixture.save).toHaveBeenCalledExactlyOnceWith(meeting, 1, {
      splits: [{ speakerId: ROOM }],
      merges: [{ from: `${ROOM}~3`, into: `${ROOM}~1` }],
      labels: [{ speakerId: `${ROOM}~1`, label: "Mira" }, { speakerId: `${ROOM}~2`, label: "Leo" }],
    });
    await expect.element(details().getByText("Updating the recording…")).toBeVisible();
    // The saved names stay in their fields while the recording catches up.
    await expect.element(nameField("Mira")).toHaveValue("Mira");

    fixture.applied(named);
    await details().getByRole("button", { name: "Reload" }).click();
    await expect.element(people()).toHaveTextContent("3 voices on 2 devices");
    await expect.element(details().getByText("Includes Speaker 3")).toBeVisible();
    // Mira already has Speaker 3 merged into her, and merges do not chain.
    await expect.element(details().getByRole("button", { name: "Mira is the same person as…" })).not.toBeInTheDocument();
    await expect.element(details().getByRole("button", { name: "Leo is the same person as…" })).toBeVisible();
    await expect.element(page.getByText("Leo").first()).toBeVisible();
    await shot("05-voices-saved");
    await page.screenshot({ path: `${SHOTS}/07-meeting-with-voices.png` });
    root.dataset.theme = "saturn-dark";
    await shot("06-voices-saved-dark");
    root.dataset.theme = "saturn-light";

    // Saying Speaker 3 is not Mira after all shows at once, before Save.
    fixture.save.mockClear();
    await details().getByRole("button", { name: "Not the same person" }).click();
    await expect.element(details().getByText("Includes Speaker 3")).not.toBeInTheDocument();
    await details().getByRole("button", { name: "Save", exact: true }).click();
    expect(fixture.save).toHaveBeenCalledExactlyOnceWith(meeting, 2, {
      splits: [{ speakerId: ROOM }],
      merges: [],
      labels: [{ speakerId: `${ROOM}~1`, label: "Mira" }, { speakerId: `${ROOM}~2`, label: "Leo" }],
    });
  });

  it("says so when the republished recording cannot be read", async () => {
    fixture = speakersFixture();
    mountView();
    await separateRoom();
    await details().getByRole("button", { name: "Separate voices" }).click();
    fixture.applied(separated, splitReport([`${ROOM}~1`, `${ROOM}~2`]));
    fixture.loadMeeting.mockRejectedValueOnce(new Error("Could not load m1.opus."));
    await details().getByRole("button", { name: "Reload" }).click();
    await expect.element(details().getByRole("alert")).toHaveTextContent("Couldn't reload the recording: Could not load m1.opus.");
    // Still offered, since the recording is still the old one.
    await expect.element(details().getByRole("button", { name: "Reload" })).toBeVisible();
  });

  it("treats a separated device as one person again", async () => {
    fixture = speakersFixture();
    mountView();
    await separateAndReload();
    fixture.save.mockClear();

    await details().getByRole("button", { name: "Actions for Meeting room laptop" }).click();
    await page.getByRole("menuitem", { name: "Treat as one person again" }).click();
    expect(fixture.save).toHaveBeenCalledExactlyOnceWith(meeting, 1, { splits: [], merges: [], labels: [] });
    await expect.element(details().getByText("Updating the recording…")).toBeVisible();
  });

  it("says when only one voice was found on a device", async () => {
    fixture = speakersFixture();
    mountView();
    await separateRoom();
    await details().getByRole("button", { name: "Separate voices" }).click();
    // One voice is no split: the operator republishes the recording unchanged.
    fixture.applied(original, splitReport([`${ROOM}~1`]));
    await expect
      .element(details().getByText("Couldn't separate voices on Meeting room laptop: only one voice found"))
      .toBeVisible();
    await details().getByRole("button", { name: "Actions for Meeting room laptop" }).click();
    await expect.element(page.getByRole("menuitem", { name: "Treat as one person again" })).toBeEnabled();
  });

  it("says when the summary could not be rewritten for the new speakers", async () => {
    fixture = speakersFixture();
    mountView();
    await separateRoom();
    await details().getByRole("button", { name: "Separate voices" }).click();
    fixture.applied(separated, { ...splitReport([`${ROOM}~1`, `${ROOM}~2`, `${ROOM}~3`]), summary: "stale" });
    await expect
      .element(details().getByText("The summary was written before these speaker changes."))
      .toBeVisible();
  });

  it("offers to retry when the recording could not be updated", async () => {
    fixture = speakersFixture();
    mountView();
    await separateRoom();
    await details().getByRole("button", { name: "Separate voices" }).click();
    fixture.failed("diarize: exit status 1");
    await expect.element(details().getByText("Couldn't update voices")).toBeVisible();
    fixture.save.mockClear();

    await details().getByRole("button", { name: "Retry" }).click();
    expect(fixture.save).toHaveBeenCalledExactlyOnceWith(meeting, 1, { splits: [{ speakerId: ROOM }], merges: [], labels: [] });
    await expect.element(details().getByText("Separating voices…")).toBeVisible();
  });

  it("explains why a recording cannot be separated", async () => {
    fixture = speakersFixture({ available: false, reason: "no-source-audio" });
    mountView();
    await openPeople();
    await details().getByRole("button", { name: "Actions for Ben Ortiz" }).click();
    await expect.element(page.getByRole("menuitem", { name: "Several people used this device…" })).toBeDisabled();
    await expect.element(page.getByText("Each participant's own audio was not kept for this recording.")).toBeVisible();
  });

  it("changes nothing on a separated meeting whose participant audio has gone since", async () => {
    fixture = speakersFixture();
    mountView();
    await separateAndReload();
    fixture.availability(false, "no-source-audio");
    await reopen();
    await openPeople();

    await expect.element(nameField(voice(1))).toBeDisabled();
    await expect.element(details().getByRole("button", { name: `${voice(1)} is the same person as…` })).toBeDisabled();
    await details().getByRole("button", { name: "Actions for Meeting room laptop" }).click();
    await expect.element(page.getByRole("menuitem", { name: "Treat as one person again" })).toBeDisabled();
    await expect.element(page.getByText("Each participant's own audio was not kept for this recording.")).toBeVisible();
    // The samples need nothing from the operator.
    await expect.element(details().getByRole("button", { name: `Play a sample of ${voice(1)}` })).toBeEnabled();
  });

  it("still names voices and undoes a split where only the diarizer is missing", async () => {
    fixture = speakersFixture();
    mountView();
    await separateAndReload();
    fixture.availability(false, "diarization-unavailable");
    await reopen();
    await openPeople();

    await expect.element(nameField(voice(1))).toBeEnabled();
    await details().getByRole("button", { name: "Actions for Meeting room laptop" }).click();
    await expect.element(page.getByRole("menuitem", { name: "Treat as one person again" })).toBeEnabled();
    await expect.element(page.getByText("Voice separation is not installed on this server.")).not.toBeInTheDocument();
  });
});

describe("where speakers cannot be changed", () => {
  it("shows an embed the voices with no controls, even from a provider that could save", async () => {
    fixture = speakersFixture();
    fixture.applied(separated, splitReport([`${ROOM}~1`, `${ROOM}~2`, `${ROOM}~3`]));
    mountView("embed");
    await openPeople();
    await expect.element(people()).toHaveTextContent("4 voices on 2 devices");
    await expect.element(details().getByText(voice(2))).toBeVisible();
    expect(details().element().querySelectorAll('[aria-label^="Actions for"], [aria-label^="Play a sample"], input')).toHaveLength(0);
    expect(fixture.load).not.toHaveBeenCalled();
  });

  it("offers nothing in the single-meeting view, even from a provider that could save", async () => {
    fixture = speakersFixture();
    fixture.applied(separated, splitReport([`${ROOM}~1`, `${ROOM}~2`, `${ROOM}~3`]));
    mountView("app", fixture.provider, true);
    await openPeople();
    await expect.element(people()).toHaveTextContent("4 voices on 2 devices");
    expect(details().element().querySelectorAll('[aria-label^="Actions for"], [aria-label^="Play a sample"], input')).toHaveLength(0);
    expect(fixture.load).not.toHaveBeenCalled();
  });

  it("shows a provider without speaker edits the participants alone", async () => {
    fixture = speakersFixture();
    const { loadSpeakerEdits: _load, saveSpeakerEdits: _save, ...readOnly } = fixture.provider;
    mountView("app", readOnly);
    await openPeople();
    await expect.element(details().getByText("Ben Ortiz")).toBeVisible();
    await expect.element(details().getByRole("button", { name: `Actions for Ben Ortiz` })).not.toBeInTheDocument();
  });
});
