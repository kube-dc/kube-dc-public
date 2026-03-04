# Kube-DC Product Backlog

This document outlines the current product backlog for the Kube-DC project, organized by epics and features.

## 🚀 Active Epics

### [Epic] Windows Support
**Status**: Done

### [Epic] VMware Migration
**Status**: Research Phase

- 🔍 **VMware vSphere migration research (CDI, vjailbreak)**
  - Investigate migration tools and methodologies
  - Evaluate CDI (Containerized Data Importer) for VM migration
  - Research vjailbreak and other migration utilities

### [Epic] Organization Management
**Status**: Planning

- 📋 **UI for project and roles**
  - Implement project management interface 
  - Role-based access control UI components
  - User and group management interfaces

- 🎨 **Customize Login Page**
  - Branding and customization options
  - Organization-specific login themes

### [Epic] UI Implementation on Backend
**Status**: Multiple Items in Progress

- ❌ **UI Clone Disks - Not working**
  - Fix disk cloning functionality in UI
  - Ensure proper CDI integration

- 📋 **UI Create VM from PVC/DataVolume**
  - Interface for VM creation from existing storage
  - DataVolume selection and configuration

- 🌐 **UI Add VM Static IP**
  - Static IP assignment interface
  - Network configuration management

- 🌐 **UI Add VM FIP (Floating IP)**
  - Floating IP assignment and management
  - Integration with FIP CRD resources

- ⚖️ **UI Add Load Balancer Setup**
  - Load balancer configuration interface
  - Service exposure management

- 🔄 **UI Migrate/Clone VM (rook/ceph)**
  - VM migration interface with Rook/Ceph backend
  - Live migration capabilities

- 👥 **UI VM Groups**
  - VM grouping and management features
  - Bulk operations on VM groups

### [Epic] Installer
**Status**: Enhancement Phase

- 🗄️ **Postgres DB for Keycloak and Billing to be dedicated in installer stack**
  - Separate PostgreSQL deployment for Keycloak
  - Database isolation and management

- ⚡ **Simplify Installer**
  - Streamline installation process
  - Reduce complexity and dependencies

- 🖥️ **Single host install**
  - Support for single-node deployments
  - All-in-one installation option

- 🔧 **Test on VMware vSX**
  - Validation on VMware infrastructure
  - Compatibility testing and documentation

- 🔐 **Fix hardcoded passwords in loki.yaml**
  - Security improvement for Loki configuration
  - Dynamic password generation

### [Epic] Licensing
**Status**: Planning

- 📄 **License for Node Limits per installation**
  - Implement licensing system
  - Node-based licensing model
  - License validation and enforcement

### [Epic] Billing & Quota Management
**Status**: In Progress

- ✅ **Organization quota enforcement (HRQ + LimitRange + EIP)**
  - Plans defined in `billing-plans` ConfigMap
  - HierarchicalResourceQuota auto-created per org
  - LimitRange propagated via HNC to project namespaces
  - EIP quota per plan, S3 object storage quota via Rook-Ceph
  - Subscription lifecycle: active → suspended → canceled with grace period
  - Addons (turbo resource packs) support
  - E2E tests for all quota scenarios

- ✅ **Billing provider decoupling (`BILLING_PROVIDER` feature flag)**
  - `none` (default): quota-only, plans assigned via kubectl annotations
  - `stripe`: full Stripe integration (checkout, webhooks, portal)
  - Frontend dynamically adapts UI based on `/api/billing/config`

- ✅ **Stripe integration**
  - Checkout sessions, subscription CRUD, customer portal
  - Webhook handling for payment events
  - Isolated in `providers/stripe.js` (not loaded when `BILLING_PROVIDER=none`)

- 🔲 **WHMCS integration** (planned)
- 🔲 **Per-project quota management UI**
- 🔲 **Cost trend analysis and budget alerts**

### [Epic] Observability
**Status**: Enhancement Phase

- 📊 **Logs**
  - Centralized logging improvements
  - Log aggregation and analysis

- 📈 **Metrics**
  - Enhanced monitoring and metrics collection
  - Performance dashboards

- 🚨 **Alerts**
  - Alerting system implementation
  - Notification and escalation policies

### [Epic] GPU Support
**Status**: Research Phase

- 🎮 **Evaluate Project HAMI**
  - GPU sharing and management solution
  - Integration assessment with KubeVirt

### [Epic] Managed Services
**Status**: Planning

- ☸️ **K8s CAPI (Cluster API)**
  - Kubernetes cluster management
  - Multi-cluster operations

- ☸️ **K8s Vcluster**
  - Virtual cluster implementation
  - Tenant isolation improvements

- 🗄️ **Rook S3**
  - Object storage services
  - S3-compatible storage backend

- 🗄️ **RDS Percona Operators**
  - Database-as-a-Service implementation
  - MySQL/PostgreSQL managed services

### [Epic] KubeVirt Enhancements
**Status**: High Priority

- 🔄 **CDI Cloning (Priority)**
  - VM disk cloning capabilities
  - Efficient storage management

- 🔥 **CPU, Memory, GPU Hotplug**
  - Dynamic resource allocation
  - Live resource scaling for VMs

## 🐛 Bug Fixes

### Critical Bugs
- 🔧 **UI get_kubeconfig.sh namespace issue**
  - Fix namespace handling in kubeconfig generation
  - Ensure proper authentication and authorization

- 🧹 **Fix issue with stale jobs with pshell**
  - Clean up orphaned pshell jobs
  - Improve job lifecycle management

## 📊 Priority Matrix

### High Priority
1. CDI Cloning functionality
2. Windows metrics fixes
3. UI disk cloning repairs
4. Critical bug fixes (kubeconfig, pshell jobs)

### Medium Priority
1. VMware migration research
2. GPU support evaluation
3. Installer simplification
4. Observability enhancements

### Low Priority
1. Licensing system
2. Billing metering and usage reports
3. Managed services expansion
4. UI enhancements (VM groups, static IP)


**Last Updated**: February 2026
**Document Owner**: Kube-DC Product Team
