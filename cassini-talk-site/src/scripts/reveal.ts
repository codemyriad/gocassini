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
}
