import { mount, unmount } from "svelte";
import { afterEach, beforeEach, describe, expect, it } from "vitest";
import { page } from "vitest/browser";

import MeetingView from "../MeetingView.svelte";
import "../../app.css";
import {
  ROOM,
  meeting,
  named,
  original,
  renamed,
  separated,
  separatedWithOriginal,
  speakersFixture,
  splitReport,
} from "./speakers.fixture";

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
    props: {
      dataProvider: provider, meeting, surface, bundled, isDesktop: true,
      speakerEditsPollMs: 20, speakerEditsClock: fixture.time.clock,
    },
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
const status = () => details().getByRole("status").filter({ hasText: /Saving|Starting|Separating|Updating|Waiting|Loading|Voices updated|Couldn't update/ });
const transcript = () => page.getByRole("log", { name: "Transcript" });
// The speaker named on each turn of the transcript, in order.
const turnNames = () => [...root.querySelectorAll('[role="log"] article .mv-speaker-name:not(.mv-speaker-inline)')].map((badge) => badge.textContent?.trim());
const freshLoads = () => fixture.loadMeeting.mock.calls.filter(([, options]) => options?.fresh).length;
const player = () => root.querySelector("audio")!;
const bar = () => details().getByRole("progressbar");

async function separateRoom() {
  await openPeople();
  await details().getByRole("button", { name: "Actions for Meeting room laptop" }).click();
  await page.getByRole("menuitem", { name: "Separate voices" }).click();
}

// Separating a device changes how the words are cut, so the republished
// recording is read again on its own once the operator has applied it.
async function separateAndReload(next = separated) {
  await separateRoom();
  await expect.element(details().getByText("Separating voices…")).toBeVisible();
  fixture.applied(next, splitReport([`${ROOM}~1`, `${ROOM}~2`, `${ROOM}~3`]));
  await expect.element(nameField(voice(1))).toBeVisible();
}

describe("separating the voices on a shared device", () => {
  it("separates a device, then reads the republished recording again on its own", async () => {
    fixture = speakersFixture();
    mountView();
    await openPeople();
    await expect.element(people()).toHaveTextContent("2 participants");
    await expect.element(details().getByText("Meeting room laptop")).toBeVisible();
    await shot("01-participants-before-split");

    // Names are said to travel with the recording only once there are voices to name.
    await expect.element(details().getByText(/^Names are saved in the recording/)).not.toBeInTheDocument();

    // Separating is the device's own action, done at once: no confirmation.
    await details().getByRole("button", { name: "Actions for Meeting room laptop" }).click();
    const item = page.getByRole("menuitem", { name: "Separate voices", exact: true });
    await expect.element(item).toHaveAccessibleDescription("Several people used this device");
    await page.screenshot({ path: `${SHOTS}/02-separate-menu.png` });
    expect(fixture.save).not.toHaveBeenCalled();
    await item.click();
    expect(fixture.save).toHaveBeenCalledExactlyOnceWith(meeting, 0, { splits: [{ speakerId: ROOM }], merges: [], labels: [] });
    await expect.element(page.getByRole("menu")).not.toBeInTheDocument();
    await expect.element(details().getByRole("group", { name: /Separate the voices/ })).not.toBeInTheDocument();
    // An operator that does not say how far it is gets today's words, and no bar.
    await expect.element(status()).toHaveTextContent(/^Separating voices…$/);
    await expect.element(bar()).not.toBeInTheDocument();
    await shot("03-separating-status");
    // Nothing else can be started while the recording is being updated.
    await details().getByRole("button", { name: "Actions for Meeting room laptop" }).click();
    await expect.element(page.getByRole("menuitem", { name: "Treat as one person again" })).toBeDisabled();

    const playerBefore = root.querySelector("audio");
    expect(playerBefore).not.toBeNull();
    let release!: () => void;
    const held = fixture.loadMeeting.getMockImplementation()!;
    fixture.loadMeeting.mockImplementationOnce((...args) => new Promise((resolve) => (release = () => resolve(held(...args)))));
    fixture.applied(separated, splitReport([`${ROOM}~1`, `${ROOM}~2`, `${ROOM}~3`]));
    // Nothing is playing, so the new recording is read at once, without a
    // Reload to press. It stays readable while the new copy arrives, rather
    // than blanking.
    await expect.poll(() => release).toBeTypeOf("function");
    await expect.element(status()).toHaveTextContent(/^Loading the updated recording…$/);
    await expect.element(details().getByRole("button", { name: "Reload" })).not.toBeInTheDocument();
    await expect.element(page.getByText("Loading meeting…")).not.toBeInTheDocument();
    await expect.element(people()).toHaveTextContent("2 participants");
    await page.screenshot({ element: details(), path: `${SHOTS}/immediacy-03-split-loading.png` });
    release();

    await expect.element(people()).toHaveTextContent("4 voices on 2 devices");
    expect(fixture.loadMeeting).toHaveBeenLastCalledWith(meeting, { fresh: true });
    for (const n of [1, 2, 3]) await expect.element(nameField(voice(n))).toBeVisible();
    await expect.element(details().getByText("Voices updated ·")).not.toBeInTheDocument();
    await expect
      .element(details().getByText("Names are saved in the recording and visible to everyone who can open it."))
      .toBeVisible();
    // The republished file is at the same address but its bytes moved: the
    // player is a new one, opened on the new file.
    expect(root.querySelector("audio")).not.toBeNull();
    expect(root.querySelector("audio")).not.toBe(playerBefore);
    // A voice heard for only a few seconds is flagged; one heard at length is not.
    expect(root.querySelectorAll(".pp-hint")).toHaveLength(2);
    // The transcript's turns now carry the voices.
    await expect.element(page.getByText(voice(2)).first()).toBeVisible();
  });

  it("says how long is left, phase by phase, counting down between the operator's answers", async () => {
    fixture = speakersFixture({ estimateMs: 75_000 });
    // The screenshots show the bar where it is, not part way through its glide.
    const still = document.createElement("style");
    still.textContent = ".pp-progress-fill { transition: none !important; }";
    root.append(still);
    mountView();
    // The save's answer is always a queued attempt: the build worker takes it
    // a moment later. Until it has waited a while, it is starting, with the
    // whole estimate to count down from the click.
    fixture.phase("queued");
    await separateRoom();
    await expect.element(status()).toHaveTextContent(/^Starting…\s*about 1 min left$/);
    await expect.element(bar()).toHaveAttribute("aria-valuenow", "0");
    await shot("eta-00-starting");

    // Still queued five seconds on: the operator is busy with another
    // recording. Nothing has started, so no countdown and no bar.
    fixture.time.advance(5000);
    await expect.element(status()).toHaveTextContent(/^Waiting for other recordings…$/);
    await expect.element(bar()).not.toBeInTheDocument();
    await shot("eta-01-queued");

    // The diarizer starts on the room's track.
    fixture.phase("separating");
    await expect.element(status()).toHaveTextContent(/^Separating voices…\s*about 1 min left$/);
    await expect.element(bar()).toHaveAttribute("aria-valuenow", "7");
    await expect.element(bar()).toHaveAttribute("aria-valuetext", "about 1 min left");

    // The time left counts down by itself while the operator's next answer is
    // still on its way.
    let release!: () => void;
    const answer = fixture.load.getMockImplementation()!;
    fixture.load.mockImplementationOnce((...args) => new Promise((resolve) => (release = () => resolve(answer(...args)))));
    await expect.poll(() => release).toBeTypeOf("function");
    const asked = fixture.load.mock.calls.length;
    fixture.time.advance(25_000);
    await expect.element(status()).toHaveTextContent(/^Separating voices…\s*about 45 s left$/);
    await expect.element(bar()).toHaveAttribute("aria-valuenow", "40");
    expect(fixture.load.mock.calls.length).toBe(asked);
    await shot("eta-02-separating");
    release();

    // The voices are found; the recording and its summary are rewritten.
    fixture.phase("updating");
    fixture.time.advance(35_000);
    await expect.element(status()).toHaveTextContent(/^Updating the recording…\s*about 10 s left$/);
    await expect.element(bar()).toHaveAttribute("aria-valuenow", "87");
    await shot("eta-03-updating");

    // Past the estimate: no negative time, and the bar waits short of full.
    fixture.time.advance(15_000);
    await expect.element(status()).toHaveTextContent(/^Updating the recording…\s*almost done$/);
    await expect.element(bar()).toHaveAttribute("aria-valuenow", "95");
    await shot("eta-04-almost-done");

    fixture.applied(separated, splitReport([`${ROOM}~1`, `${ROOM}~2`, `${ROOM}~3`]));
    await expect.element(people()).toHaveTextContent("4 voices on 2 devices");
    await expect.element(bar()).not.toBeInTheDocument();
    // Nothing is left counting.
    expect(fixture.time.ticking()).toBe(0);
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

  it("reads the new recording for a reader who comes back after it was updated", async () => {
    fixture = speakersFixture();
    mountView();
    await separateRoom();
    await expect.element(details().getByText("Separating voices…")).toBeVisible();
    // The reader opens another meeting; the apply finishes meanwhile.
    await unmount(view!);
    view = undefined;
    fixture.applied(separated, splitReport([`${ROOM}~1`, `${ROOM}~2`, `${ROOM}~3`]));

    mountView();
    // The tab still has the copy it read before; the operator says a newer
    // one, separated differently, exists, and it is read.
    await expect.poll(freshLoads).toBe(1);
    await openPeople();
    await expect.element(people()).toHaveTextContent("4 voices on 2 devices");
    await expect.element(details().getByText("Voices updated ·")).not.toBeInTheDocument();

    // Opened again with the new copy, nothing more is offered.
    await reopen();
    await openPeople();
    await expect.element(nameField(voice(1))).toBeVisible();
    await expect.element(details().getByText("Voices updated ·")).not.toBeInTheDocument();
    expect(freshLoads()).toBe(1);
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
    // …and the People list counts the merged voices as one at once.
    await expect.element(people()).toHaveTextContent("3 voices on 2 devices");

    const loads = fixture.loadMeeting.mock.calls.length;
    fixture.applied(named);
    await expect.element(details().getByText("Updating the recording…")).not.toBeInTheDocument();
    // Names and merges need nothing read again: what is on screen already says it.
    await expect.element(details().getByRole("button", { name: "Reload" })).not.toBeInTheDocument();
    expect(fixture.loadMeeting.mock.calls.length).toBe(loads);
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
    fixture.loadMeeting.mockRejectedValueOnce(new Error("Could not load m1.opus."));
    fixture.applied(separated, splitReport([`${ROOM}~1`, `${ROOM}~2`]));
    await expect.element(details().getByRole("alert")).toHaveTextContent("Couldn't reload the recording: Could not load m1.opus.");
    // Offered by hand, since the recording is still the old one; read again
    // on its own only once per apply.
    await expect.element(details().getByRole("button", { name: "Reload" })).toBeVisible();
    expect(freshLoads()).toBe(1);
    await details().getByRole("button", { name: "Reload" }).click();
    await expect.element(people()).toHaveTextContent("4 voices on 2 devices");
    expect(freshLoads()).toBe(2);
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

    // The words are cut differently again, so the recording is read again.
    fixture.applied(original, { splits: [], inconclusive: [] });
    await expect.element(people()).toHaveTextContent("2 participants");
    expect(freshLoads()).toBe(2);
    await expect.element(details().getByText("Voices updated ·")).not.toBeInTheDocument();
  });

  it("says when only one voice was found on a device", async () => {
    fixture = speakersFixture();
    mountView();
    await separateRoom();
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
    fixture.applied(separated, { ...splitReport([`${ROOM}~1`, `${ROOM}~2`, `${ROOM}~3`]), summary: "stale" });
    await expect
      .element(details().getByText("The summary was written before these speaker changes."))
      .toBeVisible();
  });

  // The People panel and the header count the meeting's people whichever
  // transcript is shown.
  it("keeps the voices nameable on the original transcript of a separated meeting", async () => {
    fixture = speakersFixture();
    mountView();
    await separateAndReload(separatedWithOriginal);
    const chip = page.getByRole("button", { name: "Meeting details" });
    await expect.element(chip.getByTitle("4 voices on 2 devices")).toHaveTextContent("4");

    await page.getByRole("button", { name: "Original", exact: true }).click();
    await expect.element(page.getByRole("button", { name: "Original", exact: true })).toHaveAttribute("aria-pressed", "true");
    // The original transcript credits the room's device, but the meeting
    // still had the same people: no device counted on top of its voices.
    await expect.element(chip.getByTitle("4 voices on 2 devices")).toHaveTextContent("4");

    // The click outside closed the details; open them again.
    await expect.element(details()).not.toBeInTheDocument();
    await openPeople();
    await expect.element(people()).toHaveTextContent("4 voices on 2 devices");
    for (const n of [1, 2, 3]) await expect.element(nameField(voice(n))).toBeEnabled();
    await expect.element(details().getByRole("button", { name: `Play a sample of ${voice(1)}` })).toBeEnabled();
    await expect.element(details().getByRole("button", { name: `${voice(2)} is the same person as…` })).toBeEnabled();
    await expect.element(details().getByText("Separating voices…")).not.toBeInTheDocument();
    await details().getByRole("button", { name: "Actions for Meeting room laptop" }).click();
    await expect.element(page.getByRole("menuitem", { name: "Treat as one person again" })).toBeEnabled();
    await details().getByRole("button", { name: "Actions for Meeting room laptop" }).click();
    await expect.element(page.getByRole("menuitem", { name: "Treat as one person again" })).not.toBeInTheDocument();

    fixture.save.mockClear();
    await nameField(voice(1)).fill("Mira");
    await details().getByRole("button", { name: "Save", exact: true }).click();
    expect(fixture.save).toHaveBeenCalledExactlyOnceWith(meeting, 1, {
      splits: [{ speakerId: ROOM }],
      merges: [],
      labels: [{ speakerId: `${ROOM}~1`, label: "Mira" }],
    });
  });

  it("offers to retry when the recording could not be updated", async () => {
    fixture = speakersFixture();
    mountView();
    await separateRoom();
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
    await expect.element(page.getByRole("menuitem", { name: "Separate voices" })).toBeDisabled();
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
    await expect.element(page.getByText(/Voice separation is not installed on this server/)).not.toBeInTheDocument();
  });
});

describe("changes shown as soon as they are saved", () => {
  const immediacyShot = (name: string, element?: ReturnType<typeof details>) =>
    page.screenshot({ ...(element ? { element } : {}), path: `${SHOTS}/immediacy-${name}.png` });

  it("puts saved names on the transcript at once, and never asks for a reload", async () => {
    fixture = speakersFixture({ estimateMs: 15_000 });
    mountView();
    await separateAndReload();
    const loads = fixture.loadMeeting.mock.calls.length;
    expect(turnNames()).toContain(voice(1));

    await nameField(voice(1)).fill("Mira");
    await nameField(voice(2)).fill("Leo");
    fixture.phase("updating");
    await details().getByRole("button", { name: "Save", exact: true }).click();
    await expect.element(status()).toHaveTextContent(/^Updating the recording…\s*about 15 s left$/);

    // The turns carry the new names straight from the save's answer, while
    // the operator is still rewriting the recording.
    await expect.poll(turnNames).toEqual(["Mira", "Ben Ortiz", "Leo", "Mira", voice(3), "Leo", "Ben Ortiz"]);
    await expect.element(details().getByRole("button", { name: "Reload" })).not.toBeInTheDocument();
    expect(fixture.loadMeeting.mock.calls.length).toBe(loads);
    await immediacyShot("01a-panel-while-updating", details());
    await page.getByRole("button", { name: "Meeting details" }).click();
    await expect.element(details()).not.toBeInTheDocument();
    await immediacyShot("01b-transcript-while-updating");

    // Copy takes the names too.
    let copied = "";
    Object.defineProperty(navigator, "clipboard", {
      configurable: true,
      value: { writeText: async (text: string) => void (copied = text) },
    });
    await page.getByRole("button", { name: "Export" }).click();
    await page.getByRole("menuitem", { name: "Copy transcript" }).click();
    await expect.poll(() => copied).toContain("**Mira**");
    expect(copied).toContain("**Leo**");
    expect(copied).not.toContain("Speaker 1");

    // Applied: nothing to reload, nothing read again; the names stay.
    fixture.applied(renamed);
    await openPeople();
    await expect.element(details().getByText("Updating the recording…")).not.toBeInTheDocument();
    await expect.element(details().getByRole("button", { name: "Reload" })).not.toBeInTheDocument();
    await expect.element(details().getByText("Voices updated ·")).not.toBeInTheDocument();
    expect(fixture.loadMeeting.mock.calls.length).toBe(loads);
    expect(turnNames()).toEqual(["Mira", "Ben Ortiz", "Leo", "Mira", voice(3), "Leo", "Ben Ortiz"]);
    await immediacyShot("02-names-applied", details());

    // Opened again later in the tab, from the copy read before: still named.
    await reopen();
    await expect.poll(turnNames).toEqual(["Mira", "Ben Ortiz", "Leo", "Mira", voice(3), "Leo", "Ben Ortiz"]);
    expect(freshLoads()).toBe(1);
  });

  it("joins a merged voice's turns into the other voice's at once", async () => {
    fixture = speakersFixture();
    mountView();
    await separateAndReload();
    const loads = fixture.loadMeeting.mock.calls.length;
    expect(turnNames()).toEqual([voice(1), "Ben Ortiz", voice(2), voice(1), voice(3), voice(2), "Ben Ortiz"]);

    await details().getByRole("button", { name: `${voice(3)} is the same person as…` }).click();
    await page.getByRole("menuitem", { name: "Speaker 1" }).click();
    await details().getByRole("button", { name: "Save", exact: true }).click();
    await expect.element(details().getByText("Updating the recording…")).toBeVisible();

    // Voice 3's one turn follows voice 1's, so the two are one turn now: its
    // words go on under voice 1's name, with no name of their own.
    await expect.poll(turnNames).toEqual([voice(1), "Ben Ortiz", voice(2), voice(1), voice(2), "Ben Ortiz"]);
    await expect.element(people()).toHaveTextContent("3 voices on 2 devices");
    await expect.element(details().getByText("Includes Speaker 3")).toBeVisible();
    expect(fixture.loadMeeting.mock.calls.length).toBe(loads);
    await immediacyShot("05-merge-at-once");
  });

  it("puts a failed save's names back and says it failed", async () => {
    fixture = speakersFixture();
    mountView();
    await separateAndReload();
    await nameField(voice(1)).fill("Mira");
    await details().getByRole("button", { name: "Save", exact: true }).click();
    await expect.poll(turnNames).toContain("Mira");

    fixture.failed("apply: exit status 1");
    await expect.element(details().getByText("Couldn't update voices")).toBeVisible();
    await expect.poll(turnNames).toEqual([voice(1), "Ben Ortiz", voice(2), voice(1), voice(3), voice(2), "Ben Ortiz"]);
  });

  it("leaves a new split for the reader to load while they are listening", async () => {
    fixture = speakersFixture();
    mountView();
    await separateRoom();
    await expect.element(details().getByText("Separating voices…")).toBeVisible();
    await page.getByRole("button", { name: "Play", exact: true }).click();
    await expect.poll(() => player().paused).toBe(false);
    const listening = player();

    fixture.applied(separated, splitReport([`${ROOM}~1`, `${ROOM}~2`, `${ROOM}~3`]));
    // Pressing Play closed the details; open them again.
    await openPeople();
    await expect.element(details().getByText("Voices updated ·")).toBeVisible();
    // The player is not interrupted.
    expect(player()).toBe(listening);
    expect(listening.paused).toBe(false);
    expect(freshLoads()).toBe(0);
    await immediacyShot("04-split-while-playing", details());

    // Pausing does not read it after all: the reader asks for it.
    listening.pause();
    await expect.poll(() => player().paused).toBe(true);
    await expect.element(details().getByText("Voices updated ·")).toBeVisible();
    expect(freshLoads()).toBe(0);
    await details().getByRole("button", { name: "Reload" }).click();
    await expect.element(people()).toHaveTextContent("4 voices on 2 devices");
  });

  it("does not replace the player when the reader starts listening while the recording is read", async () => {
    fixture = speakersFixture();
    mountView();
    await separateRoom();
    await expect.element(details().getByText("Separating voices…")).toBeVisible();
    let release!: () => void;
    const held = fixture.loadMeeting.getMockImplementation()!;
    fixture.loadMeeting.mockImplementationOnce((...args) => new Promise((resolve) => (release = () => resolve(held(...args)))));
    fixture.applied(separated, splitReport([`${ROOM}~1`, `${ROOM}~2`, `${ROOM}~3`]));
    // Nothing was playing, so the new recording is being read on its own…
    await expect.poll(() => release).toBeTypeOf("function");

    // …when the reader presses Play.
    await page.getByRole("button", { name: "Play", exact: true }).click();
    await expect.poll(() => player().paused).toBe(false);
    await expect.element(page.getByRole("button", { name: "Pause", exact: true })).toBeVisible();
    const listening = player();
    release();

    await openPeople();
    await expect.element(details().getByText("Voices updated ·")).toBeVisible();
    expect(player()).toBe(listening);
    expect(listening.paused).toBe(false);
    await expect.element(people()).toHaveTextContent("2 participants");
    await details().getByRole("button", { name: "Reload" }).click();
    await expect.element(people()).toHaveTextContent("4 voices on 2 devices");
  });

  it("keeps the reader's place in the recording when it is read again", async () => {
    fixture = speakersFixture();
    mountView();
    await separateRoom();
    await expect.poll(() => player().readyState).toBeGreaterThan(0);
    const before = player();
    before.currentTime = 12;
    await expect.poll(() => before.currentTime).toBe(12);
    before.dispatchEvent(new Event("timeupdate"));

    fixture.applied(separated, splitReport([`${ROOM}~1`, `${ROOM}~2`, `${ROOM}~3`]));
    await expect.element(people()).toHaveTextContent("4 voices on 2 devices");
    expect(player()).not.toBe(before);
    await expect.poll(() => player().currentTime).toBe(12);
  });
});

describe("taking a saved change back", () => {
  it("puts a voice's default name back at once when its name is cleared", async () => {
    fixture = speakersFixture();
    mountView();
    await separateAndReload();
    await nameField(voice(1)).fill("Mira");
    await nameField(voice(2)).fill("Leo");
    await details().getByRole("button", { name: "Save", exact: true }).click();
    fixture.applied(renamed);

    // Opened later in a new tab: the recording on screen carries the names.
    fixture.forget();
    await reopen();
    await expect.poll(turnNames).toEqual(["Mira", "Ben Ortiz", "Leo", "Mira", voice(3), "Leo", "Ben Ortiz"]);
    await openPeople();
    await nameField("Mira").fill("");
    await details().getByRole("button", { name: "Save", exact: true }).click();
    expect(fixture.save).toHaveBeenLastCalledWith(meeting, 2, {
      splits: [{ speakerId: ROOM }],
      merges: [],
      labels: [{ speakerId: `${ROOM}~2`, label: "Leo" }],
    });
    // Voice 1 is called what the operator will call it, before it has.
    await expect.poll(turnNames).toEqual([voice(1), "Ben Ortiz", "Leo", voice(1), voice(3), "Leo", "Ben Ortiz"]);
  });

  it("reads the recording again when a merged voice is said to be a different person", async () => {
    fixture = speakersFixture();
    mountView();
    await separateAndReload();
    await nameField(voice(1)).fill("Mira");
    await nameField(voice(2)).fill("Leo");
    await details().getByRole("button", { name: `${voice(3)} is the same person as…` }).click();
    await page.getByRole("menuitem", { name: "Mira" }).click();
    await details().getByRole("button", { name: "Save", exact: true }).click();
    fixture.applied(named);

    // Opened later in a new tab: voice 3's words are Mira's in the recording.
    fixture.forget();
    await reopen();
    await expect.poll(turnNames).toEqual(["Mira", "Ben Ortiz", "Leo", "Mira", "Leo", "Ben Ortiz"]);
    expect(freshLoads()).toBe(1);
    await openPeople();
    await details().getByRole("button", { name: "Not the same person" }).click();
    await details().getByRole("button", { name: "Save", exact: true }).click();
    await expect.element(details().getByText("Updating the recording…")).toBeVisible();

    // Only the republished recording has voice 3's words on their own again.
    fixture.applied(renamed);
    await expect.poll(freshLoads).toBe(2);
    await expect.poll(turnNames).toEqual(["Mira", "Ben Ortiz", "Leo", "Mira", voice(3), "Leo", "Ben Ortiz"]);
    await expect.element(details().getByText("Voices updated ·")).not.toBeInTheDocument();
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

  // An embed has no operator to say what the participants were.
  it("names a device whose voices all have names from the file itself", async () => {
    fixture = speakersFixture();
    fixture.applied(named, splitReport([`${ROOM}~1`, `${ROOM}~2`]));
    mountView("embed");
    await openPeople();
    await expect.element(people()).toHaveTextContent("3 voices on 2 devices");
    await expect.element(details().getByText("Meeting room laptop", { exact: true })).toBeVisible();
    await expect.element(details().getByText("Shared device")).not.toBeInTheDocument();
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
