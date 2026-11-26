import { afterEach, expect, it, vi } from "vitest";
import { cancelImport, stageImportUpload, startImports } from "./client";

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
 const path = await stageImportUpload(new File(["123"],"test.json"),undefined,{share:true,audience:[{type:"user",id:"bob",label:"Bob Lee"}]});
 expect(mock.events).toEqual(["prepare","mkdir","put","staged"]);
 expect(mock.body().files[0]).toMatchObject({stagedPath:path,expectedBytes:3,sourceName:"test.json",share:true,audience:[{type:"user",id:"bob",label:"Bob Lee"}]});
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

// #510: a refusal the operator coded reads in the reader's language; any
// other keeps the operator's own sentence.
const refuse = (body: object, status = 409) => vi.fn(async () => Response.json(body, { status }));

it("words a refusal the operator coded", async () => {
  vi.stubGlobal("fetch", refuse({ error: "the meeting has already been added and shared; it can no longer be canceled", error_code: "import_cancel_too_late" }));
  await expect(cancelImport("a1")).rejects.toThrow("The meeting has already been added and shared, so it can no longer be canceled.");
  vi.stubGlobal("fetch", refuse({ error: "staging is busy; try again", error_code: "import_busy" }, 503));
  await expect(startImports([{ stagedPath: ".CassiniImports/a.opus" }])).rejects.toThrow("Cassini is busy right now. Try again in a moment.");
});

it("keeps the operator's sentence for a refusal it has no words for", async () => {
  vi.stubGlobal("fetch", refuse({ error: "something new", error_code: "import_something_new" }));
  await expect(cancelImport("a1")).rejects.toThrow("something new");
});
