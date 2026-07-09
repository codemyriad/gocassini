import { mount } from "svelte";
import App from "./App.svelte";
// The browse surface's styles live in the viewing layer (D-420). The shell's
// own app.css (operator styling) returns when the operator surface lands (V3).
import "cassini-viewer/app.css";

mount(App, {
  target: document.getElementById("app")!,
});
