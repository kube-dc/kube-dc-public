import { themes as prismThemes } from 'prism-react-renderer';
import type { Config } from '@docusaurus/types';
import type * as Preset from '@docusaurus/preset-classic';

// This runs in Node.js - Don't use client-side code here (browser APIs, JSX...)

const config: Config = {
  title: 'Kube-DC Documentation',
  tagline: 'Kubernetes Data Center Platform',
  favicon: 'images/favicon.png',

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

  onBrokenLinks: 'warn',

  markdown: {
    mermaid: true,
    hooks: {
      onBrokenMarkdownLinks: 'warn',
    },
    mdx1Compat: {
      comments: true,
      admonitions: true,
      headingIds: true,
    },
  },

  themes: [
    '@docusaurus/theme-mermaid',
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
        docsRouteBasePath: ['/cloud','/platform'],
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

  plugins: [
    [
      '@docusaurus/plugin-content-docs',
      {
        id: 'platform',
        path: '../docs/platform',
        routeBasePath: '/platform',
        sidebarPath: './sidebarsPlatform.ts',
      },
    ],
  ],

  themeConfig: {
    // Replace with your project's social card
    image: 'img/kube-dc-social-card.jpg',
    metadata: [
      {property: 'og:site_name', content: 'Kube-DC'},
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
      copyright: `Copyright © ${new Date().getFullYear()} Kube-DC Project. Built with ❤️ by <a href="https://shalb.com" target="_blank" rel="noopener noreferrer">Shalb</a>.`,
    },
    prism: {
      theme: prismThemes.github,
      darkTheme: prismThemes.dracula,
      additionalLanguages: ['bash', 'yaml', 'json', 'typescript', 'go'],
    },
    mermaid: {
      theme: { light: 'default', dark: 'dark' },
      options: {
        themeVariables: {
          primaryColor: '#e3f2fd',
          primaryTextColor: '#151515',
          primaryBorderColor: '#1976d2',
          lineColor: '#1976d2',
          fontSize: '16px',
        },
      },
    },
  } satisfies Preset.ThemeConfig,
};

export default config;
