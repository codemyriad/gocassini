import { mount } from "svelte";
import App from "./App.svelte";
import { StaticCatalogProvider } from "./viewer/dataProvider";
import { browserLanguages, useFormatLocale, useLocale } from "./i18n";
import "./app.css";

document.documentElement.lang = useLocale(browserLanguages());
useFormatLocale(browserLanguages());

mount(App, {
  target: document.getElementById("app")!,
  props: { dataProvider: new StaticCatalogProvider() },
});
