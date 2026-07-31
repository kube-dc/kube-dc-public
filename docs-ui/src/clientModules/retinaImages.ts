/**
 * Retina-aware sizing for documentation screenshots.
 *
 * Screenshots are captured on 2x (HiDPI) displays, so their pixel width is
 * twice the UI size they depict. Rendered at natural pixel width, a small
 * dialog fills the whole content column with giant text; rendered at half
 * width it looks exactly like the real UI.
 *
 * Detection uses the PNG `pHYs` chunk rather than guessing from pixel width:
 * macOS Retina screenshots embed 5669 pixels-per-metre (144 dpi), while 1x
 * exports and generated diagrams carry 2835 (72 dpi) or no chunk at all. A
 * survey of the docs images found exactly this split, with no overlap.
 *
 * For each qualifying `article img`, the PNG header is fetched (same-origin,
 * served from the browser cache) and, when the chunk says 2x, the image gets
 * an explicit width of naturalWidth / 2. The stylesheet's
 * `article img { max-width: 100% }` still clamps anything wider than the
 * content column, so full-page captures stay column-sized.
 *
 * Author opt-outs win: an inline `style.width` or the `screenshot-narrow` /
 * `screenshot-full` classes are never overridden. (The build-time `width`
 * attribute Docusaurus stamps on every markdown image is NOT author intent
 * and is ignored.)
 */

const RETINA_MIN_PPM = 4000; // 2x Mac = 5669 ppm; 1x = 2835 ppm or absent

const dpiCache = new Map<string, Promise<boolean>>();

async function isRetinaPng(src: string): Promise<boolean> {
  try {
    const res = await fetch(src);
    if (!res.ok) {
      return false;
    }
    const buf = new Uint8Array(await res.arrayBuffer());
    const limit = Math.min(buf.length - 12, 4096);
    for (let i = 8; i < limit; i++) {
      // 'pHYs'
      if (buf[i] === 0x70 && buf[i + 1] === 0x48 && buf[i + 2] === 0x59 && buf[i + 3] === 0x73) {
        const x =
          (buf[i + 4] << 24) | (buf[i + 5] << 16) | (buf[i + 6] << 8) | buf[i + 7];
        return x >= RETINA_MIN_PPM;
      }
    }
    // Fallback for captures whose export pipeline stripped the pHYs chunk:
    // nothing 1x in these docs is anywhere near this wide (diagrams top out
    // ~1400px), while full-window Retina captures are 2200px+. Screenshots
    // smaller than this that lost their chunk must be re-stamped — see the
    // repo notes.
    const ihdrW =
      (buf[16] << 24) | (buf[17] << 16) | (buf[18] << 8) | buf[19];
    return ihdrW >= 2000;
  } catch {
    /* network/parse failure → treat as 1x, render natural */
  }
  return false;
}

function fit(img: HTMLImageElement): void {
  if (img.dataset.retinaFit) {
    return;
  }
  if (
    img.style.width ||
    img.classList.contains('screenshot-narrow') ||
    img.classList.contains('screenshot-full')
  ) {
    img.dataset.retinaFit = 'skip';
    return;
  }
  img.dataset.retinaFit = 'pending';
  const apply = async () => {
    const src = img.currentSrc || img.src;
    if (!src || !/\.png(\?|$)/i.test(src)) {
      img.dataset.retinaFit = 'done';
      return;
    }
    let p = dpiCache.get(src);
    if (!p) {
      p = isRetinaPng(src);
      dpiCache.set(src, p);
    }
    const retina = await p;
    if (retina && img.naturalWidth > 0) {
      img.style.width = `${Math.round(img.naturalWidth / 2)}px`;
    }
    img.dataset.retinaFit = retina ? 'halved' : 'done';
  };
  if (img.complete && img.naturalWidth > 0) {
    void apply();
  } else {
    img.addEventListener('load', () => void apply(), {once: true});
  }
}

function fitAll(): void {
  document
    .querySelectorAll<HTMLImageElement>('article img')
    .forEach((img) => fit(img));
}

export function onRouteDidUpdate(): void {
  if (typeof document === 'undefined') {
    return;
  }
  fitAll();
  // Late-mounted / hydration-swapped images.
  window.setTimeout(fitAll, 200);
}
