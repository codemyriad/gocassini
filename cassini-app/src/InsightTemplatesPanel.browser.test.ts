import { mount, unmount } from "svelte";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { page, userEvent } from "vitest/browser";
import Settings from "./Settings.svelte";
import "./app.css";

type Row = Record<string, string>;

const builtIn = (id: string, name: string, description: string, instruction: string): Row => ({
  id, name, description, instruction,
  version: "v0", sha256: `${id}-sha`, origin: "Built in",
});
const custom = (id: string, name: string, description: string, instruction: string): Row => ({
  ...builtIn(id, name, description, instruction), version: "v1", origin: "Custom",
});

const BUILT_INS = [
  builtIn("summarise", "Meeting summary", "Summarises what happened.", "You are a meeting-summary editor."),
  builtIn("ask", "Ask your own question", "Answers a question of your own about the meetings you picked, with who said what and what they leave unanswered.", "Answer {{QUESTION}} from the transcripts."),
];

let rows: Row[] = [];
let puts: Array<{ id: string; body: Record<string, string> }> = [];
let deleteRefusal = "";
let summaryTemplate = "";
let insightTemplate = "";
let deleted: string[] = [];
let app: ReturnType<typeof mount> | undefined;
let host: HTMLDivElement;

beforeEach(() => {
  rows = [...BUILT_INS];
  puts = [];
  deleteRefusal = "";
  summaryTemplate = "";
  insightTemplate = "";
  deleted = [];
  host = document.createElement("div");
  document.body.append(host);
  vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const path = new URL(String(input), location.href).pathname;
    if (path === "/operator/settings/llm") {
      return Response.json({ providers: [], summary: { template: summaryTemplate }, insight: { template: insightTemplate } });
    }
    if (path === "/operator/settings/workflows") return Response.json(rows);
    const id = decodeURIComponent(path.replace("/operator/settings/workflows/", ""));
    if (init?.method === "PUT") {
      const body = JSON.parse(String(init.body)) as Record<string, string>;
      puts.push({ id, body });
      const saved = custom(id, body.name, body.description, body.instruction);
      rows = [...rows.filter((row) => row.id !== id), saved];
      return Response.json(saved);
    }
    if (init?.method === "DELETE") {
      if (deleteRefusal) return Response.json({ error: deleteRefusal }, { status: 409 });
      deleted.push(id);
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
  delete (window as Window & { __CASSINI_CONFIG__?: unknown }).__CASSINI_CONFIG__;
});

let panelsAsked: string[] = [];

// Mounted through the settings host, as the operator surface does, so the
// panel wears the shared settings styles its layout is measured in and its
// links travel the same way the nav does.
function mountPanel() {
  panelsAsked = [];
  Object.assign(window, { __CASSINI_CONFIG__: { operatorBasePath: "/operator" } });
  app = mount(Settings, {
    target: host,
    props: { panel: "templates" },
    events: { panel: (event: CustomEvent<string>) => panelsAsked.push(event.detail) },
  });
}

// An untrusted keydown: it reaches the panel's listeners exactly as a key
// press does, but the browser takes no default action on it, so a dialog it
// closes was closed by the panel and not by the browser's own Escape.
function pressEscapeOnFocused() {
  const target = document.activeElement ?? document.body;
  target.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape", bubbles: true, composed: true, cancelable: true }));
}

const builtInHeading = () => page.getByRole("heading", { name: /Built in/ });
const deleteDialog = () => page.getByRole("dialog", { name: "Delete Resumen?" });
const SUMMARY_IN_USE =
  "This template is being used for generating summaries after calls are transcribed. Replace it with another template before deleting it.";
const INSIGHT_IN_USE =
  "This template is being used as the default for new insights. Replace it with another template before deleting it.";
const RESUMEN = () => custom("resumen", "Resumen", "Un resumen en español.", "Resume.");

describe("insight templates panel in the browser", () => {
  it("lists built-ins under a plain heading, with no edit controls on them", async () => {
    mountPanel();
    await expect.element(page.getByRole("heading", { name: "Your templates" })).toBeVisible();
    await expect.element(page.getByText("None yet.", { exact: false })).toBeVisible();
    await expect.element(builtInHeading()).toHaveTextContent("Built in 2");
    await expect.element(page.getByText("Shipped with Cassini. Duplicate one to change it.", { exact: true })).toBeVisible();
    expect(page.getByText("recorded by hash", { exact: false }).all()).toHaveLength(0);
    await expect.element(page.getByRole("heading", { name: "Meeting summary" })).toBeVisible();
    expect(page.getByRole("button", { name: /Built in/ }).all()).toHaveLength(0);
    expect(page.getByRole("button", { name: "Duplicate" }).all()).toHaveLength(2);
    expect(page.getByRole("button", { name: "Edit", exact: true }).all()).toHaveLength(0);
    expect(page.getByRole("button", { name: "Delete" }).all()).toHaveLength(0);
  });

  it("keeps built-ins open when a custom template exists, and lists it first", async () => {
    rows = [...BUILT_INS, RESUMEN()];
    mountPanel();
    await expect.element(page.getByRole("heading", { name: "Resumen" })).toBeVisible();
    await expect.element(page.getByRole("heading", { name: "Meeting summary" })).toBeVisible();
    const resumen = page.getByRole("heading", { name: "Resumen" }).element();
    expect(resumen.compareDocumentPosition(builtInHeading().element()) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
  });

  it("puts each row's name, description and actions on one compact line, the prompt under it", async () => {
    rows = [...BUILT_INS, RESUMEN()];
    mountPanel();
    const name = page.getByRole("heading", { name: "Meeting summary" });
    await expect.element(name).toBeVisible();
    const card = name.element().closest("article") as HTMLElement;
    const description = card.querySelector(".tpl-row-desc") as HTMLElement;
    const duplicate = [...card.querySelectorAll("button")].find((b) => b.textContent?.trim() === "Duplicate")!;
    const toggle = card.querySelector("summary") as HTMLElement;
    const top = (el: Element) => el.getBoundingClientRect().top;
    const mid = (el: Element) => el.getBoundingClientRect().top + el.getBoundingClientRect().height / 2;
    // A short description sits beside the name, the actions on the same line.
    expect(Math.abs(mid(description) - mid(name.element()))).toBeLessThan(6);
    expect(Math.abs(mid(duplicate) - mid(name.element()))).toBeLessThan(6);
    expect(top(toggle)).toBeGreaterThan(top(name.element()));
    expect(toggle.textContent).toContain("Prompt");
    // Compact: a closed built-in row is two short lines (it was about 103px
    // with the name, description and prompt each on a line of its own).
    expect(card.getBoundingClientRect().height).toBeLessThan(70);

    const resumenCard = page.getByRole("heading", { name: "Resumen" }).element().closest("article") as HTMLElement;
    const labels = [...resumenCard.querySelectorAll(".tpl-row-actions button")].map((b) => b.textContent?.trim());
    expect(labels).toEqual(["Edit", "Duplicate", "Delete"]);
    expect(resumenCard.querySelector(".tpl-row-actions")?.textContent?.replace(/\s+/g, " ").trim()).toBe(
      "Edit · Duplicate · Delete",
    );
    // The question marker rides on the prompt line.
    const askCard = page.getByRole("heading", { name: "Ask your own question" }).element().closest("article") as HTMLElement;
    expect(askCard.querySelector("summary")?.textContent?.trim()).toMatch(/^Prompt\s*· asks for a question$/);
  });

  it("duplicates a built-in into a new template under Your templates, deriving the id from the name", async () => {
    mountPanel();
    await page.getByRole("button", { name: "Duplicate" }).first().click();
    const form = page.getByRole("form", { name: "New insight template" });
    await expect.element(form).toBeVisible();
    // Opened above the built-in list, not as an expansion of the source row.
    const formEl = form.element();
    expect(formEl.compareDocumentPosition(builtInHeading().element()) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
    await expect.element(page.getByLabelText("Name")).toHaveValue("Meeting summary (copy)");
    await expect.element(page.getByLabelText("Description")).toHaveValue("Summarises what happened.");
    // No id to type, and no file line until it exists.
    expect(page.getByText("File:", { exact: false }).all()).toHaveLength(0);

    await page.getByRole("button", { name: "Save" }).click();
    await expect.poll(() => puts.length).toBe(1);
    expect(puts[0].id).toBe("meeting-summary-copy");
    expect(puts[0].body).toEqual({
      name: "Meeting summary (copy)",
      description: "Summarises what happened.",
      instruction: "You are a meeting-summary editor.",
    });
    await expect.element(page.getByRole("heading", { name: "Meeting summary (copy)" })).toBeVisible();
  });

  it("shows Name, Description and Prompt with their hints above the fields, and no question checkbox", async () => {
    mountPanel();
    await page.getByRole("button", { name: "New template" }).click();
    expect(page.getByText("What people pick it by.").all()).toHaveLength(0);
    const descriptionHint = page.getByText("Shown to the person choosing it, so they know what it does.");
    const promptHint = page.getByText("The instruction the model receives, together with the transcripts.", { exact: false });
    await expect.element(descriptionHint).toBeVisible();
    await expect.element(promptHint).toBeVisible();
    await expect
      .element(page.getByText("where a question should go, and people will be asked for one when they pick this template.", { exact: false }))
      .toBeVisible();
    const above = (hint: Element, field: Element) =>
      hint.getBoundingClientRect().bottom <= field.getBoundingClientRect().top &&
      Boolean(hint.compareDocumentPosition(field) & Node.DOCUMENT_POSITION_FOLLOWING);
    expect(above(descriptionHint.element(), page.getByLabelText("Description").element())).toBe(true);
    expect(above(promptHint.element(), page.getByLabelText("Prompt").element())).toBe(true);
    // The two prompt lines are one hint, both above the textarea.
    expect(promptHint.element().textContent).toContain("{{QUESTION}}");
    await expect.element(page.getByLabelText("Prompt")).toHaveAccessibleDescription(/together with the transcripts/);
    expect(page.getByRole("checkbox").all()).toHaveLength(0);
    const labels = [...document.querySelectorAll("form .op-field-label")].map((el) => el.textContent?.trim());
    expect(labels).toEqual(["Name", "Description", "Prompt"]);
  });

  it("saves a template with {{QUESTION}} in its prompt and an empty description", async () => {
    mountPanel();
    await page.getByRole("button", { name: "New template" }).click();
    await expect.element(page.getByLabelText("Prompt")).toHaveAttribute("placeholder", expect.stringContaining("Markdown"));
    await page.getByLabelText("Name").fill("Summarise");
    await page.getByLabelText("Prompt").fill("Answer {{QUESTION}} briefly.");
    await page.getByRole("button", { name: "Save" }).click();
    await expect.poll(() => puts.length).toBe(1);
    // "summarise" is a built-in, so the derived id is numbered.
    expect(puts[0].id).toBe("summarise-2");
    expect(puts[0].body).toEqual({ name: "Summarise", description: "", instruction: "Answer {{QUESTION}} briefly." });
  });

  it("duplicates the built-in that takes a question, placeholder and all", async () => {
    mountPanel();
    expect(page.getByText("cannot be duplicated", { exact: false }).all()).toHaveLength(0);
    await page.getByRole("button", { name: "Duplicate" }).nth(1).click();
    await expect.element(page.getByLabelText("Prompt")).toHaveValue("Answer {{QUESTION}} from the transcripts.");
    await page.getByRole("button", { name: "Save" }).click();
    await expect.poll(() => puts.length).toBe(1);
    expect(puts[0].body.instruction).toContain("{{QUESTION}}");
  });

  it("refuses {{TEMPLATE}} in the prompt without calling the operator", async () => {
    mountPanel();
    await page.getByRole("button", { name: "New template" }).click();
    await page.getByLabelText("Name").fill("Risks");
    await page.getByLabelText("Prompt").fill("List the risks.\n\n{{TEMPLATE}}");
    await expect.element(page.getByText("A custom template has no place for {{TEMPLATE}}.", { exact: false })).toBeVisible();
    await page.getByRole("button", { name: "Save" }).click();
    expect(puts).toHaveLength(0);
  });

  it("edits a custom template under its own id", async () => {
    rows = [...BUILT_INS, RESUMEN()];
    mountPanel();
    await page.getByRole("button", { name: "Edit", exact: true }).click();
    await expect.element(page.getByText("resumen.md")).toBeVisible();
    await page.getByLabelText("Name").fill("Resumen corto");
    await page.getByRole("button", { name: "Save" }).click();
    await expect.poll(() => puts.length).toBe(1);
    expect(puts[0].id).toBe("resumen");
  });

  it("asks in a dialog before deleting, focused on Cancel, and deletes on Delete", async () => {
    rows = [...BUILT_INS, RESUMEN()];
    mountPanel();
    await page.getByRole("button", { name: "Delete" }).click();
    const dialog = deleteDialog();
    await expect.element(dialog).toBeVisible();
    expect((dialog.element() as HTMLDialogElement).open).toBe(true);
    await expect
      .element(dialog)
      .toHaveTextContent(
        "Delete Resumen? Insights that have already been produced from it will keep a reference to its name, id, version and hash.",
      );
    expect(dialog.getByRole("heading", { name: "Delete Resumen?" }).element().querySelector("strong")?.textContent).toBe("Resumen");
    await expect.element(dialog.getByRole("button", { name: "Cancel" })).toHaveFocus();

    await dialog.getByRole("button", { name: "Delete" }).click();
    await expect.poll(() => deleted).toEqual(["resumen"]);
    await expect.element(dialog).not.toBeInTheDocument();
    await expect.element(page.getByRole("heading", { name: "Resumen" })).not.toBeInTheDocument();
  });

  it("cancels the dialog with Cancel, Escape and the backdrop, deleting nothing", async () => {
    rows = [...BUILT_INS, RESUMEN()];
    mountPanel();
    const dialogEl = () => document.querySelector("dialog") as HTMLDialogElement;

    await page.getByRole("button", { name: "Delete" }).click();
    await deleteDialog().getByRole("button", { name: "Cancel" }).click();
    await expect.poll(() => dialogEl().open).toBe(false);

    // The browser's own Escape (a trusted key press, closing via "cancel").
    await page.getByRole("button", { name: "Delete" }).click();
    await expect.poll(() => dialogEl().open).toBe(true);
    await userEvent.keyboard("{Escape}");
    await expect.poll(() => dialogEl().open).toBe(false);

    // A keydown on the focused Cancel that the browser does not act on, as
    // inside the shadow root on Nextcloud's page: the panel has to close it.
    await page.getByRole("button", { name: "Delete" }).click();
    await expect.poll(() => dialogEl().open).toBe(true);
    await expect.element(deleteDialog().getByRole("button", { name: "Cancel" })).toHaveFocus();
    pressEscapeOnFocused();
    await expect.poll(() => dialogEl().open).toBe(false);

    await page.getByRole("button", { name: "Delete" }).click();
    await expect.poll(() => dialogEl().open).toBe(true);
    // The dialog fills the viewport; its box is in the middle, so a corner is backdrop.
    await page.elementLocator(dialogEl()).click({ position: { x: 4, y: 4 } });
    await expect.poll(() => dialogEl().open).toBe(false);
    expect(deleted).toEqual([]);
  });

  it("opens straight into the in-use state for the summary template, without asking the operator", async () => {
    rows = [...BUILT_INS, RESUMEN()];
    summaryTemplate = "resumen";
    mountPanel();
    await page.getByRole("button", { name: "Delete" }).click();
    const dialog = deleteDialog();
    await expect.element(dialog).toHaveTextContent(`Delete Resumen? ${SUMMARY_IN_USE}`);
    await expect.element(dialog.getByRole("button", { name: "Cancel" })).toHaveFocus();
    expect(dialog.getByRole("button", { name: "Delete" }).all()).toHaveLength(0);
    expect(dialog.getByRole("button", { name: "Close" }).all()).toHaveLength(0);
    const actions = [...dialog.element().querySelectorAll(".tpl-dialog-actions button")].map((b) => b.textContent?.trim());
    expect(actions).toEqual(["Cancel", "Open the Publish pipeline"]);

    await dialog.getByRole("button", { name: "Open the Publish pipeline" }).click();
    expect(panelsAsked).toEqual(["pipeline"]);
    await expect.poll(() => (document.querySelector("dialog") as HTMLDialogElement).open).toBe(false);
    expect(deleted).toEqual([]);
  });

  it("says when the template is the insight default, with OK as there is nowhere in the app to change it", async () => {
    rows = [...BUILT_INS, RESUMEN()];
    insightTemplate = "resumen";
    mountPanel();
    await page.getByRole("button", { name: "Delete" }).click();
    const dialog = deleteDialog();
    await expect.element(dialog).toHaveTextContent(`Delete Resumen? ${INSIGHT_IN_USE}`);
    const actions = [...dialog.element().querySelectorAll(".tpl-dialog-actions button")].map((b) => b.textContent?.trim());
    expect(actions).toEqual(["Cancel", "OK"]);
    await dialog.getByRole("button", { name: "OK" }).click();
    await expect.poll(() => (document.querySelector("dialog") as HTMLDialogElement).open).toBe(false);
    expect(panelsAsked).toEqual([]);
  });

  it("lands in the same in-use state when the operator refuses a delete the settings did not predict", async () => {
    rows = [...BUILT_INS, RESUMEN()];
    mountPanel();
    await page.getByRole("button", { name: "Delete" }).click();
    const dialog = deleteDialog();
    await expect.element(dialog.getByRole("button", { name: "Delete" })).toBeVisible();
    // Someone set it as the summary template after the dialog opened.
    summaryTemplate = "resumen";
    deleteRefusal = '"resumen" is the template for meeting summaries; choose another one there before deleting it';
    await dialog.getByRole("button", { name: "Delete" }).click();
    await expect.element(dialog).toHaveTextContent(`Delete Resumen? ${SUMMARY_IN_USE}`);
    expect(dialog.getByRole("alert").all()).toHaveLength(0);
    expect(dialog.getByRole("button", { name: "Delete" }).all()).toHaveLength(0);
    await expect.element(dialog.getByRole("button", { name: "Cancel" })).toHaveFocus();
    expect(deleted).toEqual([]);
  });

  it("cancels the in-use state on Escape and the backdrop, without the browser's own cancel", async () => {
    rows = [...BUILT_INS, RESUMEN()];
    insightTemplate = "resumen";
    mountPanel();
    const dialogEl = () => document.querySelector("dialog") as HTMLDialogElement;
    await page.getByRole("button", { name: "Delete" }).click();
    await expect.element(deleteDialog().getByRole("button", { name: "Cancel" })).toHaveFocus();
    pressEscapeOnFocused();
    await expect.poll(() => dialogEl().open).toBe(false);

    await page.getByRole("button", { name: "Delete" }).click();
    await expect.poll(() => dialogEl().open).toBe(true);
    await page.elementLocator(dialogEl()).click({ position: { x: 4, y: 4 } });
    await expect.poll(() => dialogEl().open).toBe(false);
    expect(deleted).toEqual([]);
  });
});
