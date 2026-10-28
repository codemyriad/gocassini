import { afterEach, expect, it, vi } from "vitest";
import { AppDataProvider } from "./appDataProvider";
import { RoomNameError } from "cassini-viewer/dataProvider";

afterEach(() => vi.unstubAllGlobals());

it("uses the user route and sends an explicit revision and reset", async () => {
  const base="https://nc.test/index.php/apps/app_api/proxy/cassini/";
  vi.stubGlobal("window",{__CASSINI_VIEWER_BASE__:base,location:{href:base+"control-panel/"}});
  const state={roomId:"opaque",displayName:"admin & Alice",defaultName:"admin & Alice",customName:null,revision:4};
  const fetchMock=vi.fn(async()=>new Response(JSON.stringify(state)));
  vi.stubGlobal("fetch",fetchMock);
  const provider=new AppDataProvider();
  await expect(provider.saveRoomName("opaque",null,3)).resolves.toEqual(state);
  const [url,options]=fetchMock.mock.calls[0] as unknown as [URL,RequestInit];
  expect(String(url)).toBe(base+"rooms/opaque/name");
  expect(options).toMatchObject({method:"POST",cache:"no-store",body:JSON.stringify({customName:null,expectRevision:3})});
});

it("preserves conflict state for the editor and treats a lost response as uncertain",async()=>{
  const base="https://nc.test/";vi.stubGlobal("window",{__CASSINI_VIEWER_BASE__:base,location:{href:base}});
  const current={roomId:"one",displayName:"Theirs",defaultName:"Default",customName:"Theirs",revision:2};
  vi.stubGlobal("fetch",vi.fn(async()=>new Response(JSON.stringify({error:"Changed",current}),{status:409})));
  await expect(new AppDataProvider().saveRoomName("one","Mine",1)).rejects.toMatchObject({status:409,current});
  vi.stubGlobal("fetch",vi.fn(async()=>{throw new Error("offline")}));
  await expect(new AppDataProvider().saveRoomName("one","Mine",1)).rejects.toBeInstanceOf(RoomNameError);
});
