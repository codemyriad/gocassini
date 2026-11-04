const items = document.querySelectorAll<HTMLElement>("[data-reveal]");

if (document.documentElement.classList.contains("js") && items.length) {
  const io = new IntersectionObserver(
    entries => {
      for (const entry of entries) {
        if (!entry.isIntersecting) continue;
        entry.target.classList.add("is-revealed");
        io.unobserve(entry.target);
      }
    },
    { rootMargin: "0px 0px -8% 0px" },
  );
  items.forEach(el => io.observe(el));

  // Hidden content can't take focus or print, so keyboard users and printing get everything at once.
  const revealAll = () => {
    io.disconnect();
    items.forEach(el => el.classList.add("is-revealed"));
  };
  const onKey = (e: KeyboardEvent) => {
    if (e.key !== "Tab") return;
    revealAll();
    document.removeEventListener("keydown", onKey);
  };
  document.addEventListener("keydown", onKey);
  addEventListener("beforeprint", revealAll);
}
