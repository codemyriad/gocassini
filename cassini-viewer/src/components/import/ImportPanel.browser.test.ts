import { mount, unmount } from "svelte";
import { afterEach, expect, it, vi } from "vitest";
import { page } from "vitest/browser";
import ImportPanel from "./ImportPanel.svelte";
import { StaticCatalogProvider } from "../../viewer/dataProvider";
import type { DataProvider } from "../../viewer/dataProvider";
import type { ImportJobRecord } from "../../viewer/imports";
import fixture from "../../../../spec/testdata/transcription-v1.json";

let app: ReturnType<typeof mount> | undefined;
let host: HTMLDivElement;
afterEach(async () => { if (app) await unmount(app); app = undefined; host?.remove(); localStorage.clear(); vi.restoreAllMocks(); });
function job(id: string, state: ImportJobRecord["state"]): ImportJobRecord {
 return { id, batchId: "b", createdBy: "alice", sourceName: `${id}.json`, stagedPath: `.CassiniImports/${id}.json`, state, stagingState: state === "upload_failed" ? "pending" : "present", sharing: { share: false }, createdAt: "", updatedAt: "", error: state === "upload_failed" ? "Upload did not complete. Add the file again to import it." : undefined };
}
function open(provider: DataProvider) {
 host = document.createElement("div"); document.body.append(host);
 app = mount(ImportPanel, { target: host, props: { dataProvider: provider, active: true } });
}
it("restores failed uploads without Retry, removes them durably, and keeps cleared attempts listed", async () => {
 let jobs = [job("partial", "upload_failed"), job("staged", "staged"), job("success", "done")];
 const provider: DataProvider = Object.assign(new StaticCatalogProvider(), {
  listImports: async () => jobs,
  retryImport: vi.fn(async () => {}),
  removeImport: vi.fn(async (id: string) => { jobs = jobs.filter((j) => j.id !== id); }),
  // Files go; rows stay — a cleared attempt loses its Retry, not its place.
  clearImportStaging: vi.fn(async () => { jobs = jobs.map((j) => (j.state === "done" ? j : { ...j, stagingState: "removed" })); }),
 });
 open(provider);
 await expect.element(page.getByText("partial.json", { exact: true })).toBeVisible();
 const partial = host.querySelector(".import-item")!;
 expect(partial.textContent).not.toContain("Retry");
 await expect.element(page.getByRole("button", { name: "Retry", exact: true })).toBeVisible();
 (partial.querySelector("button") as HTMLButtonElement).click();
 await expect.element(page.getByText("partial.json", { exact: true })).not.toBeInTheDocument();
 await unmount(app!); app = undefined; host.remove(); open(provider);
 await expect.element(page.getByText("success.json", { exact: true })).toBeVisible();
 await expect.element(page.getByText("partial.json", { exact: true })).not.toBeInTheDocument();
 vi.spyOn(window,"confirm").mockReturnValue(true);
 await page.getByRole("button", { name: "Clear staged files" }).click();
 await expect.element(page.getByRole("button", { name: "Retry", exact: true })).not.toBeInTheDocument();
 await expect.element(page.getByText("staged.json", { exact: true })).toBeVisible();
 await expect.element(page.getByText("success.json", { exact: true })).toBeVisible();
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
 await expect.element(page.getByText("queued", { exact: true })).toBeVisible();
 // The server completes after the immediate post-Retry refresh. Only polling
 // can discover this transition; do not remount or navigate the workbench.
 current = { ...current, state: "done" };
 await expect.element(page.getByText("imported", { exact: true }), { timeout: 6000 }).toBeVisible();
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
 expect(cliRow.textContent).toContain("Run import-meeting again with the source file");
 expect(cliRow.textContent).not.toContain("Retry");
 expect(cliRow.textContent).not.toContain("Cancel");
 await expect.element(page.getByRole("button", { name: "Retry", exact: true })).toBeVisible();
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
 await expect.poll(() => host.querySelectorAll(".import-item .badge-success").length).toBe(51);
 expect(uploads).toBe(51);
 expect(host.querySelector('[role="alert"]')).toBeNull();
});
