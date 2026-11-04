const items = document.querySelectorAll<HTMLElement>("[data-reveal]");

/** Delay between items revealed together, and the most any one item waits. */
const STEP = 110;
const MAX = 660;

if (document.documentElement.classList.contains("js") && items.length) {
  const io = new IntersectionObserver(
    entries => {
      const batch = entries
        .filter(e => e.isIntersecting)
        .map(e => ({ el: e.target as HTMLElement, box: e.boundingClientRect }))
        .sort((a, b) => Math.round(a.box.top - b.box.top) || a.box.left - b.box.left);
      batch.forEach(({ el }, i) => {
        el.style.setProperty("--reveal-delay", Math.min(i * STEP, MAX) + "ms");
        el.classList.add("is-revealed");
        io.unobserve(el);
      });
    },
    { rootMargin: "0px 0px -8% 0px" },
  );
  items.forEach(el => io.observe(el));

  // Hidden content can't take focus or print, so keyboard users and printing get everything at once.
  const revealAll = () => {
    io.disconnect();
    items.forEach(el => {
      if (el.classList.contains("is-revealed")) return;
      el.style.setProperty("--reveal-delay", "0ms");
      el.classList.add("is-revealed");
    });
  };
  const onKey = (e: KeyboardEvent) => {
    if (e.key !== "Tab") return;
    revealAll();
    document.removeEventListener("keydown", onKey);
  };
  document.addEventListener("keydown", onKey);
  addEventListener("beforeprint", revealAll);
}
