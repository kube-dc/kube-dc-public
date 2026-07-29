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
        'core-concepts',
        {
          type: 'doc',
          id: 'core-features',
          label: 'Platform Capabilities',
        },
        'sign-up-login',
        'dashboard-overview',
        'first-project',
        'ai-ide-integration',
      ],
    },
    {
      type: 'category',
      label: 'Projects',
      collapsed: false,
      items: [
        {
          type: 'doc',
          id: 'kubernetes-projects',
          label: 'Overview',
        },
        {
          type: 'doc',
          id: 'cli-kubeconfig',
          label: 'Connect with a Project Kubeconfig',
        },
        {
          type: 'doc',
          id: 'security-restrictions',
          label: 'Project Compatibility and Restrictions',
        },
      ],
    },
    {
      type: 'category',
      label: 'Deploying Applications',
      collapsed: false,
      items: [
        {
          type: 'doc',
          id: 'deploy-first-app',
          label: 'Deploy Your First Application',
        },
        {
          type: 'doc',
          id: 'deploy-wordpress-stack',
          label: 'Deploy a Full WordPress Stack',
        },
        {
          type: 'doc',
          id: 'gpu-shared-workloads',
          label: 'Run Shared GPU Workloads',
        },
        'gitops',
      ],
    },
    {
      type: 'category',
      label: 'Managed Kubernetes Clusters',
      collapsed: true,
      items: [
        'provisioning-cluster',
        'cluster-management',
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
        'gpu-vm-guests',
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
      label: 'Security & Identity',
      collapsed: true,
      items: [
        'secrets-manager',
        'kms',
        'certificate-manager',
        'database-credentials',
      ],
    },
    {
      type: 'category',
      label: 'Account Management & Billing',
      collapsed: true,
      items: [
        'team-management',
        'billing-usage',
        'scaling-performance',
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
