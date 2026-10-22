import { mount, unmount } from "svelte";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { page, userEvent } from "vitest/browser";
import App from "../App.svelte";
import "../app.css";
import { annotationFixture } from "./marking/annotations.fixture";
import { RoomNameError, type RoomNameState } from "../viewer/roomNames";

let fixture: ReturnType<typeof annotationFixture>;
let app: ReturnType<typeof mount>;
let host: HTMLDivElement;
let originalUrl: string;
let state: RoomNameState;
let save: ReturnType<typeof vi.fn>;

beforeEach(() => {
  originalUrl = location.href; history.replaceState(null, "", location.pathname + location.search);
  host = document.createElement("div"); host.style.height = "100vh"; document.body.append(host);
  fixture = annotationFixture();
  state = {roomId:"one",displayName:"admin & Alice",defaultName:"admin & Alice",customName:null,revision:1};
  fixture.provider.loadCatalog = async () => ({version:"cassini.viewer.catalog.v1",meetings:[
    {id:"m1", title:"First meeting",dateLabel:"2026-10-21",roomId:"one",roomName:"Alice",roomDisplayName:state.displayName},
    {id:"m2", title:"Other meeting",dateLabel:"2026-10-20",roomId:"two",roomName:"Other"},
  ]});
  fixture.provider.loadRoomName = vi.fn(async () => structuredClone(state));
  save = vi.fn(async (_id: string, customName: string | null, revision: number) => {
    expect(revision).toBe(state.revision);
    state = {...state, customName, displayName:customName ?? state.defaultName,revision:revision+1};
    return structuredClone(state);
  });
  fixture.provider.saveRoomName = save;
});

afterEach(async () => {
  if(app) await unmount(app); fixture.dispose(); delete window.__CASSINI_VIEWER_BASE__; host.remove(); history.replaceState(null,"",originalUrl);
});

function start() { app=mount(App,{target:host,props:{dataProvider:fixture.provider}}); }
async function edit(name = "admin & Alice") {
  await page.getByRole("button",{name:`Rename room ${name}`,exact:true}).click();
  await expect.element(page.getByRole("textbox",{name:"Room name",exact:true})).toBeVisible();
}

it("renames and resets without selecting a different room",async()=>{
  start();
  await page.getByRole("button",{name:"Other 1",exact:true}).click();
  await edit();
  await page.getByRole("textbox",{name:"Room name",exact:true}).fill("Planning");
  await userEvent.keyboard("{Enter}");
  await expect.element(page.getByRole("dialog",{name:"Rename room"})).not.toBeInTheDocument();
  expect(save).toHaveBeenCalledWith("one","Planning",1);
  await expect.element(page.getByRole("button",{name:"Other 1",exact:true})).toHaveAttribute("aria-pressed","true");
  await edit("Planning");
  await page.getByRole("button",{name:"Use default",exact:true}).click();
  await expect.element(page.getByRole("button",{name:"Rename room admin & Alice",exact:true})).toBeInTheDocument();
  expect(save).toHaveBeenLastCalledWith("one",null,2);
});

it("keeps the draft on conflict and lets an explicit second save use the new revision",async()=>{
  save.mockRejectedValueOnce(new RoomNameError("Another person renamed this room.",409,{...state,displayName:"Their name",customName:"Their name",revision:2}));
  start();await edit();
  await page.getByRole("textbox",{name:"Room name",exact:true}).fill("My name");
  await page.getByRole("button",{name:"Save",exact:true}).click();
  await expect.element(page.getByRole("alert").filter({hasText:"Their name"})).toBeVisible();
  await expect.element(page.getByRole("textbox",{name:"Room name",exact:true})).toHaveValue("My name");
  state={...state,revision:2};
  await page.getByRole("button",{name:"Save",exact:true}).click();
  await expect.element(page.getByRole("button",{name:"Rename room My name",exact:true})).toBeInTheDocument();
});

it("cancels with Escape and restores focus without writing",async()=>{
  start();await edit();await userEvent.keyboard("{Escape}");
  await expect.element(page.getByRole("dialog",{name:"Rename room"})).not.toBeInTheDocument();
  expect(save).not.toHaveBeenCalled();
  await expect.element(page.getByRole("button",{name:"Rename room admin & Alice",exact:true})).toHaveFocus();
});

it("offers no edit control for a read-only provider",async()=>{
  delete fixture.provider.saveRoomName;start();
  await expect.element(page.getByRole("button",{name:"admin & Alice 1",exact:true})).toBeVisible();
  await expect.element(page.getByRole("button",{name:"Rename room admin & Alice",exact:true})).not.toBeInTheDocument();
});

it("ignores a catalog response started before save and preserves the edit draft during refresh",async()=>{
  const load=fixture.provider.loadCatalog.bind(fixture.provider);
  let release: ((value: Awaited<ReturnType<typeof load>>) => void) | undefined;
  const old=await load();
  const listing=vi.fn(load);
  fixture.provider.loadCatalog=listing;
  window.__CASSINI_VIEWER_BASE__ = location.origin + "/";
  app=mount(App,{target:host,props:{dataProvider:fixture.provider}});
  await edit();
  await page.getByRole("textbox",{name:"Room name",exact:true}).fill("Planning");
  listing.mockImplementationOnce(()=>new Promise(resolve=>{release=resolve}));
  window.dispatchEvent(new Event("focus"));
  await expect.poll(()=>!!release).toBe(true);
  await expect.element(page.getByRole("textbox",{name:"Room name",exact:true})).toHaveValue("Planning");
  await page.getByRole("button",{name:"Save",exact:true}).click();
  await expect.element(page.getByRole("button",{name:"Rename room Planning",exact:true})).toBeInTheDocument();
  release!(old);
  await expect.element(page.getByRole("button",{name:"Rename room Planning",exact:true})).toBeInTheDocument();
  await expect.element(page.getByRole("button",{name:"Rename room admin & Alice",exact:true})).not.toBeInTheDocument();
});

it("checks current state before retrying an uncertain write",async()=>{
  save.mockRejectedValueOnce(new RoomNameError("Lost response",null));start();await edit();
  await page.getByRole("textbox",{name:"Room name",exact:true}).fill("Planning");
  await page.getByRole("button",{name:"Save",exact:true}).click();
  await expect.element(page.getByRole("alert").filter({hasText:"Lost response"})).toBeVisible();
  state={...state,displayName:"Planning",customName:"Planning",revision:2};
  await page.getByRole("button",{name:"Save",exact:true}).click();
  await expect.element(page.getByRole("alert").filter({hasText:"Current saved name: Planning"})).toBeVisible();
  expect(save).toHaveBeenCalledTimes(1);
  await page.getByRole("button",{name:"Save",exact:true}).click();
  await expect.element(page.getByRole("dialog",{name:"Rename room"})).not.toBeInTheDocument();
  expect(save).toHaveBeenLastCalledWith("one","Planning",2);
});

it.each(["light", "dark"])("inherits the embedded %s theme and dismisses without saving", async (theme) => {
  host.dataset.ncTheme = theme;
  const background = theme === "light" ? "#f5f5f5" : "#181818";
  host.style.setProperty("--color-background-hover", background);
  host.style.setProperty("--color-background-dark", background);
  host.style.setProperty("--color-main-background", theme === "light" ? "#ffffff" : "#222222");
  host.style.setProperty("--color-main-text", theme === "light" ? "#222222" : "#eeeeee");
  host.style.setProperty("--color-border-dark", "#888888");
  const shadow = host.attachShadow({ mode: "open" });
  for (const style of document.head.querySelectorAll("style")) shadow.append(style.cloneNode(true));
  app = mount(App, { target: shadow, props: { dataProvider: fixture.provider } });

  // Embedding-page shortcuts can suppress the native dialog's Escape default.
  const hostShortcut = (event: KeyboardEvent) => { if (event.key === "Escape") event.preventDefault(); };
  document.addEventListener("keydown", hostShortcut);
  try {
    await edit();
    const dialog = shadow.querySelector("dialog")!;
    expect(getComputedStyle(dialog).backgroundColor).toBe(theme === "light" ? "rgb(245, 245, 245)" : "rgb(24, 24, 24)");
    expect(getComputedStyle(dialog).color).toBe(theme === "light" ? "rgb(34, 34, 34)" : "rgb(238, 238, 238)");
    await page.getByRole("textbox", { name: "Room name", exact: true }).fill("Unsaved draft");
    await userEvent.keyboard("{Escape}");
    await expect.element(page.getByRole("dialog", { name: "Rename room" })).not.toBeInTheDocument();
    await expect.poll(() => shadow.activeElement?.getAttribute("aria-label")).toBe("Rename room admin & Alice");

    await edit();
    await expect.element(page.getByRole("textbox", { name: "Room name", exact: true })).toHaveValue("admin & Alice");
    // The native backdrop is outside the dialog's border box.
    await page.getByRole("dialog", { name: "Rename room" }).click({ position: { x: -10, y: -10 }, force: true });
    await expect.element(page.getByRole("dialog", { name: "Rename room" })).not.toBeInTheDocument();
    await expect.poll(() => shadow.activeElement?.getAttribute("aria-label")).toBe("Rename room admin & Alice");
    expect(save).not.toHaveBeenCalled();
  } finally {
    document.removeEventListener("keydown", hostShortcut);
  }
});
