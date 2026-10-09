import { mkdtempSync, cpSync, writeFileSync, readFileSync, rmSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { expect, it } from "vitest";
import { main } from "./export-static-meetings.mjs";
it("exports a JSON meeting and ignores ordinary JSON sidecars", () => {
 const root = mkdtempSync(join(tmpdir(), "transcription-export-"));
 try {
  cpSync(new URL("../../spec/testdata/transcription-v1.json", import.meta.url), join(root,"meeting.json"));
  writeFileSync(join(root,"notes.json"), JSON.stringify({notes:"not a meeting"}));
  const output = join(root,"site");
  main(["--source-dir",root,"--output-dir",output]);
  const catalog = JSON.parse(readFileSync(join(output,"catalog.json"),"utf8"));
  expect(catalog.meetings).toHaveLength(1);
  expect(catalog.meetings[0]).toMatchObject({meetingPath:"./meetings/meeting.json",hasAudio:false,meetingFormat:"json"});
  expect(catalog.meetings[0].audioPath).toBeUndefined();
  expect(readFileSync(join(output,"meetings/meeting.json"))).toEqual(readFileSync(join(root,"meeting.json")));
 } finally {rmSync(root,{recursive:true,force:true});}
});
