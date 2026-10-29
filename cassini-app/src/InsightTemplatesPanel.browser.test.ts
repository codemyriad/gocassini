import { mount, unmount } from "svelte";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { page } from "vitest/browser";
import InsightTemplatesPanel from "./InsightTemplatesPanel.svelte";
import { OperatorClient } from "./operator/client";
import "./app.css";

type Row = Record<string, string>;

const builtIn = (id: string, name: string, question: string, instruction: string): Row => ({
  id, name, question, instruction,
  version: "v0", sha256: `${id}-sha`, description: `${name} description`, origin: "Built in",
});
const custom = (id: string, name: string, question: string, instruction: string): Row => ({
  ...builtIn(id, name, question, instruction), version: "v1", origin: "Custom",
});

const BUILT_INS = [
  builtIn("summarise", "Meeting summary", "Summarise what happened.", "You are a meeting-summary editor."),
  builtIn("ask", "Ask your own question", "", "Answer {{QUESTION}} from the transcripts."),
];

let rows: Row[] = [];
let puts: Array<{ id: string; body: Record<string, string> }> = [];
let deleteRefusal = "";
let app: ReturnType<typeof mount> | undefined;
let host: HTMLDivElement;

beforeEach(() => {
  rows = [...BUILT_INS];
  puts = [];
  deleteRefusal = "";
  host = document.createElement("div");
  document.body.append(host);
  vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const path = new URL(String(input), location.href).pathname;
    if (path === "/operator/settings/llm") return Response.json({ providers: [] });
    if (path === "/operator/settings/workflows") return Response.json(rows);
    const id = decodeURIComponent(path.replace("/operator/settings/workflows/", ""));
    if (init?.method === "PUT") {
      const body = JSON.parse(String(init.body)) as Record<string, string>;
      puts.push({ id, body });
      const saved = custom(id, body.name, body.question, body.instruction);
      rows = [...rows.filter((row) => row.id !== id), saved];
      return Response.json(saved);
    }
    if (init?.method === "DELETE") {
      if (deleteRefusal) return Response.json({ error: deleteRefusal }, { status: 409 });
      rows = rows.filter((row) => row.id !== id);
      return Response.json({ id, deleted: true });
    }
    throw new Error(`unexpected ${init?.method ?? "GET"} ${path}`);
  }));
});

afterEach(async () => {
  if (app) await unmount(app);
  app = undefined;
  host.remove();
  vi.unstubAllGlobals();
});

function mountPanel() {
  app = mount(InsightTemplatesPanel, { target: host, props: { operatorClient: new OperatorClient("/operator") } });
}

const builtInToggle = () => page.getByRole("button", { name: /Built in/ });

describe("insight templates panel in the browser", () => {
  it("lists built-ins open when there is nothing of your own, with no edit controls on them", async () => {
    mountPanel();
    await expect.element(page.getByRole("heading", { name: "Your templates" })).toBeVisible();
    await expect.element(page.getByText("None yet.", { exact: false })).toBeVisible();
    await expect.element(builtInToggle()).toHaveAttribute("aria-expanded", "true");
    await expect.element(page.getByText("Shipped with this version and recorded by hash in every document they produce. Duplicate one to change it.")).toBeVisible();
    await expect.element(page.getByRole("heading", { name: "Meeting summary" })).toBeVisible();
    expect(page.getByRole("button", { name: "Duplicate" }).all()).toHaveLength(2);
    expect(page.getByRole("button", { name: "Edit", exact: true }).all()).toHaveLength(0);
    expect(page.getByRole("button", { name: "Delete" }).all()).toHaveLength(0);
  });

  it("collapses built-ins when a custom template exists, and lists it first", async () => {
    rows = [...BUILT_INS, custom("resumen", "Resumen", "Resume la reunión.", "Resume.")];
    mountPanel();
    await expect.element(page.getByRole("heading", { name: "Resumen" })).toBeVisible();
    await expect.element(builtInToggle()).toHaveAttribute("aria-expanded", "false");
    expect(page.getByRole("heading", { name: "Meeting summary" }).all()).toHaveLength(0);
    await builtInToggle().click();
    await expect.element(page.getByRole("heading", { name: "Meeting summary" })).toBeVisible();
  });

  it("duplicates a built-in into a new template under Your templates, deriving the id from the name", async () => {
    mountPanel();
    await page.getByRole("button", { name: "Duplicate" }).first().click();
    const form = page.getByRole("form", { name: "New insight template" });
    await expect.element(form).toBeVisible();
    // Opened above the built-in list, not as an expansion of the source row.
    const formEl = form.element();
    const builtInHeading = builtInToggle().element();
    expect(formEl.compareDocumentPosition(builtInHeading) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
    await expect.element(page.getByLabelText("Name")).toHaveValue("Meeting summary (copy)");
    await expect.element(page.getByLabelText("What it asks")).toHaveValue("Summarise what happened.");
    // No id to type, and no file line until it exists.
    expect(page.getByText("File:", { exact: false }).all()).toHaveLength(0);

    await page.getByRole("button", { name: "Save" }).click();
    await expect.poll(() => puts.length).toBe(1);
    expect(puts[0].id).toBe("meeting-summary-copy");
    expect(puts[0].body).toEqual({
      name: "Meeting summary (copy)",
      question: "Summarise what happened.",
      description: "Meeting summary description",
      instruction: "You are a meeting-summary editor.",
    });
    await expect.element(page.getByRole("heading", { name: "Meeting summary (copy)" })).toBeVisible();
  });

  it("checks the question box against {{QUESTION}} before saving, and writes an empty question when ticked", async () => {
    mountPanel();
    await page.getByRole("button", { name: "New template" }).click();
    await expect.element(page.getByLabelText("Prompt")).toHaveAttribute("placeholder", expect.stringContaining("Markdown"));
    await page.getByLabelText("Name").fill("Summarise");
    await page.getByLabelText("Prompt").fill("Answer {{QUESTION}} briefly.");
    await expect.element(page.getByText("The prompt has {{QUESTION}} in it.", { exact: false })).toBeVisible();

    await page.getByLabelText("What it asks").fill("Something");
    await page.getByRole("checkbox", { name: "Asks the person for a question" }).click();
    await expect.element(page.getByLabelText("What it asks")).toBeDisabled();
    await expect.element(page.getByLabelText("What it asks")).toHaveValue("");
    expect(page.getByText("The prompt has {{QUESTION}} in it.", { exact: false }).all()).toHaveLength(0);

    await page.getByRole("button", { name: "Save" }).click();
    await expect.poll(() => puts.length).toBe(1);
    // "summarise" is a built-in, so the derived id is numbered.
    expect(puts[0].id).toBe("summarise-2");
    expect(puts[0].body.question).toBe("");
  });

  it("refuses an unticked template with nothing in What it asks, without calling the operator", async () => {
    mountPanel();
    await page.getByRole("button", { name: "New template" }).click();
    await page.getByLabelText("Name").fill("Risks");
    await page.getByLabelText("Prompt").fill("List the risks.");
    await page.getByRole("button", { name: "Save" }).click();
    await expect.element(page.getByText("Say in one sentence what it asks", { exact: false })).toBeVisible();
    expect(puts).toHaveLength(0);
  });

  it("edits a custom template under its own id and shows the operator's reason for refusing a delete", async () => {
    rows = [...BUILT_INS, custom("resumen", "Resumen", "Resume la reunión.", "Resume.")];
    deleteRefusal = "resumen is the configured summary template";
    mountPanel();
    await page.getByRole("button", { name: "Edit", exact: true }).click();
    await expect.element(page.getByText("resumen.md")).toBeVisible();
    await page.getByLabelText("Name").fill("Resumen corto");
    await page.getByRole("button", { name: "Save" }).click();
    await expect.poll(() => puts.length).toBe(1);
    expect(puts[0].id).toBe("resumen");

    await page.getByRole("button", { name: "Delete" }).click();
    await page.getByRole("button", { name: "Yes, delete it" }).click();
    await expect.element(page.getByText("resumen is the configured summary template")).toBeVisible();
  });
});
