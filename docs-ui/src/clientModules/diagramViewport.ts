/**
 * Center a readable, horizontally scrollable diagram on first display. The
 * full canvas remains reachable in both directions, while the initial narrow
 * view shows the architectural center instead of an arbitrary left edge.
 */

function center(figure: HTMLElement): void {
  if (figure.dataset.diagramCentered) {
    return;
  }
  window.requestAnimationFrame(() => {
    const viewport = figure.querySelector<HTMLElement>('[data-diagram-scroll]')
      ?? figure;
    const overflow = viewport.scrollWidth - viewport.clientWidth;
    if (overflow <= 0) {
      return;
    }
    viewport.scrollLeft = Math.round(overflow / 2);
    figure.dataset.diagramCentered = 'true';
  });
}

function wire(details: HTMLDetailsElement): void {
  if (details.dataset.diagramWired) {
    return;
  }
  const figure = details.querySelector<HTMLElement>(
    'figure[data-diagram-explainer]',
  );
  if (!figure) {
    return;
  }
  details.dataset.diagramWired = 'true';
  details.addEventListener('toggle', () => {
    if (!details.open) {
      return;
    }
    const image = figure.querySelector<HTMLImageElement>('img');
    if (image && !image.complete) {
      image.addEventListener('load', () => center(figure), {once: true});
    } else {
      center(figure);
    }
  });
  if (details.open) {
    center(figure);
  }
}

export function onRouteDidUpdate(): void {
  if (typeof document === 'undefined') return;
  document.querySelectorAll<HTMLDetailsElement>('article details').forEach(wire);
  document
    .querySelectorAll<HTMLElement>('article figure[data-diagram-explainer]')
    .forEach((figure) => {
      if (!figure.closest('details')) center(figure);
    });
}
