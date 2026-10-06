import { mount, unmount } from "svelte";
import { afterEach, expect, it, vi } from "vitest";
import { page } from "vitest/browser";
import App from "../App.svelte";
import { StaticCatalogProvider } from "../viewer/dataProvider";
import fixture from "../../../spec/testdata/transcription-v1.json";
import "../app.css";

let app: ReturnType<typeof mount> | undefined;
let host: HTMLDivElement;
afterEach(async () => {if(app) await unmount(app);host?.remove();vi.unstubAllGlobals();history.replaceState(null,"",location.pathname);});
it("shows a transcription-only badge and notice with transcript but no player", async () => {
 history.replaceState(null,"",location.pathname);
 const provider = new StaticCatalogProvider();
 const meeting = {id:"text",title:"Text meeting",dateLabel:"2026-10-06",meetingPath:new URL("/meetings/text.json",location.href).href};
 provider.loadCatalog=async()=>({version:"cassini.viewer.catalog.v1",meetings:[meeting]});
 const fetcher=vi.fn(async()=>new Response(JSON.stringify(fixture),{headers:{"Content-Type":"application/json"}}));
 vi.stubGlobal("fetch",fetcher);
 host=document.createElement("div");host.style.height="100vh";document.body.append(host);
 app=mount(App,{target:host,props:{dataProvider:provider}});
 await expect.element(page.getByText("Transcription only",{exact:true})).toBeVisible();
 await expect.element(page.getByText("Transcription-only artefact — audio was not stored. Playback is unavailable.",{exact:true})).toBeVisible();
 await page.getByRole("button",{name:"Export",exact:true}).click();
 await expect.element(page.getByRole("menuitem",{name:"Download meeting file",exact:true})).toBeEnabled();
 expect(host.querySelector("audio")).toBeNull();
 expect(host.textContent).toContain(fixture.source.speakers[0].label);
 expect(fetcher.mock.calls.every(([url])=>url===meeting.meetingPath)).toBe(true);
});
