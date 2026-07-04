import type {SidebarsConfig} from '@docusaurus/plugin-content-docs';

const sidebars: SidebarsConfig = {
  platformSidebar: [
    'index',
    {
      type: 'category',
      label: 'Installation',
      collapsed: false,
      items: [
        'installation-overview',
        'installation-guide',
      ],
    },
    {
      type: 'category',
      label: 'Architecture',
      collapsed: false,
      items: [
        'architecture-overview',
        'architecture-networking',
        'architecture-multi-tenancy',
        'architecture-virtualization',
        'internal-platform-endpoints',
        'controller-diagram',
      ],
    },
    {
      type: 'category',
      label: 'Operations',
      collapsed: false,
      items: [
        'security-model',
        'managing-os-images',
        'project-resources',
        'windows-vm-setup',
        'sso-google-auth',
        'networking-external',
        'observability',
        'upgrading-management-cluster',
        'billing-plans-configuration',
        'smtp-gmail-app-password',
      ],
    },
    {
      type: 'category',
      label: 'Managed Kubernetes',
      collapsed: true,
      items: [
        'managed-k8s-etcd-backup-restore',
        'managed-k8s-etcd-encryption',
      ],
    },
    {
      type: 'category',
      label: 'Cluster Operator CLI',
      collapsed: false,
      items: [
        'cluster-cli-overview',
        'cluster-cli-fleet',
        'cluster-cli-admin-login',
        'cluster-cli-context-manager',
        'cluster-cli-break-glass',
        'cluster-cli-troubleshooting',
      ],
    },
    {
      type: 'category',
      label: 'Infrastructure Add-ons',
      collapsed: true,
      items: [
        'deploy-metallb-ha',
        'deploy-metal3-bare-metal-workers',
        'deploy-rook-ceph-object-storage',
      ],
    },
    'roadmap',
  ],
};

export default sidebars;
