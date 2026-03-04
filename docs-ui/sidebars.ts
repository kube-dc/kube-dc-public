import type { SidebarsConfig } from '@docusaurus/plugin-content-docs';

// This runs in Node.js - Don't use client-side code here (browser APIs, JSX...)

/**
 * Creating a sidebar enables you to:
 - create an ordered group of docs
 - render a sidebar for each doc of that group
 - provide next/previous navigation

 The sidebars can be generated from the filesystem, or explicitly defined here.

 Create as many sidebars as you want.
 */
const sidebars: SidebarsConfig = {
  docsSidebar: [
    'index',
    'core-features',
    'quickstart-overview',
    'quickstart-hetzner',
    {
      type: 'category',
      label: 'Architecture',
      collapsed: false,
      items: [
        'architecture-overview',
        'architecture-multi-tenancy',
        'architecture-virtualization',
      ],
    },
    {
      type: 'category',
      label: 'Tutorials',
      collapsed: false,
      items: [
        'tutorials/tutorial-virtual-machines',
        'tutorials/tutorial-windows-vm',
        'tutorials/tutorial-kubeconfig',
        'tutorials/tutorial-service-exposure',
        'tutorials/tutorial-networking-external',
        'tutorials/tutorial-user-groups',
        'tutorials/tutorial-sso-google-auth',
      ],
    },
    'managing-os-images',
    'user-groups',
    'project_resources',
    'billing-plans-configuration',
    'internal-billing-integration',
    'deploy-rook-ceph-object-storage',
    'controller_diagram',
    'community-support',
    'roadmap',
    'product-backlog',
  ],
};

export default sidebars;
