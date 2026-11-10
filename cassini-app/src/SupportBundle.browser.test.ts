import { mount, unmount } from "svelte";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { page } from "vitest/browser";
import SupportBundle from "./SupportBundle.svelte";
import App from "./App.svelte";
import "./app.css";

let component: ReturnType<typeof mount> | undefined;
let host: HTMLDivElement;
beforeEach(() => {
  host = document.createElement("div"); document.body.append(host);
  Object.assign(window, { __CASSINI_CONFIG__: { operatorBasePath: "/operator" } });
});
afterEach(async () => {
  if (component) await unmount(component);
  component = undefined;
  host.remove();
  delete (window as Window & { __CASSINI_CONFIG__?: unknown }).__CASSINI_CONFIG__;
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
});

it("downloads once, reports success, and releases its Blob URL on unmount", async () => {
  let complete!: (response: Response) => void;
  const fetchMock = vi.fn(() => new Promise<Response>(resolve => { complete = resolve; }));
  vi.stubGlobal("fetch", fetchMock);
  const create = vi.spyOn(URL, "createObjectURL").mockReturnValue("blob:diagnostic-test");
  const revoke = vi.spyOn(URL, "revokeObjectURL").mockImplementation(() => {});
  const click = vi.spyOn(HTMLAnchorElement.prototype, "click").mockImplementation(() => {});
  component = mount(SupportBundle, { target: host });
  await page.getByRole("button", { name: "Download support bundle" }).click();
  await expect.element(page.getByRole("button", { name: "Preparing support bundle…" })).toBeDisabled();
  complete(new Response(new Blob(["PK-test"]), { headers: { "Content-Type": "application/zip" } }));
  await expect.element(page.getByRole("status")).toHaveTextContent("Support bundle downloaded. Privacy filtering was off.");
  expect(create).toHaveBeenCalledOnce(); expect(click).toHaveBeenCalledOnce(); expect(fetchMock).toHaveBeenCalledOnce();
  await unmount(component); component = undefined;
  expect(revoke).toHaveBeenCalledWith("blob:diagnostic-test");
});

it("leaves a retryable button after a failed server request", async () => {
  vi.stubGlobal("fetch", vi.fn(async () => new Response(null, { status: 500 })));
  component = mount(SupportBundle, { target: host, props: { administrator: true, surface: "operator" } });
  await page.getByRole("button", { name: "Download support bundle" }).click();
  await expect.element(page.getByRole("alert")).toHaveTextContent("Couldn’t prepare the support bundle.");
  await expect.element(page.getByRole("button", { name: "Download support bundle" })).toBeEnabled();
});

it("cancels an in-flight export when the support action is unmounted", async () => {
  let signal: AbortSignal | undefined;
  vi.stubGlobal("fetch", vi.fn((_url, init) => { signal = init.signal; return new Promise(() => {}); }));
  component = mount(SupportBundle, { target: host });
  await page.getByRole("button", { name: "Download support bundle" }).click();
  expect(signal?.aborted).toBe(false);
  await unmount(component); component = undefined;
  expect(signal?.aborted).toBe(true);
});

it("keeps support reachable from Help for an ordinary user when the catalog fails", async () => {
  vi.stubGlobal("OC", { isUserAdmin: () => false });
  vi.stubGlobal("fetch", vi.fn(async () => new Response(null, { status: 502 })));
  component = mount(App, { target: host });
  await expect.element(page.getByText("Load note", { exact: true })).toBeVisible();
  // No strip above the list any more (D-910): the download lives in Help,
  // whose sheet stays mounted but hidden until it is opened.
  expect(document.querySelector(".cassini-support-bar")).toBeNull();
  await expect.element(page.getByRole("button", { name: "Download support bundle" })).not.toBeInTheDocument();
  await page.getByRole("navigation", { name: "Rooms" }).getByRole("button", { name: "Help" }).click();
  await expect.element(page.getByRole("button", { name: "Download support bundle" })).toBeEnabled();
  // A reader's own bundle holds no logs, but their user ID and error text:
  // the scrub choice stays, the logs warning and the Operator link do not.
  await expect.element(page.getByRole("checkbox", { name: "Scrub sensitive information" })).not.toBeChecked();
  await expect.element(page.getByText(/The bundle includes your Nextcloud user ID/)).toBeVisible();
  expect(document.body.textContent).not.toContain("Logs can include names");
  expect(document.body.textContent).not.toContain("Operator › Help");
});

for (const [administrator, originalCopy] of [[true, /Logs can include names/], [false, /The bundle includes your Nextcloud user ID/]] as const)
it(`offers original diagnostics by default and scrubs only when selected (administrator=${administrator})`, async () => {
  const bodies: {privacy:string}[]=[];
  vi.stubGlobal("fetch",vi.fn(async (_url,init)=>{bodies.push(JSON.parse(init.body));return new Response(null,{status:500});}));
  component=mount(SupportBundle,{target:host,props:{administrator,surface:administrator?"operator":"browse"}});
  const choice=page.getByRole("checkbox",{name:"Scrub sensitive information"});
  await expect.element(choice).not.toBeChecked();
  await expect.element(page.getByText(originalCopy)).toBeVisible();
  await page.getByRole("button",{name:"Download support bundle"}).click();
  await expect.element(page.getByRole("alert")).toBeVisible();
  expect(bodies[0].privacy).toBe("original");
  await choice.click();
  await page.getByRole("button",{name:"Download support bundle"}).click();
  await expect.poll(()=>bodies.length).toBe(2);
  expect(bodies[1].privacy).toBe("scrubbed");
});

it("points an administrator at Operator › Help from Browse › Help", async () => {
  const openOperatorHelp = vi.fn();
  component = mount(SupportBundle, { target: host, props: { openOperatorHelp } });
  await page.getByRole("button", { name: "Open Operator › Help" }).click();
  expect(openOperatorHelp).toHaveBeenCalledOnce();
});

it("keeps a download running when the Help sheet is closed", async () => {
  vi.stubGlobal("OC", { isUserAdmin: () => false });
  let complete!: (response: Response) => void;
  let signal: AbortSignal | undefined;
  vi.stubGlobal("fetch", vi.fn((url: string, init?: RequestInit) => {
    if (String(url).includes("support/bundle")) {
      signal = init?.signal ?? undefined;
      return new Promise<Response>(resolve => { complete = resolve; });
    }
    return Promise.resolve(new Response(null, { status: 502 }));
  }));
  vi.spyOn(URL, "createObjectURL").mockReturnValue("blob:support-closed");
  vi.spyOn(URL, "revokeObjectURL").mockImplementation(() => {});
  const click = vi.spyOn(HTMLAnchorElement.prototype, "click").mockImplementation(() => {});
  component = mount(App, { target: host });
  await page.getByRole("navigation", { name: "Rooms" }).getByRole("button", { name: "Help" }).click();
  await page.getByRole("button", { name: "Download support bundle" }).click();
  await expect.element(page.getByRole("button", { name: "Preparing support bundle…" })).toBeDisabled();
  await page.getByRole("button", { name: "Close Help" }).first().click();
  expect(signal?.aborted).toBe(false);
  complete(new Response(new Blob(["PK-test"]), { headers: { "Content-Type": "application/zip" } }));
  await expect.poll(() => click.mock.calls.length).toBe(1);
  await page.getByRole("navigation", { name: "Rooms" }).getByRole("button", { name: "Help" }).click();
  await expect.element(page.getByText("Support bundle downloaded. Privacy filtering was off.")).toBeVisible();
});
