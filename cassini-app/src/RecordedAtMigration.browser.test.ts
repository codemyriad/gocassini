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

it("explains what rewriting does before offering to start it", async () => {
  const fake = fakeClient(scenarioMigration("recorded-at-needed"), { start: scenarioMigration("recorded-at-running") });
  show(fake.client, 60000);
  await expect.element(page.getByRole("heading", { name: /Recording times in past recordings/ })).toBeVisible();
  await expect.element(page.getByText(/37 recordings were published before/)).toBeVisible();
  // Open by default while the decision is pending.
  await expect.element(page.getByText(/everyone the recording is shared with gets a “file changed by cassini” entry/)).toBeVisible();
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
  await expect.element(page.getByText(/The 2 recordings being written now finish first/)).toBeVisible();
  fake.reads.shift()?.();
  await expect.poll(() => fake.reads.length).toBe(1);
  await expect.element(page.getByText(/The 2 recordings being written now finish first/)).toBeVisible();
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
