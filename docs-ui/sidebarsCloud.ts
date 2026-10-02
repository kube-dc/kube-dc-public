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
          type: 'category',
          label: 'Common tasks',
          collapsed: true,
          items: [
            {type: 'doc', id: 'managed-services-plans', label: 'Choose a class and plan'},
            {type: 'doc', id: 'managed-services-console', label: 'Use the console'},
            {type: 'doc', id: 'managed-services-connect', label: 'Connect applications'},
            {type: 'doc', id: 'managed-services-credentials', label: 'Rotate credentials'},
            {type: 'doc', id: 'managed-services-operations', label: 'Request an operation'},
            {type: 'doc', id: 'managed-services-backup-restore', label: 'Back up and restore'},
            {type: 'doc', id: 'managed-services-status', label: 'Check status and troubleshoot'},
            {type: 'doc', id: 'managed-services-status-deletion', label: 'Delete a service'},
          ],
        },
        {
          type: 'category',
          label: 'PostgreSQL',
          collapsed: true,
          link: {type: 'doc', id: 'postgresql'},
          items: [
            {type: 'doc', id: 'postgresql-create', label: 'Create a service'},
            {type: 'doc', id: 'postgresql-connect', label: 'Connect applications'},
            {type: 'doc', id: 'postgresql-credentials', label: 'Credentials and SQL logins'},
            {type: 'doc', id: 'postgresql-operations', label: 'Scale, configure, and upgrade'},
            {type: 'doc', id: 'postgresql-backup-restore', label: 'Backups and recovery'},
            {type: 'doc', id: 'postgresql-external-access', label: 'External access'},
            {type: 'doc', id: 'postgresql-deletion', label: 'Deletion and retained resources'},
          ],
        },
        {type: 'doc', id: 'managed-services-mysql-mariadb', label: 'MySQL and MariaDB'},
        {type: 'doc', id: 'managed-services-valkey', label: 'Valkey'},
        {type: 'doc', id: 'managed-services-clickhouse', label: 'ClickHouse'},
        {type: 'doc', id: 'managed-services-kafka', label: 'Kafka'},
        {type: 'doc', id: 'managed-services-github-runners', label: 'GitHub Actions runners'},
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
