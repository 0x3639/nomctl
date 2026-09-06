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
          showLastUpdateTime: false,
        },
        blog: false,
        theme: {customCss: './src/css/custom.css'},
      } satisfies Preset.Options,
    ],
  ],

  themeConfig: {
    image: 'img/logo.png',
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
