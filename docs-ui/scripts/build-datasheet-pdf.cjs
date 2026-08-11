const fs = require('fs');
const os = require('os');
const path = require('path');
const {spawn} = require('child_process');
const {chromium} = require('playwright');

const docsUiRoot = path.resolve(__dirname, '..');
const buildDir = path.join(docsUiRoot, 'build');
const outputDir = process.env.DATASHEET_PDF_DIR
  || path.join(os.tmpdir(), 'kube-dc-datasheet');
const port = Number(process.env.DATASHEET_PDF_PORT || '4174');
const origin = `http://127.0.0.1:${port}`;
const formats = (process.env.DATASHEET_PDF_FORMATS || 'A4,Letter')
  .split(',')
  .map((value) => value.trim())
  .filter(Boolean);

const routes = [
  '/datasheet/',
  '/datasheet/managed-kubernetes/',
  '/datasheet/virtual-machines/',
  '/datasheet/managed-databases/',
  '/datasheet/networking/',
  '/datasheet/storage/',
  '/datasheet/security/',
  '/datasheet/observability/',
  '/datasheet/gpu/',
];

function waitForServer(url, timeoutMs = 30_000) {
  const started = Date.now();
  return new Promise((resolve, reject) => {
    const poll = async () => {
      try {
        const response = await fetch(url);
        if (response.ok) return resolve();
      } catch (_) {
        // The static server is still starting.
      }
      if (Date.now() - started > timeoutMs) {
        return reject(new Error(`Timed out waiting for ${url}`));
      }
      setTimeout(poll, 250);
    };
    poll();
  });
}

async function main() {
  if (!fs.existsSync(path.join(buildDir, 'index.html'))) {
    throw new Error('Docusaurus build is missing. Run `npm run build` first.');
  }
  fs.mkdirSync(outputDir, {recursive: true});

  const docusaurus = path.join(
    docsUiRoot,
    'node_modules',
    '.bin',
    process.platform === 'win32' ? 'docusaurus.cmd' : 'docusaurus',
  );
  const server = spawn(
    docusaurus,
    ['serve', '--host', '127.0.0.1', '--port', String(port), '--no-open'],
    {cwd: docsUiRoot, stdio: ['ignore', 'pipe', 'pipe']},
  );
  server.stdout.on('data', (chunk) => process.stdout.write(chunk));
  server.stderr.on('data', (chunk) => process.stderr.write(chunk));

  let browser;
  try {
    await waitForServer(`${origin}/datasheet/`);
    browser = await chromium.launch({headless: true});
    const page = await browser.newPage({viewport: {width: 1440, height: 1000}});
    await page.goto(`${origin}/datasheet/`, {waitUntil: 'networkidle'});
    console.log(`[datasheet-pdf] composing ${routes.length} rendered routes`);

    const composition = await page.evaluate(async ({routes: documentRoutes}) => {
      document.documentElement.dataset.theme = 'light';
      const pages = [];
      for (const route of documentRoutes) {
        const response = await fetch(route);
        if (!response.ok) throw new Error(`Cannot load ${route}: ${response.status}`);
        const source = await response.text();
        const parsed = new DOMParser().parseFromString(source, 'text/html');
        const article = parsed.querySelector('article .theme-doc-markdown');
        if (!article) throw new Error(`No document article found at ${route}`);
        const section = document.createElement('section');
        section.className = 'datasheet-print-document';
        section.dataset.sourceRoute = route;
        section.innerHTML = article.innerHTML;
        section.dataset.sourceTitle = article.querySelector('h1')?.textContent?.trim()
          || route;
        pages.push(section);
      }

      const main = document.createElement('main');
      main.className = 'datasheet-print-root';
      pages.forEach((section) => main.appendChild(section));
      document.body.replaceChildren(main);

      const images = Array.from(document.images);
      images.forEach((image) => {
        image.loading = 'eager';
        const source = image.getAttribute('src');
        if (source) image.src = new URL(source, document.baseURI).href;
      });
      await document.fonts.ready;
      await Promise.all(
        images.map(async (image) => {
          if (!image.complete) {
            await Promise.race([
              image.decode(),
              new Promise((_, reject) => setTimeout(
                () => reject(new Error(`Timed out loading ${image.currentSrc || image.src}`)),
                15_000,
              )),
            ]);
          }
          if (!image.naturalWidth || !image.naturalHeight) {
            throw new Error(`Cannot decode ${image.currentSrc || image.src}`);
          }
        }),
      );
      return {
        markup: main.outerHTML,
        sectionCount: document.querySelectorAll('.datasheet-print-document').length,
        stylesheets: Array.from(document.querySelectorAll(
          'link[rel="stylesheet"]',
        )).map((link) => link.href),
        titles: Array.from(document.querySelectorAll(
          '.datasheet-print-document',
        )).map((section) => section.dataset.sourceTitle || ''),
      };
    }, {routes});
    if (composition.sectionCount !== routes.length
        || composition.titles.length !== routes.length
        || composition.titles.some((title) => !title)) {
      throw new Error(`Incomplete PDF composition: ${JSON.stringify(composition)}`);
    }
    console.log(
      `[datasheet-pdf] rendered routes and local images are ready: ${composition.titles.join(' | ')}`,
    );

    const stylesheetLinks = composition.stylesheets
      .map((href) => `<link rel="stylesheet" href="${href}">`)
      .join('');
    await page.setContent(
      '<!doctype html><html data-theme="light"><head>'
        + `<base href="${origin}/">`
        + '<meta charset="utf-8">'
        + stylesheetLinks
        + '</head><body>'
        + composition.markup
        + '</body></html>',
      {waitUntil: 'networkidle'},
    );
    await page.emulateMedia({media: 'print', colorScheme: 'light'});
    await page.evaluate(async () => {
      await document.fonts.ready;
      await Promise.all(Array.from(document.images).map(async (image) => {
        image.loading = 'eager';
        if (!image.complete) await image.decode();
        if (!image.naturalWidth || !image.naturalHeight) {
          throw new Error(`Cannot decode ${image.currentSrc || image.src}`);
        }
      }));
    });

    await page.addStyleTag({content: `
      @page { margin: 14mm 13mm 16mm; }
      html, body { background: #fff !important; color: #151515 !important; }
      body { display: block !important; min-height: 0 !important; margin: 0 !important; font-size: 10pt; }
      .datasheet-print-root { display: block !important; max-width: 184mm; margin: 0 auto; }
      .datasheet-print-document { break-before: page; }
      .datasheet-print-document:first-child { break-before: auto; }
      .datasheet-print-document h1 { font-size: 25pt; margin-top: 0; }
      .datasheet-print-document h2 { break-after: avoid; font-size: 17pt; }
      .datasheet-print-document h3 { break-after: avoid; font-size: 13pt; }
      .datasheet-print-document p,
      .datasheet-print-document li { orphans: 3; widows: 3; }
      .datasheet-print-document table,
      .datasheet-print-document figure,
      .datasheet-print-document [data-diagram-explainer='true'] { break-inside: avoid; }
      .datasheet-print-document [data-diagram-scroll='true'] { overflow: visible !important; }
      .datasheet-print-document [data-diagram-scroll='true'] > svg {
        min-width: 0 !important;
        width: 100% !important;
        height: auto !important;
      }
      .datasheet-print-document pre { white-space: pre-wrap !important; }
      a { color: #0645ad !important; text-decoration: none !important; }
    `});
    const printSections = await page.locator('.datasheet-print-document').count();
    if (printSections !== routes.length) {
      throw new Error(`Print document contains ${printSections} of ${routes.length} routes`);
    }
    const printLayout = await page.evaluate(() => ({
      bodyHeight: document.body.scrollHeight,
      sections: Array.from(document.querySelectorAll(
        '.datasheet-print-document',
      )).map((section) => {
        const bounds = section.getBoundingClientRect();
        return {
          display: getComputedStyle(section).display,
          height: Math.round(bounds.height),
          title: section.querySelector('h1')?.textContent?.trim() || '',
          top: Math.round(bounds.top),
        };
      }),
    }));
    console.log(`[datasheet-pdf] print layout ${JSON.stringify(printLayout)}`);

    for (const format of formats) {
      const normalized = format.toLowerCase() === 'letter' ? 'Letter' : 'A4';
      const output = path.join(
        outputDir,
        `kube-dc-platform-datasheet-${normalized.toLowerCase()}.pdf`,
      );
      await page.pdf({
        path: output,
        format: normalized,
        printBackground: true,
        displayHeaderFooter: true,
        headerTemplate: '<div style="font:8px sans-serif;color:#666;width:100%;padding:0 13mm">Kube-DC Platform Datasheet</div>',
        footerTemplate: '<div style="font:8px sans-serif;color:#666;width:100%;padding:0 13mm;text-align:right"><span class="pageNumber"></span> / <span class="totalPages"></span></div>',
        margin: {top: '17mm', right: '13mm', bottom: '18mm', left: '13mm'},
      });
      console.log(`[datasheet-pdf] ${output}`);
    }
  } finally {
    if (browser) await browser.close();
    server.kill('SIGTERM');
  }
}

main().catch((error) => {
  console.error(error);
  process.exitCode = 1;
});
