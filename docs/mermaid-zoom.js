// Zoom modal pour les diagrammes Mermaid.
// - Click sur un diagramme → ouvre une modale plein écran avec le SVG
// - Mollette pour zoom in/out, drag pour pan
// - ESC ou clic sur le fond pour fermer
//
// Utilise svg-pan-zoom (CDN) pour le pan+zoom interactif.

(function () {
  let modalReady = false;
  let panZoomInstance = null;

  function buildModal() {
    if (modalReady) return;
    modalReady = true;

    const overlay = document.createElement('div');
    overlay.id = 'mermaid-zoom-overlay';
    overlay.innerHTML = `
      <div class="mz-toolbar">
        <button class="mz-btn" data-action="zoom-in" title="Zoom +">+</button>
        <button class="mz-btn" data-action="zoom-out" title="Zoom −">−</button>
        <button class="mz-btn" data-action="reset" title="Réinitialiser">⟲</button>
        <button class="mz-btn mz-close" data-action="close" title="Fermer (ESC)">✕</button>
      </div>
      <div class="mz-stage"></div>
      <div class="mz-hint">Mollette = zoom · drag = déplacer · ESC = fermer</div>
    `;
    document.body.appendChild(overlay);

    overlay.addEventListener('click', (e) => {
      const action = e.target.dataset?.action;
      if (action === 'close' || e.target === overlay) {
        closeModal();
      } else if (action === 'zoom-in' && panZoomInstance) {
        panZoomInstance.zoomIn();
      } else if (action === 'zoom-out' && panZoomInstance) {
        panZoomInstance.zoomOut();
      } else if (action === 'reset' && panZoomInstance) {
        panZoomInstance.resetZoom();
        panZoomInstance.center();
      }
    });

    document.addEventListener('keydown', (e) => {
      if (e.key === 'Escape') closeModal();
    });
  }

  function openModal(originalSvg) {
    buildModal();
    const overlay = document.getElementById('mermaid-zoom-overlay');
    const stage = overlay.querySelector('.mz-stage');

    // Clone le SVG dans la modale
    const clone = originalSvg.cloneNode(true);
    clone.removeAttribute('style'); // libère les contraintes width/height inline
    clone.setAttribute('width', '100%');
    clone.setAttribute('height', '100%');
    stage.innerHTML = '';
    stage.appendChild(clone);

    overlay.classList.add('mz-open');
    document.body.style.overflow = 'hidden';

    // Init pan/zoom (la lib doit être chargée)
    if (typeof svgPanZoom === 'function') {
      // setTimeout pour laisser le rendu se faire avant de mesurer
      setTimeout(() => {
        try {
          panZoomInstance = svgPanZoom(clone, {
            zoomEnabled: true,
            controlIconsEnabled: false,
            fit: true,
            center: true,
            minZoom: 0.3,
            maxZoom: 8,
            zoomScaleSensitivity: 0.3,
          });
        } catch (err) {
          console.warn('svgPanZoom init failed:', err);
        }
      }, 50);
    }
  }

  function closeModal() {
    const overlay = document.getElementById('mermaid-zoom-overlay');
    if (!overlay) return;
    overlay.classList.remove('mz-open');
    document.body.style.overflow = '';
    if (panZoomInstance) {
      try { panZoomInstance.destroy(); } catch {}
      panZoomInstance = null;
    }
  }

  function attachZoomToMermaidBlocks() {
    document.querySelectorAll('.mermaid').forEach((block) => {
      if (block.dataset.mzAttached === '1') return;
      const svg = block.querySelector('svg');
      if (!svg) return;
      block.dataset.mzAttached = '1';
      block.style.cursor = 'zoom-in';
      block.title = 'Cliquer pour zoomer / explorer le diagramme';
      block.addEventListener('click', (e) => {
        e.preventDefault();
        openModal(svg);
      });
    });
  }

  // Mermaid rend les SVG après que la page est chargée. On attend un peu puis
  // on attache les handlers. Si certains diagrammes mettent plus de temps,
  // on retente quelques fois.
  function waitAndAttach() {
    let tries = 0;
    const id = setInterval(() => {
      attachZoomToMermaidBlocks();
      tries++;
      if (tries >= 10) clearInterval(id);
    }, 300);
  }

  if (document.readyState === 'complete') {
    waitAndAttach();
  } else {
    window.addEventListener('load', waitAndAttach);
  }
})();
