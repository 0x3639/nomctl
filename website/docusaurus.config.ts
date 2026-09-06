import {themes as prismThemes} from 'prism-react-renderer';
import type {Config} from '@docusaurus/types';
import type * as Preset from '@docusaurus/preset-classic';

const config: Config = {
  title: 'nomctl',
  tagline: 'Deploy, operate and watch Zenon Network nodes from one binary.',
  favicon: 'img/logo.png',

  url: 'https://nomctl.0x3639.com',
  baseUrl: '/',
  trailingSlash: false,

  organizationName: '0x3639',
  projectName: 'nomctl',

  onBrokenLinks: 'throw',
  markdown: {hooks: {onBrokenMarkdownLinks: 'throw'}},

  i18n: {defaultLocale: 'en', locales: ['en']},

  presets: [
    [
      'classic',
      {
        docs: {
          sidebarPath: './sidebars.ts',
          routeBasePath: '/',
          editUrl: 'https://github.com/0x3639/nomctl/tree/main/website/',
          showLastUpdateTime: true,
        },
        sitemap: {
          changefreq: 'weekly',
          priority: 0.6,
          ignorePatterns: ['/tags/**'],
          filename: 'sitemap.xml',
        },
        blog: false,
        theme: {customCss: './src/css/custom.css'},
      } satisfies Preset.Options,
    ],
  ],

  plugins: ['./plugins/llms-txt.js'],

  headTags: [
    {tagName: 'meta', attributes: {property: 'og:type', content: 'website'}},
    {tagName: 'meta', attributes: {property: 'og:site_name', content: 'nomctl'}},
    {tagName: 'meta', attributes: {property: 'og:image:width', content: '1200'}},
    {tagName: 'meta', attributes: {property: 'og:image:height', content: '630'}},
    {tagName: 'meta', attributes: {name: 'twitter:card', content: 'summary_large_image'}},
    {tagName: 'link', attributes: {rel: 'alternate', type: 'text/plain', href: 'https://nomctl.0x3639.com/llms.txt', title: 'llms.txt'}},
  ],

  themes: [
    [
      '@easyops-cn/docusaurus-search-local',
      {
        // Local index built at deploy time; queries never leave the browser.
        hashed: true,
        docsRouteBasePath: '/',
        indexBlog: false,
        highlightSearchTermsOnTargetPage: true,
        searchResultLimits: 8,
        searchBarShortcutHint: true,
      },
    ],
  ],

  themeConfig: {
    image: 'img/og-card.png',
    metadata: [
      {name: 'description', content: 'Deploy, back up, watch and get Telegram alerts for Zenon Network (NoM) nodes from one static binary. Docs, command reference and troubleshooting.'},
      {name: 'keywords', content: 'zenon, network of momentum, znnd, pillar, node, nomctl, deploy, backup, alerts, telegram'},
    ],
    colorMode: {defaultMode: 'dark', respectPrefersColorScheme: false},
    navbar: {
      title: 'nomctl',
      logo: {alt: 'nomctl', src: 'img/logo.png'},
      items: [
        {type: 'docSidebar', sidebarId: 'docs', position: 'left', label: 'Docs'},
        {to: '/alerts/overview', label: 'Alerts', position: 'left'},
        {to: '/troubleshooting/first-look', label: 'Troubleshooting', position: 'left'},
        {href: 'https://github.com/0x3639/nomctl/releases', label: 'Releases', position: 'right'},
        {href: 'https://github.com/0x3639/nomctl', label: 'GitHub', position: 'right'},
      ],
    },
    footer: {
      style: 'dark',
      links: [
        {
          title: 'Use',
          items: [
            {label: 'Overview', to: '/overview'},
            {label: 'Getting started', to: '/getting-started'},
            {label: 'Commands', to: '/reference/commands'},
            {label: 'Configuration', to: '/reference/configuration'},
          ],
        },
        {
          title: 'Operate',
          items: [
            {label: 'Alerts', to: '/alerts/overview'},
            {label: 'Troubleshooting', to: '/troubleshooting/first-look'},
            {label: 'Support bundle', to: '/troubleshooting/support-bundle'},
          ],
        },
        {
          title: 'Project',
          items: [
            {label: 'GitHub', href: 'https://github.com/0x3639/nomctl'},
            {label: 'Releases', href: 'https://github.com/0x3639/nomctl/releases'},
            {label: 'Roadmap', to: '/roadmap'},
          ],
        },
      ],
      copyright: `nomctl is GPL-3.0, a Go port of hypercore-one/deployment. Zenon Network of Momentum.`,
    },
    prism: {
      theme: prismThemes.github,
      darkTheme: prismThemes.dracula,
      additionalLanguages: ['bash', 'json', 'ini', 'go'],
    },
  } satisfies Preset.ThemeConfig,
};

export default config;
