import { mount, unmount } from "svelte";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { page } from "vitest/browser";
import WebhookPanel from "./WebhookPanel.svelte";
import { OperatorClient } from "./operator/client";
import "./app.css";

const CATALOG = [
  "recording.started",
  "recording.stopped",
  "recording.published",
  "recording.failed",
  "summary.completed",
  "summary.failed",
];

// Long enough for the operator, which refuses a secret under 32 characters.
const SECRET = "s3cret-0123456789abcdef0123456789";

const delivery = () => ({ last_delivered_at: "", delivered: 0, waiting: 0, failed: 0, last_error: "", last_error_at: "" });
const off = () => ({ url: "", secret_set: false, events: [] as string[], timeout_seconds: 10, catalog: CATALOG, delivery: delivery(), error: "" });
const working = () => ({
  ...off(),
  url: "https://flows.example.org/hook",
  secret_set: true,
  events: ["recording.failed", "summary.completed"],
  delivery: { ...delivery(), delivered: 38, last_delivered_at: new Date(Date.now() - 4 * 60_000).toISOString() },
});

type Handler = (body: Record<string, unknown>) => Response;
let current: ReturnType<typeof off>;
let puts: Record<string, unknown>[];
let onPut: Handler | null;
let onTest: (() => Response | Promise<Response>) | null;
let gets: number;
let app: ReturnType<typeof mount> | undefined;
let host: HTMLDivElement;

beforeEach(() => {
  current = off();
  puts = [];
  onPut = null;
  onTest = null;
  gets = 0;
  host = document.createElement("div");
  host.className = "op-settings";
  document.body.append(host);
  vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const path = new URL(String(input), location.href).pathname;
    if (path === "/operator/settings/webhook/test") {
      return onTest ? onTest() : Response.json({ status: 204, duration_ms: 182 });
    }
    expect(path).toBe("/operator/settings/webhook");
    if (init?.method === "PUT") {
      const body = JSON.parse(init.body as string) as Record<string, unknown>;
      puts.push(body);
      if (onPut) return onPut(body);
      current = { ...current, ...body, secret_set: "secret" in body ? body.secret !== "" : current.secret_set } as typeof current;
      delete (current as Record<string, unknown>).secret;
    } else {
      gets++;
    }
    return Response.json(current);
  }));
});

afterEach(async () => {
  if (app) await unmount(app);
  app = undefined;
  host.remove();
  vi.unstubAllGlobals();
});

function mountPanel() {
  app = mount(WebhookPanel, { target: host, props: { operatorClient: new OperatorClient("/operator") } });
}

async function openSetup() {
  mountPanel();
  await expect.element(page.getByText("No receiver yet")).toBeVisible();
  await page.getByRole("button", { name: "Set up a receiver" }).click();
  await expect.element(page.getByRole("heading", { name: "Set up a receiver" })).toBeVisible();
}

describe("webhook panel in the browser", () => {
  it("starts a new receiver with nothing ticked and blocks Save until an event is", async () => {
    await openSetup();
    const boxes = page.getByRole("checkbox").all();
    expect(boxes).toHaveLength(CATALOG.length);
    for (const box of boxes) expect((box.element() as HTMLInputElement).checked).toBe(false);
    await expect.element(page.getByText("The recorder started")).toBeVisible();

    await page.getByLabelText("Receiver address").fill("https://flows.example.org/hook");
    await page.getByLabelText("Signing secret").fill(SECRET);
    await page.getByRole("button", { name: "Save" }).click();
    await expect.element(page.getByText("Pick at least one event.")).toBeVisible();
    expect(puts).toHaveLength(0);

    await page.getByRole("checkbox", { name: /summary\.completed/ }).click();
    await page.getByRole("button", { name: "Save" }).click();
    await expect.element(page.getByText("Saved. Send a test event to check that your receiver accepts it.")).toBeVisible();
    expect(puts).toEqual([
      { url: "https://flows.example.org/hook", timeout_seconds: 10, events: ["summary.completed"], secret: SECRET },
    ]);
    // The secret is never on the page after saving.
    expect(document.body.textContent).not.toContain(SECRET);
    await expect.element(page.getByText("Set", { exact: true })).toBeVisible();
    // A new receiver's delivery block is there, at zero.
    await expect.element(page.getByText("Nothing sent yet")).toBeVisible();
    await expect.element(page.getByText("No meeting event delivered yet")).toBeVisible();
  });

  it("says events come from every meeting, and that summary.completed sends the full text", async () => {
    await openSetup();
    await expect
      .element(page.getByText("Events come from every recorded meeting on this Nextcloud, in every room, including one-to-one calls.", { exact: false }))
      .toBeVisible();
    const bold = host.querySelector("label strong");
    expect(bold?.textContent).toBe("includes its full text, for every recorded meeting");
    expect(bold?.parentElement?.textContent).toBe("A summary was written; includes its full text, for every recorded meeting");
  });

  it("ticks the server's catalog with Select all", async () => {
    current = { ...off(), catalog: ["recording.started", "summary.failed", "meeting.future"] };
    await openSetup();
    await page.getByRole("button", { name: "Select all" }).click();
    const boxes = page.getByRole("checkbox").all();
    expect(boxes).toHaveLength(3);
    for (const box of boxes) expect((box.element() as HTMLInputElement).checked).toBe(true);
    // No words for an unknown event: its bare name only.
    await expect.element(page.getByText("meeting.future")).toBeVisible();
  });

  it("puts each coded refusal under its field, and an unknown one in a box", async () => {
    await openSetup();
    await page.getByLabelText("Receiver address").fill("https://flows.example.org/hook");
    await page.getByLabelText("Signing secret").fill(SECRET);
    await page.getByRole("button", { name: "Select all" }).click();
    const cases: [string, string][] = [
      ["webhook_url_invalid", "Enter a full address starting with https:// or http://."],
      ["webhook_secret_required", "A receiver needs a signing secret."],
      ["webhook_secret_required_for_new_url", "A new address needs its own secret. Generate one, or paste"],
      ["webhook_secret_too_short", "Use at least 32 characters."],
      ["webhook_events_required", "Pick at least one event."],
      ["webhook_event_unknown", "This version of Cassini does not know one of these events."],
      ["webhook_timeout_invalid", "Enter a whole number of seconds, from 1 to 60."],
      ["mystery_code", "the server's own words"],
    ];
    for (const [code, text] of cases) {
      onPut = () => Response.json({ error: "the server's own words", error_code: code }, { status: 400 });
      await page.getByRole("button", { name: "Save" }).click();
      await expect.element(page.getByText(text, { exact: false })).toBeVisible();
    }
    await expect.element(page.getByRole("alert")).toHaveTextContent("the server's own words");
  });

  it("shows the test result with its status and duration", async () => {
    current = working();
    mountPanel();
    await expect.element(page.getByText("Delivering")).toBeVisible();
    expect(gets).toBe(1);
    // The receiver recovered meanwhile: the panel reads delivery again after
    // the test.
    current = { ...current, delivery: { ...current.delivery, delivered: 39 } };
    await page.getByRole("button", { name: "Send test event" }).click();
    await expect.element(page.getByText("Receiver answered HTTP 204 in 182 ms.")).toBeVisible();
    await expect.poll(() => host.querySelector(".wh-fact .wh-v")?.textContent).toContain("39");
    expect(gets).toBe(2);

    onTest = () => Response.json({ status: 401, duration_ms: 95 });
    await page.getByRole("button", { name: "Send test event" }).click();
    await expect.element(page.getByText("Receiver answered HTTP 401 in 95 ms. It did not accept the signature.")).toBeVisible();

    onTest = () => Response.json({ error: "connection refused", duration_ms: 3 });
    await page.getByRole("button", { name: "Send test event" }).click();
    await expect.element(page.getByText("No answer from the receiver: connection refused")).toBeVisible();

    onTest = () =>
      Response.json({ error: "address not allowed: link-local/metadata (169.254.169.254)", error_code: "webhook_address_not_allowed", duration_ms: 1 });
    await page.getByRole("button", { name: "Send test event" }).click();
    await expect.element(page.getByText("Not sent. This address is link-local or a cloud metadata service", { exact: false })).toBeVisible();
    expect(document.body.textContent).not.toContain("No answer from the receiver");
  });

  it("words a refused test instead of showing the raw answer", async () => {
    current = working();
    mountPanel();
    await expect.element(page.getByText("Delivering")).toBeVisible();

    onTest = () =>
      Response.json({ error: "No webhook receiver is saved.", error_code: "webhook_not_configured" }, { status: 409 });
    await page.getByRole("button", { name: "Send test event" }).click();
    await expect.element(page.getByText("No receiver is saved. Set one up before sending a test event.")).toBeVisible();

    // A proxy in front of Cassini gave up first and answered with its own page.
    for (const status of [502, 504]) {
      onTest = () => new Response("<html>Gateway Time-out</html>", { status, headers: { "Content-Type": "text/html" } });
      await page.getByRole("button", { name: "Send test event" }).click();
      await expect.element(page.getByText("No answer in time. Either the receiver is slow or Cassini could not be reached.")).toBeVisible();
      expect(document.body.textContent).not.toContain("Gateway");
    }
  });

  it("says how many waiting events a new address deletes, above Save", async () => {
    current = { ...working(), delivery: { ...working().delivery, waiting: 3 } };
    mountPanel();
    await page.getByRole("button", { name: "Edit" }).click();
    const note = "3 events waiting for the old address will be deleted when you save.";
    expect(document.body.textContent).not.toContain(note);
    const address = page.getByLabelText("Receiver address");
    await address.fill("https://flows.example.org/other");
    await expect.element(page.getByText(note)).toBeVisible();
    await address.fill("https://flows.example.org/hook");
    await expect.element(page.getByText(note)).not.toBeInTheDocument();

    current = { ...current, delivery: { ...current.delivery, waiting: 1 } };
    await page.getByRole("button", { name: "Cancel" }).click();
    await page.getByRole("button", { name: "Send test event" }).click();
    await page.getByRole("button", { name: "Edit" }).click();
    await page.getByLabelText("Receiver address").fill("https://flows.example.org/other");
    await expect.element(page.getByText("1 event waiting for the old address will be deleted when you save.")).toBeVisible();
  });

  it("stops offering to keep the secret for a new address, and takes a generated one", async () => {
    current = working();
    mountPanel();
    await page.getByRole("button", { name: "Edit" }).click();
    const secret = page.getByLabelText("Signing secret");
    await expect.element(secret).toHaveAttribute("placeholder", "Leave empty to keep the saved secret");
    await page.getByLabelText("Receiver address").fill("https://flows.example.org/other");
    await expect.element(secret).toHaveAttribute("placeholder", "Paste or generate");
    await page.getByRole("button", { name: "Generate" }).click();
    const generated = (page.getByLabelText("Generated secret").element() as HTMLInputElement).value;
    await page.getByRole("button", { name: "Save" }).click();
    await expect.element(page.getByText(/^Saved\./)).toBeVisible();
    expect(puts).toEqual([{ url: "https://flows.example.org/other", secret: generated }]);
  });

  it("puts a refused replacement secret's reason under it", async () => {
    current = working();
    mountPanel();
    await page.getByRole("button", { name: "Replace" }).click();
    await page.getByLabelText("New secret").fill("short");
    onPut = () => Response.json({ error: "too short", error_code: "webhook_secret_too_short" }, { status: 400 });
    await page.getByRole("button", { name: "Save secret" }).click();
    await expect.element(page.getByText("Use at least 32 characters. Generate one to get a strong secret.")).toBeVisible();
    expect(puts).toEqual([{ secret: "short" }]);
  });

  it("drops a receiver removed elsewhere when a test send is refused", async () => {
    current = working();
    mountPanel();
    await expect.element(page.getByText("Delivering")).toBeVisible();
    // Removed in another tab: the operator has no receiver any more.
    current = off();
    onTest = () =>
      Response.json({ error: "No webhook receiver is saved.", error_code: "webhook_not_configured" }, { status: 409 });
    await page.getByRole("button", { name: "Send test event" }).click();
    await expect.element(page.getByText("No receiver yet")).toBeVisible();
    expect(document.body.textContent).not.toContain("https://flows.example.org/hook");
  });

  it("disables every action while a request is in flight", async () => {
    current = working();
    mountPanel();
    await expect.element(page.getByText("Delivering")).toBeVisible();
    let answer: ((response: Response) => void) | undefined;
    onTest = () => new Promise<Response>((resolve) => (answer = resolve));
    await page.getByRole("button", { name: "Send test event" }).click();
    await expect.element(page.getByText("Sending…")).toBeVisible();
    for (const name of ["Send test event", "Edit", "Replace", "Remove receiver"]) {
      await expect.element(page.getByRole("button", { name })).toBeDisabled();
    }
    answer?.(Response.json({ status: 204, duration_ms: 95 }));
    await expect.element(page.getByText("Receiver answered HTTP 204 in 95 ms.")).toBeVisible();
    await expect.element(page.getByRole("button", { name: "Send test event" })).toBeEnabled();
  });

  it("shows a failing receiver with its latest error and the secret hint", async () => {
    current = {
      ...working(),
      delivery: {
        last_delivered_at: "2026-11-16T16:42:00Z",
        delivered: 21,
        waiting: 5,
        failed: 2,
        last_error: "receiver returned HTTP 401",
        last_error_at: "2026-11-17T09:12:00Z",
      },
    };
    mountPanel();
    await expect.element(page.getByText("Receiver is not accepting messages")).toBeVisible();
    await expect.element(page.getByText("receiver returned HTTP 401")).toBeVisible();
    await expect.element(page.getByText(/A 401 or 403 usually means/)).toBeVisible();
    await expect.element(page.getByText("retrying, at most 5 min apart")).toBeVisible();
  });

  it("says when an error's time was not recorded, without a gap", async () => {
    current = {
      ...working(),
      delivery: { ...delivery(), waiting: 3, last_error: "connection refused", last_error_at: "" },
    };
    mountPanel();
    await expect.element(page.getByText("Receiver is not accepting messages")).toBeVisible();
    await expect.element(page.getByText("Latest error:")).toBeVisible();
    expect(document.body.textContent).not.toContain("Latest error, ");
  });

  it("is not failing for given-up events alone, but shows them in red", async () => {
    current = {
      ...working(),
      delivery: { ...working().delivery, failed: 2, last_error: "receiver returned HTTP 500", last_error_at: new Date().toISOString() },
    };
    mountPanel();
    await expect.element(page.getByText("Delivering")).toBeVisible();
    expect(document.body.textContent).not.toContain("Receiver is not accepting messages");
    expect(host.querySelector(".wh-fact.wh-bad")?.textContent).toContain("Gave up after 24 hours");
  });

  it("offers a fresh setup when the saved settings could not be loaded", async () => {
    current = { ...off(), error: 'unknown event "meeting.shared"' };
    mountPanel();
    await expect.element(page.getByText("Your receiver could not be loaded")).toBeVisible();
    await expect.element(page.getByText('unknown event "meeting.shared"')).toBeVisible();
    await page.getByRole("button", { name: "Set up a receiver" }).click();
    await expect.element(page.getByLabelText("Signing secret")).toHaveAttribute("placeholder", "Paste or generate");
  });

  it("edits only what changed and keeps the secret when the field is empty", async () => {
    current = working();
    mountPanel();
    await page.getByRole("button", { name: "Edit" }).click();
    await expect.element(page.getByRole("heading", { name: "Edit receiver" })).toBeVisible();
    await expect.element(page.getByLabelText("Signing secret")).toHaveValue("");
    await page.getByLabelText("Wait for an answer").fill("30");
    await page.getByRole("button", { name: "Save" }).click();
    await expect.element(page.getByText(/^Saved\./)).toBeVisible();
    expect(puts).toEqual([{ timeout_seconds: 30 }]);
  });

  it("replaces the secret with a generated one shown once", async () => {
    current = working();
    mountPanel();
    await page.getByRole("button", { name: "Replace" }).click();
    await page.getByRole("button", { name: "Generate" }).click();
    const field = page.getByLabelText("Generated secret");
    await expect.element(field).toBeVisible();
    const generated = (field.element() as HTMLInputElement).value;
    expect(generated).toMatch(/^[0-9a-f]{64}$/);
    await page.getByRole("button", { name: "Save secret" }).click();
    await expect.element(page.getByText(/^Saved\./)).toBeVisible();
    expect(puts).toEqual([{ secret: generated }]);
    expect(document.body.textContent).not.toContain(generated);
  });

  it("removes the receiver only after an inline confirm", async () => {
    current = working();
    mountPanel();
    await page.getByRole("button", { name: "Remove receiver" }).click();
    expect(puts).toHaveLength(0);
    await page.getByRole("button", { name: "Keep it" }).click();
    await page.getByRole("button", { name: "Remove receiver" }).click();
    await page.getByRole("button", { name: "Remove", exact: true }).click();
    await expect.element(page.getByText("No receiver yet")).toBeVisible();
    expect(puts).toEqual([{ url: "", secret: "", events: [], timeout_seconds: 10 }]);
  });

  it("copies a generated secret, and selects it when the clipboard refuses", async () => {
    current = working();
    mountPanel();
    await page.getByRole("button", { name: "Replace" }).click();
    await page.getByRole("button", { name: "Generate" }).click();
    const field = page.getByLabelText("Generated secret");
    const input = field.element() as HTMLInputElement;

    const writeText = vi.spyOn(navigator.clipboard, "writeText").mockResolvedValue(undefined);
    await page.getByRole("button", { name: "Copy" }).click();
    await expect.element(page.getByRole("button", { name: "Copied" })).toBeVisible();
    expect(writeText).toHaveBeenCalledWith(input.value);

    // A fresh secret, and a page where neither clipboard path works.
    await page.getByRole("button", { name: "Generate" }).click();
    writeText.mockRejectedValue(new Error("denied"));
    const execCommand = vi.spyOn(document, "execCommand").mockReturnValue(false);
    await page.getByRole("button", { name: "Copy" }).click();
    await expect.element(page.getByText(/Your browser blocked copying/)).toBeVisible();
    expect(execCommand).toHaveBeenCalledWith("copy");
    const fresh = page.getByLabelText("Generated secret").element() as HTMLInputElement;
    expect([fresh.selectionStart, fresh.selectionEnd]).toEqual([0, fresh.value.length]);
    writeText.mockRestore();
    execCommand.mockRestore();
  });
});
