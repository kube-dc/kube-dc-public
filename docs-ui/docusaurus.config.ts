import { themes as prismThemes } from 'prism-react-renderer';
import type { Config } from '@docusaurus/types';
import type * as Preset from '@docusaurus/preset-classic';
import stripGithubOnly from './src/remark/stripGithubOnly';

// This runs in Node.js - Don't use client-side code here (browser APIs, JSX...)

const config: Config = {
  title: 'Kube-DC Documentation',
  tagline: 'Build with Kube-DC or operate the platform',
  favicon: 'images/favicon.png',
  staticDirectories: ['static', '../static'],

  // Note: future.v4 flag can cause SSG issues with external docs
  // Removed for compatibility

  // Set the production url of your site here
  url: 'https://docs.kube-dc.com',
  // Set the /<baseUrl>/ pathname under which your site is served
  // For GitHub pages deployment, it is often '/<projectName>/'
  baseUrl: '/',

  // GitHub pages deployment config.
  organizationName: 'kube-dc', // GitHub org name (kube-dc/kube-dc-public)
  projectName: 'kube-dc-public', // GitHub repo name.

  onBrokenLinks: 'throw',
  onBrokenAnchors: 'throw',

  markdown: {
    hooks: {
      onBrokenMarkdownLinks: 'throw',
    },
    mdx1Compat: {
      comments: true,
      admonitions: true,
      headingIds: true,
    },
  },

  clientModules: [
    require.resolve('./src/clientModules/retinaImages.ts'),
    require.resolve('./src/clientModules/diagramViewport.ts'),
  ],

  themes: [
    [
      require.resolve('@easyops-cn/docusaurus-search-local'),
      {
        hashed: true,
        language: ['en'],
        highlightSearchTermsOnTargetPage: true,
        explicitSearchResultPath: true,
        indexDocs: true,
        indexBlog: false,
        indexPages: false,
        docsDir: ['../docs/cloud', '../docs/platform'],
        docsRouteBasePath: ['/cloud', '/platform'],
        searchResultLimits: 8,
        searchResultContextMaxLength: 50,
      },
    ],
  ],

  // Even if you don't use internationalization, you can use this field to set
  // useful metadata like html lang. For example, if your site is Chinese, you
  // may want to replace "en" with "zh-Hans".
  i18n: {
    defaultLocale: 'en',
    locales: ['en'],
  },

  presets: [
    [
      'classic',
      {
        docs: {
          path: '../docs/cloud',
          routeBasePath: '/cloud',
          sidebarPath: './sidebarsCloud.ts',
          beforeDefaultRemarkPlugins: [stripGithubOnly],
          editUrl: ({docPath}) => 'https://github.com/kube-dc/kube-dc-public/edit/main/docs/cloud/' + docPath,
        },
        blog: false,
        theme: {
          customCss: './src/css/custom.css',
        },
        gtag: {
          trackingID: 'G-XQ2DPWNG99',
          anonymizeIP: true,
        },
      } satisfies Preset.Options,
    ],
  ],

  headTags: [
    {
      tagName: 'script',
      attributes: {
        type: 'application/ld+json',
      },
      innerHTML: JSON.stringify({
        '@context': 'https://schema.org',
        '@type': 'SoftwareApplication',
        name: 'Kube-DC',
        applicationCategory: 'Cloud Infrastructure',
        operatingSystem: 'Kubernetes',
        description: 'An open-source platform that transforms Kubernetes into a full-featured data center with multi-tenancy, virtual machines, Managed Clusters, networking, storage, and billing.',
        url: 'https://docs.kube-dc.com',
        featureList: [
          'Multi-tenant organization management',
          'Virtual machine lifecycle management via KubeVirt',
          'Managed Clusters (Kamaji + Cluster API)',
          'Managed databases (PostgreSQL, MariaDB)',
          'Public and floating IP management (OVN)',
          'S3-compatible object storage (Rook Ceph)',
          'Block storage with persistent volumes',
          'Service-specific backup and recovery',
          'SSO integration (Keycloak OIDC)',
          'Hierarchical RBAC with project isolation',
          'Billing plans and resource quotas',
          'Web console and CLI access',
        ],
        sourceOrganization: {
          '@type': 'Organization',
          name: 'Shalb',
          url: 'https://shalb.com',
        },
      }),
    },
  ],

  plugins: [
    [
      '@docusaurus/plugin-content-docs',
      {
        id: 'platform',
        path: '../docs/platform',
        routeBasePath: '/platform',
        sidebarPath: './sidebarsPlatform.ts',
        beforeDefaultRemarkPlugins: [stripGithubOnly],
        editUrl: ({docPath}) => 'https://github.com/kube-dc/kube-dc-public/edit/main/docs/platform/' + docPath,
      },
    ],
    function llmsPlugin() {
      return {
        name: 'docusaurus-plugin-llms-txt',
        async postBuild({outDir}) {
          const fs = require('fs');
          const path = require('path');
          const cloudDir = path.resolve(__dirname, '../docs/cloud');
          const platformDir = path.resolve(__dirname, '../docs/platform');
          const siteUrl = 'https://docs.kube-dc.com';
          const description = 'Kube-DC is an open-source Kubernetes Data Center platform. It provides multi-tenancy, virtual machines (KubeVirt), Managed Clusters (Kamaji + Cluster API), managed databases, OVN networking with public/floating IPs, S3 object storage, block storage, backups, SSO, RBAC, and billing.';

          const header = `# Kube-DC Documentation\n\n> ${description}\n`;
          const indexLines: string[] = [header, `For the complete documentation in a single file, see: ${siteUrl}/llms-full.txt\n`];
          const fullSections: string[] = [header];

          function parseDocument(content: string): {title: string; body: string} {
            const frontmatter = content.match(/^\uFEFF?---\r?\n([\s\S]*?)\r?\n---(?:\r?\n|$)/);
            const frontmatterTitleMatch = frontmatter?.[1].match(/^title:\s*(.+?)\s*$/m);
            const frontmatterTitle = frontmatterTitleMatch?.[1]
              .trim()
              .replace(/^(['"])(.*)\1$/, '$2') ?? '';
            let body = frontmatter ? content.slice(frontmatter[0].length) : content;
            const contentTitle = body.match(/^#\s+(.+?)(?:\r?\n|$)/m);
            if (contentTitle) body = body.replace(contentTitle[0], '');
            return {
              title: frontmatterTitle || contentTitle?.[1].trim() || '',
              body: body.trim(),
            };
          }
          function extractDescription(content: string): string {
            const paragraphs = content
              .replace(/^---\s*\n[\s\S]*?\n---\s*\n/, '')
              .replace(/```[\s\S]*?```/g, '')
              .split(/\n\s*\n/)
              .map((paragraph) => paragraph
                .split('\n')
                .map((line) => line.trim())
                .join(' ')
                .trim());

            for (const paragraph of paragraphs) {
              if (!paragraph || /^(#|:::|import |export |<|!\[|\||---|>)/.test(paragraph)) continue;
              const plain = paragraph
                .replace(/!\[[^\]]*\]\([^)]*\)/g, '')
                .replace(/\[([^\]]+)\]\([^)]*\)/g, '$1')
                .replace(/[*_`]+/g, '')
                .replace(/\s+/g, ' ')
                .trim();
              if (!plain) continue;
              if (plain.length <= 160) return plain;
              const shortened = plain.slice(0, 157).replace(/\s+\S*$/, '');
              return `${shortened}...`;
            }
            return '';
          }
          function rewriteRelativeDocLinks(content: string, routeBase: string): string {
            return content.replace(
              /\[([^\]]+)\]\((?![a-z][a-z0-9+.-]*:|\/|#)(?:\.\/)?([^)\s]+)\.md(#[^)]+)?\)/gi,
              (_match: string, label: string, target: string, anchor = '') => {
                const slug = target === 'index' ? '' : target;
                const targetUrl = slug ? `${siteUrl}${routeBase}/${slug}` : `${siteUrl}${routeBase}/`;
                return `[${label}](${targetUrl}${anchor})`;
              },
            );
          }
          function shiftPageHeadings(content: string): string {
            let fenceCharacter = '';
            let fenceLength = 0;

            return content.split('\n').map((line) => {
              const fence = line.match(/^\s{0,3}(`{3,}|~{3,})/);
              if (fence) {
                const marker = fence[1];
                if (!fenceCharacter) {
                  fenceCharacter = marker[0];
                  fenceLength = marker.length;
                } else if (marker[0] === fenceCharacter && marker.length >= fenceLength && /^\s{0,3}(`+|~+)\s*$/.test(line)) {
                  fenceCharacter = '';
                  fenceLength = 0;
                }
                return line;
              }
              if (fenceCharacter) return line;

              return line.replace(/^(\s{0,3})(#{1,6})(?=[ \t])/, (_match, indent: string, hashes: string) =>
                `${indent}${'#'.repeat(Math.min(hashes.length + 2, 6))}`,
              );
            }).join('\n');
          }

          let documentCount = 0;
          for (const [label, dir, routeBase] of [['Cloud Guide', cloudDir, '/cloud'], ['Platform Docs', platformDir, '/platform']] as const) {
            indexLines.push(`\n## ${label}\n`);
            fullSections.push(`\n## ${label}\n`);

            const files = fs.readdirSync(dir).filter((f: string) => f.endsWith('.md')).sort();
            for (const file of files) {
              const source = fs.readFileSync(path.join(dir, file), 'utf-8');
              const websiteSource = source.replace(
                /<details\s+data-github-only>\s*<summary>[\s\S]*?<\/summary>[\s\S]*?<\/details>/g,
                '',
              );
              const document = parseDocument(websiteSource);
              const slug = file.replace('.md', '');
              const url = `${siteUrl}${routeBase}/${slug === 'index' ? '' : slug}`;
              const title = document.title || slug;
              const desc = extractDescription(document.body);
              indexLines.push(`- [${title}](${url})${desc ? ': ' + desc : ''}`);

              const cleaned = shiftPageHeadings(rewriteRelativeDocLinks(document.body
                .replace(/^import\s.*;\s*$/gm, '')
                .replace(/!\[([^\]]*)\]\([^)]*\)/g, (_match: string, alt: string) => alt ? `[Image: ${alt}]` : '[Image]')
                .replace(/<img\s[^>]*\/?\s*>/g, '[Image]')
                .trim(), routeBase));
              fullSections.push(`\n---\n\n### ${title}\n\nCanonical URL: ${url}\n\n${cleaned}\n`);
              documentCount += 1;
            }
          }

          const fullContent = fullSections.join('\n');
          const structuralContent = fullContent.replace(/^(`{3,}|~{3,})[^\n]*\n[\s\S]*?^\1\s*$/gm, '');
          const h1Count = structuralContent.match(/^#\s+/gm)?.length ?? 0;
          if (h1Count !== 1) {
            throw new Error(`llms-full.txt must contain exactly one H1; found ${h1Count}`);
          }
          const h3Count = structuralContent.match(/^###\s+/gm)?.length ?? 0;
          if (h3Count !== documentCount) {
            throw new Error(`llms-full.txt must contain one H3 per document; expected ${documentCount}, found ${h3Count}`);
          }
          if (/\]\((?![a-z][a-z0-9+.-]*:|\/|#)[^)]*\.md(?:#[^)]*)?\)/i.test(fullContent)) {
            throw new Error('llms-full.txt contains a relative Markdown document link');
          }

          fs.writeFileSync(path.join(outDir, 'llms.txt'), indexLines.join('\n') + '\n');
          fs.writeFileSync(path.join(outDir, 'llms-full.txt'), fullContent);
          console.log(`[llms-txt] Generated llms.txt and llms-full.txt`);
        },
      };
    },
  ],

  themeConfig: {
    // Replace with your project's social card
    image: 'img/kube-dc-social-card.jpg',
    metadata: [
      {property: 'og:site_name', content: 'Kube-DC'},
      {name: 'description', content: 'Kube-DC documentation for Organizations, Projects, virtual machines, Managed Clusters, networking, storage, and billing.'},
      {name: 'keywords', content: 'Kube-DC, Kubernetes, data center, Organizations, Projects, KubeVirt, virtual machines, Managed Clusters, Kamaji, Cluster API, OVN, networking, object storage, billing, RBAC, SSO'},
      {property: 'og:type', content: 'website'},
      {property: 'og:description', content: 'Documentation for Kube-DC: deploy applications and VMs in Projects, create Managed Clusters, and operate the platform.'},
    ],
    colorMode: {
      defaultMode: 'light',
      respectPrefersColorScheme: false,
    },
    navbar: {
      title: 'Kube-DC',
      logo: {
        alt: 'Kube-DC Logo',
        src: 'img/kube-dc-logo.png',
      },
      items: [
        {
          type: 'docSidebar',
          sidebarId: 'cloudSidebar',
          position: 'left',
          label: 'Cloud Guide',
        },
        {
          type: 'docSidebar',
          sidebarId: 'platformSidebar',
          docsPluginId: 'platform',
          position: 'left',
          label: 'Platform Docs',
        },
        {
          href: 'https://github.com/kube-dc/kube-dc-public',
          label: 'GitHub',
          position: 'right',
        },
      ],
    },
    footer: {
      style: 'dark',
      links: [
        {
          title: 'Cloud Guide',
          items: [
            {
              label: 'Getting Started',
              to: '/cloud',
            },
            {
              label: 'Virtual Machines',
              to: '/cloud/creating-vm',
            },
            {
              label: 'Networking',
              to: '/cloud/public-floating-ips',
            },
          ],
        },
        {
          title: 'Platform',
          items: [
            {
              label: 'Installation',
              to: '/platform/installation-overview',
            },
            {
              label: 'Architecture',
              to: '/platform/architecture-overview',
            },
            {
              label: 'Operations',
              to: '/platform/billing-plans-configuration',
            },
          ],
        },
        {
          title: 'Community',
          items: [
            {
              label: 'GitHub',
              href: 'https://github.com/kube-dc/kube-dc-public',
            },
            {
              label: 'Slack',
              href: 'https://join.slack.com/t/kube-dc/shared_invite/zt-31mr5c6ci-W3kYQ7qGDULlGQ5QJjsxmA',
            },
            {
              label: 'Issues',
              href: 'https://github.com/kube-dc/kube-dc-public/issues',
            },
          ],
        },
        {
          title: 'More',
          items: [
            {
              label: 'Roadmap',
              to: '/platform/roadmap',
            },
            {
              label: 'Shalb',
              href: 'https://shalb.com',
            },
            {
              label: 'Contact',
              href: 'https://shalb.com/contact',
            },
          ],
        },
      ],
      copyright: `Copyright © ${new Date().getFullYear()} Kube-DC Project.`,
    },
    prism: {
      theme: prismThemes.github,
      darkTheme: prismThemes.dracula,
      additionalLanguages: ['bash', 'yaml', 'json', 'typescript', 'go'],
    },
  } satisfies Preset.ThemeConfig,
};

export default config;
