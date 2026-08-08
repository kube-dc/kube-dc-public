import type {SidebarsConfig} from '@docusaurus/plugin-content-docs';

const sidebars: SidebarsConfig = {
  platformSidebar: [
    'index',
    {
      type: 'category',
      label: 'Installation',
      collapsed: false,
      items: [
        'quickstart',
        'installation-overview',
        'installation-guide',
      ],
    },
    {
      type: 'category',
      label: 'Architecture',
      collapsed: true,
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
      label: 'Networking',
      collapsed: true,
      items: [
        'networking-external',
        'tenant-vlan-attachment',
        'deploy-metallb-ha',
      ],
    },
    {
      type: 'category',
      label: 'Security & Identity',
      collapsed: true,
      items: [
        'security-model',
        'sso-google-auth',
        'certificates',
        'private-ca-enterprise-install',
        'smtp-gmail-app-password',
      ],
    },
    {
      type: 'category',
      label: 'Virtual Machines & Images',
      collapsed: true,
      items: [
        'managing-os-images',
        'os-image-operations',
        'vm-storage-tiers',
        'windows-vm-setup',
      ],
    },
    {
      type: 'category',
      label: 'GPU',
      collapsed: true,
      items: [
        'gpu-capacity-reservations',
        'gpu-node-mode-transitions',
        'gpu-node-upgrades',
        'gpu-supply-chain',
        'gpu-threat-model',
      ],
    },
    {
      type: 'category',
      label: 'Managed Clusters',
      collapsed: true,
      items: [
        'managed-k8s-etcd-backup-restore',
        'managed-k8s-etcd-encryption',
      ],
    },
    {
      type: 'category',
      label: 'Storage',
      collapsed: true,
      items: [
        'deploy-rook-ceph-object-storage',
      ],
    },
    {
      type: 'category',
      label: 'Day-2 Operations',
      collapsed: true,
      items: [
        'observability',
        'upgrading-management-cluster',
        'billing-plans-configuration',
      ],
    },
    {
      type: 'category',
      label: 'Bare Metal',
      collapsed: true,
      items: [
        'deploy-metal3-bare-metal-workers',
      ],
    },
    {
      type: 'category',
      label: 'Cluster Operator CLI',
      collapsed: true,
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
      label: 'Reference',
      collapsed: true,
      items: [
        'project-resources',
      ],
    },
    'roadmap',
  ],
};

export default sidebars;
