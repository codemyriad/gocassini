import { mount, unmount } from "svelte";
import { expect, it } from "vitest";
import { page } from "vitest/browser";
import App from "../../App.svelte";
import "../../app.css";
import { annotationFixture } from "./annotations.fixture";

// A drag started off a word is the browser's own selection, and releasing it
// over another element fires a click on their common ancestor. That click used
// to read as "a click beside the selection" and drop it.

it("keeps a native text selection through the click that ends the drag", async () => {
  history.replaceState(null, "", location.pathname + location.search);
  const fixture = annotationFixture();
  const host = document.createElement("div"); host.style.height = "100vh"; document.body.append(host);
  const app = mount(App, { target: host, props: { dataProvider: fixture.provider } });
  await page.getByRole("button", { name: /^Annotation test Mon/ }).click();
  await expect.poll(() => host.querySelectorAll("[data-word-id]").length).toBe(6);
  const words = host.querySelectorAll<HTMLElement>("[data-word-id]");
  // A slow drag from blank space: the browser's own selection, registered
  // by the app (120ms debounce) before the button is released.
  document.getSelection()!.setBaseAndExtent(words[1].firstChild!, 0, words[4].lastChild!, words[4].lastChild!.textContent!.length);
  document.dispatchEvent(new Event("selectionchange"));
  await expect.element(page.getByRole("button", { name: /^Tag selection/ })).toBeVisible();
  // Releasing over a different element than the press: the browser fires the
  // click on their common ancestor, the transcript line.
  words[4].parentElement!.dispatchEvent(new MouseEvent("click", { bubbles: true, composed: true }));
  await new Promise((r) => setTimeout(r, 300));
  await expect.element(page.getByRole("button", { name: /^Tag selection/ })).toBeVisible();

  // A click beside it still lets it go: the press collapses the reader's
  // selection before the click arrives.
  document.getSelection()!.collapse(words[0].firstChild!, 0);
  document.dispatchEvent(new Event("selectionchange"));
  await new Promise((r) => setTimeout(r, 200));
  words[0].dispatchEvent(new MouseEvent("click", { bubbles: true, composed: true }));
  await expect.element(page.getByRole("button", { name: /^Tag selection/ })).not.toBeInTheDocument();
  await unmount(app); fixture.dispose(); host.remove();
});
