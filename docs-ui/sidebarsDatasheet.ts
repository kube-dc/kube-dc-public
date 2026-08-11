import type {SidebarsConfig} from '@docusaurus/plugin-content-docs';

const sidebars: SidebarsConfig = {
  datasheetSidebar: [
    {
      type: 'doc',
      id: 'draft-artifact-a-datasheet',
      label: 'Overview',
    },
    {
      type: 'category',
      label: 'Capabilities',
      collapsed: false,
      items: [
        'function-managed-kubernetes',
        'function-virtual-machines',
        'function-managed-databases',
        'function-networking',
        'function-storage',
        'function-security',
        'function-observability',
        'function-gpu',
      ],
    },
  ],
};

export default sidebars;
