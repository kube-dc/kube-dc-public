/**
 * Center a readable, horizontally scrollable D2 diagram the first time its
 * temporary comparison disclosure opens. The full canvas remains reachable in
 * both directions, while the initial view shows the architectural center
 * instead of an empty ELK margin.
 */

function center(figure: HTMLElement): void {
  if (figure.dataset.diagramCentered) {
    return;
  }
  window.requestAnimationFrame(() => {
    const overflow = figure.scrollWidth - figure.clientWidth;
    if (overflow <= 0) {
      return;
    }
    figure.scrollLeft = Math.round(overflow / 2);
    figure.dataset.diagramCentered = 'true';
  });
}

function wire(details: HTMLDetailsElement): void {
  if (details.dataset.diagramWired) {
    return;
  }
  const figure = details.querySelector<HTMLElement>('figure.diagram-comparison');
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
}
