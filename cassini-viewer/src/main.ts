import { mount } from "svelte";
import App from "./App.svelte";
import { StaticCatalogProvider } from "./viewer/dataProvider";
import "./app.css";

mount(App, {
  target: document.getElementById("app")!,
  props: { dataProvider: new StaticCatalogProvider() },
});
