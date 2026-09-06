import type {SidebarsConfig} from '@docusaurus/plugin-content-docs';

const sidebars: SidebarsConfig = {
  docs: [
    'index',
    'getting-started',
    {
      type: 'category',
      label: 'Guide',
      collapsed: false,
      items: ['guide/deploy', 'guide/service', 'guide/backups', 'guide/analytics', 'guide/menu'],
    },
    {
      type: 'category',
      label: 'Alerts',
      collapsed: false,
      items: ['alerts/overview', 'alerts/pairing', 'alerts/rules', 'alerts/telegram-commands', 'alerts/relay'],
    },
    {
      type: 'category',
      label: 'Troubleshooting',
      collapsed: false,
      items: ['troubleshooting/first-look', 'troubleshooting/signatures', 'troubleshooting/support-bundle'],
    },
    {
      type: 'category',
      label: 'Reference',
      collapsed: false,
      items: ['reference/commands', 'reference/configuration', 'reference/differences'],
    },
    'roadmap',
  ],
};

export default sidebars;
