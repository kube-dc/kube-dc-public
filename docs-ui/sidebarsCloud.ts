import type {SidebarsConfig} from '@docusaurus/plugin-content-docs';

const sidebars: SidebarsConfig = {
  cloudSidebar: [
    'index',
    {
      type: 'category',
      label: 'Getting Started',
      collapsed: false,
      items: [
        'what-is-kube-dc',
        'sign-up-login',
        'dashboard-overview',
        'core-concepts',
        'first-project',
        'cli-kubeconfig',
      ],
    },
    {
      type: 'category',
      label: 'Quick Start',
      collapsed: true,
      items: [
        'deploy-first-app',
      ],
    },
    {
      type: 'category',
      label: 'Virtual Machines',
      collapsed: true,
      items: [
        'creating-vm',
        'connecting-vm',
        'vm-lifecycle',
      ],
    },
    {
      type: 'category',
      label: 'Networking',
      collapsed: true,
      items: [
        'networking-overview',
        'public-floating-ips',
        'private-networking',
        'service-exposure',
      ],
    },
    {
      type: 'category',
      label: 'Managed Kubernetes',
      collapsed: true,
      items: [
        'provisioning-cluster',
        'cluster-management',
      ],
    },
    {
      type: 'category',
      label: 'Managed Databases',
      collapsed: true,
      items: [
        'managed-databases',
      ],
    },
    {
      type: 'category',
      label: 'Storage & Data',
      collapsed: true,
      items: [
        'block-storage',
        'object-storage',
        'backups-snapshots',
      ],
    },
    {
      type: 'category',
      label: 'Account Management & Billing',
      collapsed: true,
      items: [
        'team-management',
        'billing-usage',
      ],
    },
    {
      type: 'category',
      label: 'Advanced Topics',
      collapsed: true,
      items: [
        'security-restrictions',
        'ai-ide-integration',
        'gitops',
      ],
    },
    {
      type: 'category',
      label: 'Examples & Tutorials',
      collapsed: true,
      items: [
        'examples',
        'tutorials',
      ],
    },
    'community-support',
  ],
};

export default sidebars;
