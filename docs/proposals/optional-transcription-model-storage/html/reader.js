(() => {
  const documents = [...document.querySelectorAll('.document')];
  const links = [...document.querySelectorAll('.doc-link')];
  const sections = document.querySelector('#section-nav');
  const progress = document.querySelector('.reading-progress span');
  let active;
  let sectionLinks = [];

  function updatePosition() {
    if (!active) return;
    const pageHeight = document.documentElement.scrollHeight - innerHeight;
    progress.style.width = (pageHeight > 0 ? Math.min(100, scrollY / pageHeight * 100) : 100) + '%';
    const headings = [...active.querySelectorAll('h2[id]')];
    let current = headings[0];
    for (const heading of headings) {
      if (heading.getBoundingClientRect().top <= 130) current = heading;
    }
    for (const link of sectionLinks) {
      if (link.hash === '#' + current?.id) link.setAttribute('aria-current', 'location');
      else link.removeAttribute('aria-current');
    }
  }

  function navigate() {
    const hash = decodeURIComponent(location.hash.slice(1));
    const target = document.getElementById(hash);
    const next = target?.closest('.document') ?? documents[0];
    if (next !== active) {
      active = next;
      for (const doc of documents) doc.hidden = doc !== active;
      for (const link of links) {
        if (link.dataset.document === active.id) link.setAttribute('aria-current', 'page');
        else link.removeAttribute('aria-current');
      }
      document.querySelector('#current-document').textContent = active.dataset.label;
      document.title = active.dataset.label + ' — gocassini shaping';
      sections.replaceChildren();
      for (const heading of active.querySelectorAll('h2[id]')) {
        const link = document.createElement('a');
        link.href = '#' + heading.id;
        link.textContent = [...heading.childNodes].filter(node => node.nodeType === Node.TEXT_NODE).map(node => node.textContent).join('').trim();
        sections.append(link);
      }
      sectionLinks = [...sections.querySelectorAll('a')];
    }
    requestAnimationFrame(() => {
      if (target && target !== active && target.id !== 'content') target.scrollIntoView({ block: 'start' });
      else scrollTo({ top: 0, behavior: 'instant' });
      updatePosition();
    });
  }

  addEventListener('hashchange', navigate);
  addEventListener('scroll', updatePosition, { passive: true });
  addEventListener('resize', updatePosition);
  document.querySelector('#print-all').addEventListener('click', () => print());

  const dialog = document.querySelector('#diagram-dialog');
  const largeDiagram = document.querySelector('#large-diagram');
  const zoom = document.querySelector('#diagram-zoom');
  const zoomValue = document.querySelector('#zoom-value');
  let naturalWidth = 1600;

  function setZoom() {
    const svg = largeDiagram.querySelector('svg');
    if (svg) svg.style.width = naturalWidth * Number(zoom.value) / 100 + 'px';
    zoomValue.textContent = zoom.value + '%';
  }
  for (const button of document.querySelectorAll('[data-expand-diagram]')) {
    button.addEventListener('click', () => {
      const original = button.closest('.breadboard').querySelector('svg');
      const svg = original.cloneNode(true);
      // Prefix IDs and their references: duplicate Mermaid SVG IDs otherwise
      // make marker and accessibility references point at the underlying page.
      const nodes = [svg, ...svg.querySelectorAll('*')];
      const ids = new Map(nodes.filter(node => node.id).map(node => [node.id, 'expanded-' + node.id]));
      for (const node of nodes) {
        for (const attribute of [...node.attributes]) {
          let value = attribute.value;
          if (attribute.name === 'id') value = ids.get(value) ?? value;
          else if (attribute.name === 'aria-labelledby' || attribute.name === 'aria-describedby') {
            value = value.split(' ').map(id => ids.get(id) ?? id).join(' ');
          } else if (attribute.name === 'href' || attribute.name === 'xlink:href') {
            if (value.startsWith('#') && ids.has(value.slice(1))) value = '#' + ids.get(value.slice(1));
          } else value = value.replace(/url\(#([^)]*)\)/g, (match, id) => ids.has(id) ? 'url(#' + ids.get(id) + ')' : match);
          node.setAttribute(attribute.name, value);
        }
        if (node.tagName.toLowerCase() === 'style') {
          node.textContent = node.textContent.replace(/#([a-zA-Z_][\w-]*)/g, (match, id) => ids.has(id) ? '#' + ids.get(id) : match);
        }
      }
      largeDiagram.replaceChildren(svg);
      naturalWidth = original.viewBox.baseVal.width || 1600;
      dialog.showModal();
      zoom.value = String(Math.max(25, Math.min(100, Math.floor((dialog.clientWidth - 70) / naturalWidth * 100 / 5) * 5)));
      setZoom();
    });
  }
  zoom.addEventListener('input', setZoom);
  document.querySelector('#close-diagram').addEventListener('click', () => dialog.close());
  dialog.addEventListener('click', event => { if (event.target === dialog) dialog.close(); });
  dialog.addEventListener('close', () => largeDiagram.replaceChildren());
  navigate();
})();
