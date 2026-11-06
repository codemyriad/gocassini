import { mount, unmount } from "svelte";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { page } from "vitest/browser";
import GenerateCard from "./GenerateCard.svelte";
import "./app.css";

const MEETING = { id: "m1", title: "Weekly", dateLabel: "2 Nov", wordCount: 120 };

let app: ReturnType<typeof mount> | undefined;
let host: HTMLDivElement;
let defaultId = "";

beforeEach(() => {
  defaultId = "";
  host = document.createElement("div");
  document.body.append(host);
  vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL) => {
    const path = new URL(String(input), location.href).pathname;
    if (path === "/operator/insights/workflows") {
      return Response.json({
        default: defaultId,
        templates: [
          { id: "summarise", name: "Meeting summary", description: "", takesQuestion: false, origin: "Built in" },
          { id: "resumen", name: "Resumen", description: "", takesQuestion: false, origin: "Custom" },
        ],
      });
    }
    if (path === "/operator/ai/providers") return Response.json([]);
    throw new Error(`unexpected GET ${path}`);
  }));
  Object.assign(window, { __CASSINI_CONFIG__: { operatorBasePath: "/operator" } });
});

afterEach(async () => {
  if (app) await unmount(app);
  app = undefined;
  host.remove();
  vi.unstubAllGlobals();
  delete (window as Window & { __CASSINI_CONFIG__?: unknown }).__CASSINI_CONFIG__;
});

describe("the Generate card's template picker (D-819)", () => {
  it("opens on the deployment's default template", async () => {
    defaultId = "resumen";
    app = mount(GenerateCard, { target: host, props: { entries: [MEETING] } });
    await expect.element(page.getByLabelText("Insight template")).toHaveValue("resumen");
  });

  it("opens on the first template when the operator names none", async () => {
    app = mount(GenerateCard, { target: host, props: { entries: [MEETING] } });
    await expect.element(page.getByLabelText("Insight template")).toHaveValue("summarise");
  });
});
