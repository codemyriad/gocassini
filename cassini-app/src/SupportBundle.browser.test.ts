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
  await expect.element(page.getByRole("status")).toHaveTextContent("Your support bundle downloaded.");
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
  // No strip above the list any more (D-910): the download lives in Help › Support.
  expect(document.body.textContent).not.toContain("Download support bundle");
  await page.getByRole("group", { name: "Help" }).getByRole("button", { name: "Support" }).click();
  await expect.element(page.getByRole("button", { name: "Download support bundle" })).toBeEnabled();
  // A reader's own bundle holds no logs: no scrub choice, no logs warning, no Doctor link.
  await expect.element(page.getByRole("checkbox", { name: "Scrub sensitive information" })).not.toBeInTheDocument();
  expect(document.body.textContent).not.toContain("Original logs may contain");
  expect(document.body.textContent).not.toContain("Operator › Doctor");
});

it("offers original logs by default and scrubs only when selected", async () => {
  const bodies: {privacy:string}[]=[];
  vi.stubGlobal("fetch",vi.fn(async (_url,init)=>{bodies.push(JSON.parse(init.body));return new Response(null,{status:500});}));
  component=mount(SupportBundle,{target:host,props:{administrator:true,surface:"operator"}});
  const choice=page.getByRole("checkbox",{name:"Scrub sensitive information"});
  await expect.element(choice).not.toBeChecked();
  await expect.element(page.getByText(/Original logs may contain/)).toBeVisible();
  await page.getByRole("button",{name:"Download support bundle"}).click();
  await expect.element(page.getByRole("alert")).toBeVisible();
  expect(bodies[0].privacy).toBe("original");
  await choice.click();
  await page.getByRole("button",{name:"Download support bundle"}).click();
  await expect.poll(()=>bodies.length).toBe(2);
  expect(bodies[1].privacy).toBe("scrubbed");
});

it("points an administrator at Doctor from Help › Support", async () => {
  const openDoctor = vi.fn();
  component = mount(SupportBundle, { target: host, props: { openDoctor } });
  await page.getByRole("button", { name: "Operator › Doctor" }).click();
  expect(openDoctor).toHaveBeenCalledOnce();
});
