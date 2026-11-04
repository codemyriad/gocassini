/** Single-select tab rows ([data-tabs]): click or arrow keys move the selection. */
for (const list of document.querySelectorAll<HTMLElement>("[data-tabs]")) {
  const tabs = [...list.querySelectorAll<HTMLButtonElement>("[role=tab]")];
  const select = (tab: HTMLButtonElement, focus = false) => {
    for (const t of tabs) {
      const on = t === tab;
      t.setAttribute("aria-selected", String(on));
      t.tabIndex = on ? 0 : -1;
      t.classList.toggle("tab-active", on);
    }
    if (focus) tab.focus();
  };
  tabs.forEach(t => t.addEventListener("click", () => select(t)));
  list.addEventListener("keydown", e => {
    const i = tabs.indexOf(document.activeElement as HTMLButtonElement);
    if (i < 0) return;
    const to = { ArrowRight: i + 1, ArrowLeft: i - 1, Home: 0, End: tabs.length - 1 }[e.key];
    if (to === undefined) return;
    e.preventDefault();
    select(tabs[(to + tabs.length) % tabs.length], true);
  });
}
