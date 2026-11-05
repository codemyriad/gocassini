import { describe, expect, it } from "vitest";
import infoXml from "../../../appinfo/info.xml?raw";
import funding from "../../../.github/FUNDING.yml?raw";
import readme from "../../../README.md?raw";
import footer from "../../../cassini-microsite/src/components/Footer.astro?raw";
import supportPanel from "../SupportPanel.svelte?raw";
import { SUPPORT_URL } from "./support";

// The support link is written in five places that ship separately: the App
// Store entry, GitHub's Sponsor button, the README, the site footer and the
// admin page.
// When the Stripe link is replaced, every copy has to move together.
describe("Support Cassini link", () => {
  it("is a Stripe link", () => {
    expect(SUPPORT_URL).toMatch(/^https:\/\/donate\.stripe\.com\//);
  });

  it("is the same URL in info.xml, FUNDING.yml, the README and the site footer", () => {
    expect(infoXml).toContain(`<donation title="Support Cassini" type="stripe">${SUPPORT_URL}</donation>`);
    expect(funding).toContain(SUPPORT_URL);
    expect(readme).toContain(SUPPORT_URL);
    expect(footer).toContain(SUPPORT_URL);
  });

  it("is read from the shared constant on the admin page", () => {
    expect(supportPanel).toContain("href={SUPPORT_URL}");
    expect(supportPanel).not.toContain("donate.stripe.com");
  });
});
