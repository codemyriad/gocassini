import { afterEach, expect, it, vi } from "vitest";
import { stageImportUpload } from "./client";

afterEach(() => vi.unstubAllGlobals());
function mockUpload(putStatus = 201, prepareStatus = 202) {
 const events: string[] = [];
 let prepareBody: Record<string, any>;
 vi.stubGlobal("OC", { currentUser: "alice", requestToken: "test", getRootPath: () => "" });
 vi.stubGlobal("fetch", vi.fn(async (_url: string, init?: RequestInit) => {
  if (init?.method === "MKCOL") { events.push("mkdir"); return new Response(null,{status:201}); }
  const body = JSON.parse(String(init?.body || "{}"));
  if (body.prepare) { events.push("prepare"); prepareBody = body; return Response.json({ imports: [{id:"job-1"}] },{status:prepareStatus}); }
  const action = new URL(_url,location.href).pathname.split("/").at(-1)!;
  events.push(action);
  return Response.json({accepted:true});
 }));
 class XHR extends EventTarget {
  upload = new EventTarget();
  status = putStatus;
  onload?: () => void;
  withCredentials = false;
  open() {}
  setRequestHeader() {}
  send() { events.push("put"); this.onload?.(); this.dispatchEvent(new Event("loadend")); }
 }
 vi.stubGlobal("XMLHttpRequest", XHR);
 return { events, body: () => prepareBody };
}
it("registers owner metadata and sharing before sending bytes, then confirms staging",async()=>{
 const mock = mockUpload();
 const path = await stageImportUpload(new File(["123"],"test.json"),undefined,{share:true,audience:[{type:"user",id:"bob"}]});
 expect(mock.events).toEqual(["prepare","mkdir","put","staged"]);
 expect(mock.body().files[0]).toMatchObject({stagedPath:path,expectedBytes:3,sourceName:"test.json",share:true,audience:[{type:"user",id:"bob"}]});
});
it("does not upload when DB preparation fails",async()=>{
 const mock=mockUpload(201,503);
 await expect(stageImportUpload(new File(["123"],"test.json"))).rejects.toThrow();
 expect(mock.events).toEqual(["prepare"]);
});
it("reports a failed PUT to its durable record",async()=>{
 const mock=mockUpload(507);
 await expect(stageImportUpload(new File(["123"],"test.json"))).rejects.toMatchObject({importJobId:"job-1",message:"Not enough space in your Nextcloud Files for this upload."});
 expect(mock.events).toEqual(["prepare","mkdir","put","upload-failed"]);
});
