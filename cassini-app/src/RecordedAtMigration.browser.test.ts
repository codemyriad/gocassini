import { mount, tick, unmount } from "svelte";
import { afterEach, beforeEach, expect, it } from "vitest";
import { page, userEvent } from "vitest/browser";
import RecordedAtMigration from "./RecordedAtMigration.svelte";
import { scenarioMigration } from "./preview/doctorScenarios";
import { normalizeRecordedAtMigration, type RecordedAtMigration as Migration } from "./operator/recordedAtMigration";
import "./app.css";

// The Doctor's migration section against a faked operator: what an
// administrator reads before starting, the one button for each state, and the
// pause that lets the files in progress finish.

let app: ReturnType<typeof mount> | undefined;
let host: HTMLDivElement;
let calls: string[];

beforeEach(() => {
  host = document.createElement("div");
  host.className = "op-settings";
  document.body.appendChild(host);
  calls = [];
});

afterEach(() => {
  if (app) unmount(app);
  app = undefined;
  host.remove();
});

// A fake whose reads are resolved by the test, so the poll is observed as an
// event rather than waited for.
function fakeClient(initial: Migration, after: { start?: Migration; pause?: Migration } = {}) {
  let current = initial;
  const reads: Array<() => void> = [];
  return {
    reads,
    client: {
      async getRecordedAtMigration() {
        calls.push("read");
        // What the operator answered when asked, delivered when released.
        const answer = structuredClone(current);
        if (calls.filter(call => call === "read").length > 1) await new Promise<void>(resolve => reads.push(resolve));
        return answer;
      },
      async startRecordedAtMigration() { calls.push("start"); current = after.start ?? current; return structuredClone(current); },
      async pauseRecordedAtMigration() { calls.push("pause"); current = after.pause ?? current; return structuredClone(current); },
    },
    set(next: Migration) { current = next; },
  };
}

function show(client: ReturnType<typeof fakeClient>["client"], pollMs = 1) {
  app = mount(RecordedAtMigration, { target: host, props: { operatorClient: client, pollMs } });
}

// Timers the test fires itself: what is pending is observable, and a re-read
// happens exactly when the test says.
function manualTimers() {
  const pending = new Map<number, () => void>();
  let next = 0;
  return {
    pending,
    timers: {
      set(run: () => void) { pending.set(++next, run); return next; },
      clear(handle: unknown) { pending.delete(handle as number); },
    },
    fire() {
      const runs = [...pending.values()];
      pending.clear();
      for (const run of runs) run();
    },
  };
}

// A client whose every call is answered (or failed) by the test.
function heldClient() {
  const held: Array<{ call: string; resolve: (m: Migration) => void; reject: (e: Error) => void }> = [];
  const call = (name: string) => () => new Promise<Migration>((resolve, reject) => { calls.push(name); held.push({ call: name, resolve, reject }); });
  return {
    held,
    take(name: string) {
      const i = held.findIndex(item => item.call === name);
      if (i < 0) throw new Error(`no ${name} in flight: ${calls.join(",")}`);
      return held.splice(i, 1)[0];
    },
    client: { getRecordedAtMigration: call("read"), startRecordedAtMigration: call("start"), pauseRecordedAtMigration: call("pause") },
  };
}

it("explains what rewriting does before offering to start it", async () => {
  const fake = fakeClient(scenarioMigration("recorded-at-needed"), { start: scenarioMigration("recorded-at-running") });
  show(fake.client, 60000);
  await expect.element(page.getByRole("heading", { name: /Recording times in past recordings/ })).toBeVisible();
  await expect.element(page.getByText(/37 recordings were published before/)).toBeVisible();
  // Open by default while the decision is pending.
  await expect.element(page.getByText(/everyone the recording is shared with gets a “file changed” entry for each recording/)).toBeVisible();
  await expect.element(page.getByText(/not emailed and not sent as a notification/)).toBeVisible();
  await expect.element(page.getByText("Activity app", { exact: true })).toBeVisible();
  expect(calls).toEqual(["read"]);

  await userEvent.click(page.getByRole("button", { name: "Write the recording time into 37 recordings" }));
  expect(calls).toEqual(["read", "start"]);
  await expect.element(page.getByRole("button", { name: "Pause" })).toBeVisible();
  await expect.element(page.getByText(/up to 2 at a time, 30 seconds apart/)).toBeVisible();
});

it("pauses, says the files in progress finish first, and re-reads until they did", async () => {
  const running = scenarioMigration("recorded-at-running");
  const pausing = normalizeRecordedAtMigration({ ...running, state: "pausing", paused: true });
  const fake = fakeClient(running, { pause: pausing });
  show(fake.client);
  // Polls are held until the test answers them. One is in flight when the
  // pause is pressed; what it read is older than the pause and is dropped.
  await expect.poll(() => fake.reads.length).toBe(1);
  await userEvent.click(page.getByRole("button", { name: "Pause" }));
  await expect.element(page.getByText(/The 2 recordings already being written are finished first/)).toBeVisible();
  fake.reads.shift()?.();
  await expect.poll(() => fake.reads.length).toBe(1);
  await expect.element(page.getByText(/The 2 recordings already being written are finished first/)).toBeVisible();
  expect(page.getByRole("button", { name: "Resume" }).query()).toBeNull();

  // The poll after the pause was asked before the files finished; the next
  // one sees them finished.
  fake.set(scenarioMigration("recorded-at-paused"));
  fake.reads.shift()?.();
  await expect.poll(() => fake.reads.length).toBe(1);
  fake.reads.shift()?.();
  await expect.element(page.getByRole("button", { name: "Resume" })).toBeVisible();
  await expect.element(page.getByText(/Paused with 21 recordings left and 16 done/)).toBeVisible();
});

it("names the blocked recordings and offers a retry, without claiming facts it could not read", async () => {
  const fake = fakeClient(scenarioMigration("recorded-at-blocked"));
  show(fake.client, 60000);
  await expect.element(page.getByText(/Weekly sync/)).toBeVisible();
  await expect.element(page.getByText(/recording is missing; restore it and retry/)).toBeVisible();
  await expect.element(page.getByRole("button", { name: "Retry 2 blocked recordings" })).toBeVisible();
  await userEvent.click(page.getByText("What this does in Nextcloud"));
  await expect.element(page.getByText(/could not read Nextcloud's capabilities/).first()).toBeVisible();
  expect(page.getByText("Activity app", { exact: true }).query()).toBeNull();
});

it("shows nothing where nothing needs migrating", async () => {
  const fake = fakeClient(scenarioMigration("healthy"));
  show(fake.client, 60000);
  await tick();
  await expect.poll(() => calls).toEqual(["read"]);
  await tick();
  expect(host.querySelector("section")).toBeNull();
});

it("shows the first count while it runs and re-reads until it finished", async () => {
  const fake = heldClient();
  const clock = manualTimers();
  app = mount(RecordedAtMigration, { target: host, props: { operatorClient: fake.client, timers: clock.timers } });
  await expect.poll(() => fake.held.length).toBe(1);
  fake.take("read").resolve(normalizeRecordedAtMigration({ applicable: true, state: "checking", checked: false }));
  await expect.element(page.getByText(/checking which past recordings/)).toBeVisible();
  await expect.poll(() => clock.pending.size).toBe(1);

  // Found some, still counting: the start waits for the count.
  clock.fire();
  await expect.poll(() => fake.held.length).toBe(1);
  fake.take("read").resolve({ ...scenarioMigration("recorded-at-needed"), needed: 12, checked: false });
  await expect.element(page.getByText(/still counting/)).toBeVisible();
  await expect.element(page.getByRole("button", { name: "Write the recording time into 12 recordings" })).toBeDisabled();
  await expect.poll(() => clock.pending.size).toBe(1);

  clock.fire();
  await expect.poll(() => fake.held.length).toBe(1);
  fake.take("read").resolve(scenarioMigration("recorded-at-needed"));
  await expect.element(page.getByRole("button", { name: "Write the recording time into 37 recordings" })).toBeEnabled();
  expect(page.getByText(/still counting/).query()).toBeNull();
  // Counted, not started: nothing more to re-read.
  expect(clock.pending.size).toBe(0);
});

it("drops the failure of a re-read that started before an action", async () => {
  const fake = heldClient();
  const clock = manualTimers();
  const running = scenarioMigration("recorded-at-running");
  app = mount(RecordedAtMigration, { target: host, props: { operatorClient: fake.client, timers: clock.timers } });
  await expect.poll(() => fake.held.length).toBe(1);
  fake.take("read").resolve(running);
  await expect.element(page.getByRole("button", { name: "Pause" })).toBeVisible();
  // A re-read is in flight when Pause is pressed, and fails after it.
  await expect.poll(() => clock.pending.size).toBe(1);
  clock.fire();
  await expect.poll(() => fake.held.length).toBe(1);
  const stale = fake.take("read");
  await userEvent.click(page.getByRole("button", { name: "Pause" }));
  await expect.poll(() => calls.includes("pause")).toBe(true);
  expect(calls.filter(c => c === "read").length).toBe(2);
  const pausing = normalizeRecordedAtMigration({ ...running, state: "pausing", paused: true });
  fake.take("pause").resolve(pausing);
  await expect.element(page.getByText(/The 2 recordings already being written are finished first/)).toBeVisible();

  await expect.poll(() => clock.pending.size).toBe(1);
  stale.reject(new Error("operator restarted"));
  // The poll after the action is the sentinel: once it is in flight, the
  // stale failure has been handled — and nothing has cleared an error since.
  clock.fire();
  await expect.poll(() => fake.held.length).toBe(1);
  await tick();
  expect(page.getByRole("alert").query()).toBeNull();
  fake.take("read").resolve(pausing);
  await expect.poll(() => clock.pending.size).toBe(1);
  await expect.element(page.getByText(/The 2 recordings already being written are finished first/)).toBeVisible();
});

it("clears a pending re-read when an action starts", async () => {
  const fake = heldClient();
  const clock = manualTimers();
  app = mount(RecordedAtMigration, { target: host, props: { operatorClient: fake.client, timers: clock.timers } });
  await expect.poll(() => fake.held.length).toBe(1);
  fake.take("read").resolve(scenarioMigration("recorded-at-running"));
  await expect.poll(() => clock.pending.size).toBe(1);
  await userEvent.click(page.getByRole("button", { name: "Pause" }));
  await expect.poll(() => calls.includes("pause")).toBe(true);
  expect(clock.pending.size).toBe(0);
  fake.take("pause").resolve(normalizeRecordedAtMigration({ ...scenarioMigration("recorded-at-running"), state: "pausing", paused: true }));
  await expect.poll(() => clock.pending.size).toBe(1);
  expect(calls).toEqual(["read", "pause"]);
});
