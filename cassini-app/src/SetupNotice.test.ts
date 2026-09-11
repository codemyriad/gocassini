import { describe, expect, it } from "vitest";
import { render } from "svelte/server";
import SetupNotice from "./SetupNotice.svelte";
import { buildSetupNotice } from "./operator/setupHealth";

function noticeFor(step: string, isAdmin = true) {
  return buildSetupNotice({
    health: null,
    access: { ok: false, state: "unavailable", step,
      detail: `${step}: diagnostic from Nextcloud`, mode: "", modeConfirmed: false,
      prerequisites: [] },
    isAdmin,
    appUrl: "https://nextcloud.example/apps/gocassini",
  })!;
}

describe("setup notice presentation", () => {
  it("keeps the setup action visible and the screenshot diagnosis collapsed", () => {
    const { body } = render(SetupNotice, { props: { notice: noticeFor("storage_mode_undecided") } });
    const visible = body.replace(/<details\b[^>]*>[\s\S]*?<\/details>/g, "");
    expect(visible).toContain("Choose recording access");
    expect(visible).toContain("Finish setting up Cassini");
    expect(visible).not.toContain("storage_mode_undecided");
    expect(visible).not.toContain("text-warning");
    expect(body).not.toMatch(/<details\b[^>]*\bopen\b/);
  });

  it("puts manual recovery commands inside the closed technical disclosure", () => {
    const { body } = render(SetupNotice, { props: { notice: noticeFor("owner_account") } });
    const visible = body.replace(/<details\b[^>]*>[\s\S]*?<\/details>/g, "");
    expect(visible).toContain("Continue setup");
    expect(visible).not.toContain("occ ");
    expect(visible).not.toContain("diagnostic from Nextcloud");
    expect(body).toContain("occ app_api:app:disable gocassini");
    expect(body).not.toMatch(/<details\b[^>]*\bopen\b/);
  });

  it("gives non-admins neither setup controls nor technical details", () => {
    const { body } = render(SetupNotice, { props: { notice: noticeFor("storage_mode_undecided", false) } });
    expect(body).not.toContain("<button");
    expect(body).not.toContain("<details");
    expect(body).not.toContain("diagnostic from Nextcloud");
    expect(body).toContain("administrator");
  });
});
