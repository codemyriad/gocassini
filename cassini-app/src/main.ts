import { mount } from "svelte";
import App from "./AppEntry.svelte";
import { browserLanguages, nextcloudLanguage, nextcloudLocale, useFormatLocale, useLocale } from "cassini-viewer/i18n";
// The shell's stylesheet composes the viewing layer's app.css and adds a
// @source for the shell's own components (nav + operator surface) — D-420 V3.
import "./app.css";

document.documentElement.lang = useLocale([nextcloudLanguage(window), ...browserLanguages()]);
useFormatLocale([nextcloudLocale(window), nextcloudLanguage(window), ...browserLanguages()]);

mount(App, {
  target: document.getElementById("app")!,
});
