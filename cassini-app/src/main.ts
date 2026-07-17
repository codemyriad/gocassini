import { mount } from "svelte";
import App from "./App.svelte";
// The shell's stylesheet composes the viewing layer's app.css and adds a
// @source for the shell's own components (nav + operator surface) — D-420 V3.
import "./app.css";

mount(App, {
  target: document.getElementById("app")!,
});
