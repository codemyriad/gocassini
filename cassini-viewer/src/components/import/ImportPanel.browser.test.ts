import { mount, unmount } from "svelte";
import { afterEach, expect, it, vi } from "vitest";
import { page } from "vitest/browser";
import ImportPanel from "./ImportPanel.svelte";
import { StaticCatalogProvider } from "../../viewer/dataProvider";
import type { DataProvider } from "../../viewer/dataProvider";
import type { ImportJobRecord } from "../../viewer/imports";
import type { ImportStatus } from "./importStatus";
import fixture from "../../../../spec/testdata/transcription-v1.json";

let app: ReturnType<typeof mount> | undefined;
let host: HTMLDivElement;
afterEach(async () => { if (app) await unmount(app); app = undefined; host?.remove(); localStorage.clear(); vi.restoreAllMocks(); });
function job(id: string, state: ImportJobRecord["state"]): ImportJobRecord {
 return { id, batchId: "b", createdBy: "alice", sourceName: `${id}.json`, stagedPath: `.CassiniImports/${id}.json`, state, stagingState: state === "upload_failed" ? "pending" : "present", sharing: { share: false }, createdAt: "", updatedAt: "", error: state === "upload_failed" ? "Upload did not complete. Add the file again to import it." : undefined };
}
function open(provider: DataProvider, opts: { open?: boolean; onStatus?: (status: ImportStatus) => void } = {}) {
 host = document.createElement("div"); document.body.append(host);
 app = mount(ImportPanel, {
  target: host,
  props: { dataProvider: provider, open: opts.open ?? true },
  events: { status: (event: CustomEvent<ImportStatus>) => opts.onStatus?.(event.detail) },
 });
}
it("offers Retry only where the upload is still there, and removes attempts durably", async () => {
 let jobs = [job("partial", "upload_failed"), job("staged", "staged"), job("success", "done"), { ...job("swept", "failed"), stagingState: "removed" }];
 const provider: DataProvider = Object.assign(new StaticCatalogProvider(), {
  listImports: async () => jobs,
  retryImport: vi.fn(async () => {}),
  removeImport: vi.fn(async (id: string) => { jobs = jobs.filter((j) => j.id !== id); }),
 });
 open(provider);
 await expect.element(page.getByText("partial.json", { exact: true })).toBeVisible();
 const row = (name: string) => Array.from(host.querySelectorAll(".import-item")).find((r) => r.textContent?.includes(name))!;
 expect(row("partial.json").textContent).not.toContain("Retry");
 expect(row("swept.json").textContent).not.toContain("Retry");
 expect(row("staged.json").textContent).toContain("Retry");
 await page.getByRole("button", { name: "Remove" }).first().click();
 await expect.element(page.getByText("partial.json", { exact: true })).not.toBeInTheDocument();
 await unmount(app!); app = undefined; host.remove(); open(provider);
 await expect.element(page.getByText("success.json", { exact: true })).toBeVisible();
 await expect.element(page.getByText("partial.json", { exact: true })).not.toBeInTheDocument();
});
it("keeps staged imports recoverable when job submission fails and a new selection creates new uploads", async () => {
 let jobs: ImportJobRecord[] = [];
 let uploads = 0;
 const provider: DataProvider = Object.assign(new StaticCatalogProvider(), {
  listImports: async () => jobs,
  stageImportUpload: vi.fn(async () => { const j = job(`attempt-${++uploads}`, "staged"); jobs = [...jobs, j]; return j.stagedPath; }),
  startImports: vi.fn(async () => { throw new Error("Could not start import"); }),
  retryImport: vi.fn(async () => {}),
 });
 open(provider);
 async function addFile() {
  const transfer = new DataTransfer();
  transfer.items.add(new File([JSON.stringify(fixture)], "meeting.json", { type: "application/json" }));
  const input = host.querySelector('input[type="file"]') as HTMLInputElement;
  input.files = transfer.files; input.dispatchEvent(new Event("change", { bubbles: true }));
  await page.getByRole("button", { name: "Import 1 meeting", exact: true }).click();
 }
 await addFile();
 await expect.element(page.getByText("attempt-1.json", { exact: true })).toBeVisible();
 await expect.element(page.getByText("Could not start import", { exact: true })).toBeVisible();
 await expect.element(page.getByRole("button", { name: "Retry", exact: true })).toBeVisible();
 await unmount(app!); app = undefined; host.remove(); open(provider);
 await expect.element(page.getByText("attempt-1.json", { exact: true })).toBeVisible();
 await addFile();
 await expect.element(page.getByText("attempt-2.json", { exact: true })).toBeVisible();
 expect(uploads).toBe(2);
});

it.each(["staged", "failed", "canceled"] as const)("polls a retried %s import through completion without reloading", async (initialState) => {
 let current = job("retry", initialState);
 const provider: DataProvider = Object.assign(new StaticCatalogProvider(), {
  listImports: vi.fn(async () => [{ ...current }]),
  retryImport: vi.fn(async () => { current = { ...current, state: "queued" }; }),
 });
 open(provider);
 await page.getByRole("button", { name: "Retry", exact: true }).click();
 await expect.element(page.getByText("Adding…", { exact: true })).toBeVisible();
 // The server completes after the immediate post-Retry refresh. Only polling
 // can discover this transition; do not remount or navigate the workbench.
 current = { ...current, state: "done" };
 await expect.poll(() => host.querySelector(".import-item")?.textContent ?? "", { timeout: 6000 }).toContain("Imported");
 expect(host.querySelector(".import-item progress")).toBeNull();
});

it.each(["failed", "canceled"] as const)("explains recovery for %s CLI imports without offering web Retry", async (state) => {
 const cli = { ...job("cli", state), stagedPath: "local:/tmp/source.json", origin: "cli" as const, error: "CLI import failed" };
 const provider: DataProvider = Object.assign(new StaticCatalogProvider(), {
  listImports: async () => [cli, job("browser", "failed")],
  retryImport: vi.fn(async () => {}),
 });
 open(provider);
 await expect.element(page.getByText("cli.json", { exact: true })).toBeVisible();
 const cliRow = Array.from(host.querySelectorAll(".import-item")).find((row) => row.textContent?.includes("cli.json"))!;
 // A plain line in the row; the operator's text only behind Details.
 expect(cliRow.textContent).not.toContain("CLI import failed");
 expect(cliRow.textContent).toContain(state === "failed" ? "Import on the server didn't finish" : "Canceled");
 expect(cliRow.textContent).toContain("Dismiss");
 expect(cliRow.textContent).not.toContain("Retry");
 expect(cliRow.querySelector('[aria-label^="Cancel"]')).toBeNull();
 await expect.element(page.getByRole("button", { name: "Retry", exact: true })).toBeVisible();
 // Clear finished takes the row nothing can be done about, and keeps the one Retry can fix.
 await page.getByRole("button", { name: "Clear finished" }).click();
 await expect.element(page.getByText("cli.json", { exact: true })).not.toBeInTheDocument();
 await expect.element(page.getByText("browser.json", { exact: true })).toBeVisible();
});

it("keeps the operator's error behind Details", async () => {
 const provider: DataProvider = Object.assign(new StaticCatalogProvider(), {
  listImports: async () => [{ ...job("broken", "failed"), error: "mkdir /seed/.import-x: read-only file system" }],
  retryImport: vi.fn(async () => {}),
 });
 open(provider);
 await expect.element(page.getByText("Couldn't import this file", { exact: true })).toBeVisible();
 await expect.element(page.getByText(/read-only file system/)).not.toBeInTheDocument();
 await page.getByRole("button", { name: "Details" }).click();
 await expect.element(page.getByText(/read-only file system/)).toBeVisible();
});

it("imports a 51-file selection without hitting the server batch limit", async () => {
 let jobs: ImportJobRecord[] = [];
 let uploads = 0;
 const provider: DataProvider = Object.assign(new StaticCatalogProvider(), {
  listImports: async () => [...jobs],
  stageImportUpload: vi.fn(async () => `.CassiniImports/batch-${++uploads}.json`),
  startImports: vi.fn(async (files: Array<{ stagedPath: string }>) => {
   if (files.length > 50) throw new Error("an import names between 1 and 50 staged files");
   const created = files.map((file) => ({ ...job(file.stagedPath.split("/").at(-1)!, "done"), stagedPath: file.stagedPath }));
   jobs.push(...created);
   return created;
  }),
 });
 open(provider);
 const transfer = new DataTransfer();
 for (let i = 0; i < 51; i++) transfer.items.add(new File([JSON.stringify(fixture)], `meeting-${i}.json`, {type:"application/json"}));
 const input = host.querySelector('input[type="file"]') as HTMLInputElement;
 input.files = transfer.files; input.dispatchEvent(new Event("change",{bubbles:true}));
 await page.getByRole("button",{name:"Import 51 meetings",exact:true}).click();
 await expect.poll(() => host.querySelectorAll(".import-item .text-success").length).toBe(51);
 expect(uploads).toBe(51);
 expect(host.querySelector('[role="alert"]')).toBeNull();
});

it("tells every settled row how it is shared, with the names on hover", async () => {
 const shared = { ...job("shared", "canceled"), sharing: { share: true, audience: [{ type: "user", id: "bob", label: "Bob Lee" }, { type: "group", id: "design", label: "Design" }] } };
 const provider: DataProvider = Object.assign(new StaticCatalogProvider(), {
  listImports: async () => [shared, job("private", "done"), job("live", "validating")],
  retryImport: vi.fn(async () => {}),
 });
 open(provider);
 await expect.element(page.getByText("Shared with 3 recipients", { exact: true })).toBeVisible();
 expect((host.querySelector(".import-sharing") as HTMLElement).title).toBe("you, Bob Lee, Design (group)");
 await expect.element(page.getByText("Only you", { exact: true })).toBeVisible();
 const live = Array.from(host.querySelectorAll(".import-item")).find((row) => row.textContent?.includes("live.json"))!;
 expect(live.querySelector(".import-sharing")).toBeNull();
 expect(live.querySelector("progress")).not.toBeNull();
});

it("tells the Import button about failures until the sheet is opened, and about meetings that just landed", async () => {
 let current = [job("broken", "failed")];
 const statuses: ImportStatus[] = [];
 const provider: DataProvider = Object.assign(new StaticCatalogProvider(), { listImports: vi.fn(async () => current.map((j) => ({ ...j }))) });
 open(provider, { open: false, onStatus: (status) => statuses.push(status) });
 await expect.poll(() => statuses.at(-1)).toEqual({ kind: "failed", count: 1 });
 // Opening the sheet counts the failure as seen, and that sticks.
 await unmount(app!); app = undefined; host.remove();
 open(provider, { open: true, onStatus: (status) => statuses.push(status) });
 await expect.poll(() => statuses.at(-1)).toEqual({ kind: "idle", count: 0 });
 await unmount(app!); app = undefined; host.remove();
 current = [job("broken", "failed"), job("fresh", "validating")];
 open(provider, { open: false, onStatus: (status) => statuses.push(status) });
 await expect.poll(() => statuses.at(-1)).toEqual({ kind: "running", count: 1 });
 current = [job("broken", "failed"), job("fresh", "done")];
 await expect.poll(() => statuses.at(-1), { timeout: 6000 }).toEqual({ kind: "done", count: 1 });
});

it("groups the people a meeting is shared with, and an empty list means only me", async () => {
 const provider: DataProvider = Object.assign(new StaticCatalogProvider(), {
  listImports: async () => [],
  stageImportUpload: vi.fn(async () => ".CassiniImports/x.json"),
  startImports: vi.fn(async () => []),
  searchImportSharees: vi.fn(async () => [{ type: "group" as const, id: "sales", label: "Sales" }]),
 });
 open(provider);
 const transfer = new DataTransfer();
 transfer.items.add(new File([JSON.stringify(fixture)], "meeting.json", { type: "application/json" }));
 const input = host.querySelector('input[type="file"]') as HTMLInputElement;
 input.files = transfer.files; input.dispatchEvent(new Event("change", { bubbles: true }));
 await page.getByRole("button", { name: "Only me" }).click();
 await page.getByRole("combobox", { name: "Add a person, group or team" }).fill("sal");
 await page.getByRole("option", { name: /Sales/ }).click();
 await expect.element(page.getByText("Groups", { exact: true })).toBeVisible();
 await expect.element(page.getByRole("button", { name: "Shared" })).toBeVisible();
 await page.getByRole("button", { name: "Remove Sales" }).click();
 await expect.element(page.getByText("Groups", { exact: true })).not.toBeInTheDocument();
 await expect.element(page.getByRole("button", { name: "Only me" })).toBeVisible();
});
