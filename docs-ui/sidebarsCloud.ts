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
      ],
    },
    {
      type: 'category',
      label: 'Projects',
      collapsed: true,
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
      label: 'Applications & Automation',
      collapsed: true,
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
        'ai-ide-integration',
      ],
    },
    {
      type: 'category',
      label: 'Managed Clusters',
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
        'routed-networks',
        'datacenter-vlans',
        'service-exposure',
      ],
    },
    {
      type: 'category',
      label: 'Managed Services',
      collapsed: true,
      items: [
        {
          type: 'doc',
          id: 'managed-services',
          label: 'Overview',
        },
        {
          type: 'doc',
          id: 'managed-services-plans',
          label: 'Classes and Plans',
        },
        {
          type: 'doc',
          id: 'postgresql-create',
          label: 'Create a PostgreSQL Service',
        },
        {
          type: 'doc',
          id: 'postgresql-connect',
          label: 'Connect Applications',
        },
        {
          type: 'doc',
          id: 'postgresql-credentials',
          label: 'Credentials and Rotation',
        },
        {
          type: 'doc',
          id: 'postgresql-operations',
          label: 'Day-2 Operations',
        },
        {
          type: 'doc',
          id: 'postgresql-backup-restore',
          label: 'Backups and Restore',
        },
        {
          type: 'doc',
          id: 'postgresql-external-access',
          label: 'External Access',
        },
        {
          type: 'doc',
          id: 'managed-services-status-deletion',
          label: 'Status and Deletion',
        },
        {
          type: 'doc',
          id: 'managed-services-from-kdcdatabase',
          label: 'Coming from KdcDatabase',
        },
        {
          type: 'doc',
          id: 'managed-services-other-families',
          label: 'Valkey',
        },
      ],
    },
    {
      type: 'category',
      label: 'Managed Databases',
      collapsed: true,
      items: [
        'managed-databases',
        'database-credentials',
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
