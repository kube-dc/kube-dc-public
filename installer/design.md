# Kube-DC Installer Design

## Overview

This document outlines the design for a comprehensive Kube-DC installer that simplifies the deployment process from the current complex manual setup to an intuitive, guided installation experience.

## TL;DR - Installation Strategy

### 🎯 Recommended Approach: Minimal First, Configure Later

**Phase 1: Bare Minimum (15-20 min)**
- **Input**: 5 simple questions (domain, email, org name, project name, SSL type)
- **Output**: Working Kube-DC with internal networking only
- **Use Case**: Test platform, deploy internal VMs/pods, verify functionality

**Phase 2: Add External Networks (5-10 min) - OPTIONAL**
- **When**: After verifying base installation works
- **How**: UI wizard, CLI tool, or manual YAML
- **Enables**: External IPs, LoadBalancers, Floating IPs for VMs

**Phase 3: Add Worker Nodes (10-15 min per node) - OPTIONAL**
- **When**: Need more capacity or HA
- **How**: Run join script on new nodes
- **Enables**: Workload distribution, higher capacity

**Phase 4: Advanced Features (varies) - OPTIONAL**
- **When**: Production requirements arise
- **Options**: HA, backups, GitOps, advanced monitoring

### 🔑 Key Design Decisions

1. **Keep cluster.dev**: Installer wraps cdev, doesn't replace it
2. **Minimal initial install**: Get running quickly with just 5 inputs
3. **Post-install configuration**: Add VLANs, workers, features later
4. **Progressive complexity**: Start simple, add complexity as needed

### 📊 Configuration Timeline

| Feature | During Install | Post-Install | Method |
|---------|---------------|--------------|--------|
| **Domain/IP** | ✅ Required | ❌ | Installer wizard |
| **SSL Certificates** | ✅ Required | ✅ Can update | Installer wizard / UI |
| **Organization** | ✅ Required | ✅ Can add more | Installer wizard / UI |
| **Project** | ✅ Required | ✅ Can add more | Installer wizard / UI |
| **External VLANs** | ❌ Skip | ✅ Add later | UI / CLI / YAML |
| **External IPs** | ❌ Skip | ✅ Add later | UI / CLI / YAML |
| **Worker Nodes** | ❌ Skip | ✅ Add later | Join script |
| **High Availability** | ❌ Skip | ✅ Add later | CLI tool |
| **Backup/Restore** | ❌ Skip | ✅ Add later | CLI tool |
| **Advanced Monitoring** | ❌ Skip | ✅ Add later | CLI tool |
| **GitOps** | ❌ Skip | ✅ Add later | CLI tool |

**Legend:**
- ✅ = Available/Recommended
- ❌ = Not available/Not recommended

## Current State Analysis

### Existing Installation Components
- **Manual Documentation**: `docs/quickstart-hetzner.md` (458 lines of manual steps)
- **Node Installation Scripts**: `hack/install_nodes/master/` and `hack/install_nodes/worker/`
- **cdev Templates**: `installer/kube-dc/templates/kube-dc/template.yaml` (24,921 lines)
- **Stack Configuration**: `installer/kube-dc/stack.yaml` with 50+ parameters

### Current Complexity Issues
1. **Manual Network Configuration**: VLAN setup, IP addressing, DNS configuration
2. **System Preparation**: Kernel updates, module loading, service configuration
3. **RKE2 Cluster Setup**: Master/worker node configuration and joining
4. **Platform Deployment**: Complex cdev template with numerous parameters
5. **Post-Installation**: Manual credential retrieval and validation

## Prerequisites for Installer VM

### Hardware Requirements
- **CPU**: Minimum 4 cores, Recommended 8+ cores
- **Memory**: Minimum 16GB RAM, Recommended 32GB+ RAM
- **Storage**: Minimum 100GB SSD, Recommended 500GB+ SSD
- **Network**: 1Gbps+ network interface with VLAN support

### Software Prerequisites
- **Operating System**: Ubuntu 24.04 LTS (fresh installation)
- **SSH Access**: Root or sudo access to installer VM
- **Network Access**: Internet connectivity for package downloads
- **DNS Resolution**: Ability to resolve external domains

### Infrastructure Prerequisites
- **Hetzner Account**: Access to Robot interface for VLAN/subnet management
- **Domain Management**: DNS control for wildcard domain setup
- **SSL Certificates**: Let's Encrypt or custom certificate authority access
- **Additional Servers**: Optional worker nodes (if multi-node deployment)

### Network Prerequisites
- **Public IP**: Static public IP address for the installer VM
- **VLAN Access**: Hetzner vSwitch with allocated VLANs (4011, 4013, etc.)
- **External Subnets**: Additional subnet allocation for external IPs
- **Firewall Rules**: Ports 80, 443, 6443, 9345 accessible

## Installer Architecture Options

### cluster.dev Role in Installation

**Decision: Keep cluster.dev as the deployment engine** ✅

#### Why Use cluster.dev?

**Advantages:**
- ✅ **Proven deployment engine** - Already handles complex Helm charts and dependencies
- ✅ **Existing templates** - 24k+ line template.yaml is production-tested
- ✅ **State management** - Tracks deployment state, enables updates/upgrades
- ✅ **Idempotent operations** - Safe to re-run, handles failures gracefully
- ✅ **Terraform integration** - Can manage infrastructure if needed
- ✅ **Backend flexibility** - Supports local, S3, GCS state backends
- ✅ **Community support** - Active development and maintenance

**What Installer Adds on Top:**
- 🎯 **User-friendly interface** - No manual YAML editing required
- 🎯 **Auto-detection** - Network, IPs, VLANs discovered automatically
- 🎯 **Pre-flight validation** - Catch errors before deployment starts
- 🎯 **Guided configuration** - Step-by-step wizard with smart defaults
- 🎯 **Error handling** - Better error messages and recovery options
- 🎯 **Post-install validation** - Automated health checks and verification

### Option 1: Interactive Shell Installer (Primary) - WITH cluster.dev Integration

#### Design Philosophy
The installer **wraps cluster.dev** rather than replacing it:
- **Installer layer**: User interaction, validation, configuration generation
- **cluster.dev layer**: Actual deployment using existing templates
- **Result**: Best of both worlds - simple UX + proven deployment engine

#### Structure
```
kube-dc-installer/
├── install.sh                 # Main installer entry point
├── lib/
│   ├── preflight.sh          # System and network validation
│   ├── config.sh             # Interactive configuration wizard
│   ├── network.sh            # Network detection and setup
│   ├── system.sh             # System preparation and optimization
│   ├── cluster.sh            # RKE2 cluster installation
│   ├── cdev.sh               # cluster.dev integration and execution
│   ├── validation.sh         # Post-installation health checks
│   └── utils.sh              # Common utilities and helpers
├── templates/
│   ├── rke2-master.yaml.tmpl # RKE2 master configuration template
│   ├── rke2-worker.yaml.tmpl # RKE2 worker configuration template
│   ├── project.yaml.tmpl     # cdev project configuration template
│   ├── stack.yaml.tmpl       # cdev stack configuration template
│   └── netplan.yaml.tmpl     # Network configuration template
├── configs/
│   ├── single-node.conf      # Single-node deployment preset
│   ├── ha-cluster.conf       # High-availability cluster preset
│   └── production.conf       # Production deployment preset
└── README.md                 # Installation guide
```

#### cluster.dev Integration Points

**1. Installation Phase:**
```bash
# Installer installs cdev if not present
curl -fsSL https://raw.githubusercontent.com/shalb/cluster.dev/master/scripts/get_cdev.sh | sh
```

**2. Configuration Generation:**
```bash
# Installer generates cdev-compatible configs from user input
generate_cdev_project_yaml() {
  cat > ~/kube-dc-install/project.yaml <<EOF
kind: Project
name: kube-dc-deployment
backend: "default"
variables:
  kubeconfig: ~/.kube/config
  debug: ${DEBUG_MODE}
EOF
}

generate_cdev_stack_yaml() {
  # Use existing template from installer/kube-dc/templates/kube-dc/
  # Populate with user-provided values
  cat > ~/kube-dc-install/stack.yaml <<EOF
name: cluster
template: https://github.com/kube-dc/kube-dc-public//installer/kube-dc/templates/kube-dc?ref=main
kind: Stack
backend: default
variables:
  domain: "${USER_DOMAIN}"
  node_external_ip: "${DETECTED_PUBLIC_IP}"
  # ... all other user-configured values
EOF
}
```

**3. Deployment Execution:**
```bash
# Installer runs cdev commands with proper error handling
cd ~/kube-dc-install
cdev plan   # Show deployment plan
cdev apply  # Execute deployment using existing templates
```

**4. State Management:**
```bash
# cdev maintains state in .cluster.dev/
# Installer can query state for validation and updates
cdev output  # Get deployment outputs (URLs, credentials)
```

#### Comparison: Manual vs Installer Approach

**Current Manual Process (Without Installer):**
```bash
# User must manually:
1. Edit stack.yaml with 50+ parameters
2. Understand VLAN configuration
3. Calculate CIDR ranges
4. Configure DNS manually
5. Run: cdev plan && cdev apply
6. Debug errors by reading cdev logs
7. Manually verify deployment
```

**With Installer (cluster.dev Integration):**
```bash
# Installer automates:
1. ./install.sh
2. Answer 5-10 simple questions
3. Installer auto-detects network config
4. Installer generates stack.yaml
5. Installer runs: cdev plan && cdev apply
6. Installer shows user-friendly progress
7. Installer validates deployment automatically
```

**Key Difference:** Same deployment engine (cdev), dramatically better UX!

#### User Experience Flow
```bash
# 1. Download and run installer
curl -fsSL https://install.kube-dc.com | bash
# or
wget -O install.sh https://install.kube-dc.com && chmod +x install.sh && ./install.sh

# 2. Interactive configuration
🚀 Kube-DC Installer v2.0
========================

📋 Pre-flight Checks...
✅ Ubuntu 24.04 LTS detected
✅ System requirements met
✅ Network connectivity verified

🌐 Network Discovery...
🔍 Detected interface: enp0s31f6 (168.119.17.61/28)
🔍 Available VLANs: 4011 (public), 4013 (cloud)
📝 Domain validation required

⚙️ Configuration Wizard...
Please provide the following information:
```

### Option 2: Web-Based Installer UI

#### Architecture
```
web-installer/
├── frontend/                 # React/TypeScript UI
│   ├── src/
│   │   ├── components/
│   │   │   ├── WizardSteps/
│   │   │   ├── ValidationForms/
│   │   │   └── ProgressMonitor/
│   │   ├── services/
│   │   └── utils/
│   └── public/
├── backend/                  # Node.js API server
│   ├── controllers/
│   ├── services/
│   ├── validators/
│   └── deployers/
└── docker-compose.yml        # Containerized deployment
```

#### Access Method
- **URL**: `http://168.119.17.61:8080/installer`
- **Authentication**: SSH key-based or temporary token
- **Real-time Updates**: WebSocket connection for deployment progress

## Configuration Parameters & User Inputs

### 1. Infrastructure Configuration

#### **Deployment Type** (Required)
- **Options**: 
  - `single-node` - All-in-one deployment on installer VM
  - `multi-node` - Master + worker nodes
  - `ha-cluster` - High availability with multiple masters
- **Default**: `single-node`
- **Validation**: Check available resources for selected type

#### **Node Configuration** (Required for multi-node)
```yaml
nodes:
  master:
    - hostname: kube-dc-master-1
      ip: 168.119.17.61
      ssh_key: /root/.ssh/id_rsa
      role: master
  workers:
    - hostname: kube-dc-worker-1  
      ip: 168.119.17.62
      ssh_key: /root/.ssh/id_rsa
      role: worker
```

### 2. Network Configuration

#### **Primary Network Interface** (Auto-detected)
- **Parameter**: `primary_interface`
- **Example**: `enp0s31f6`
- **Validation**: Interface exists and has IP assigned
- **Auto-detection**: Parse `ip addr show` output

#### **Public IP Address** (Auto-detected)
- **Parameter**: `node_external_ip`
- **Example**: `168.119.17.61`
- **Validation**: IP is reachable and matches interface
- **Auto-detection**: Extract from primary interface

#### **Domain Configuration** (Required)
- **Parameter**: `domain`
- **Example**: `stage.kube-dc.com`
- **Validation**: 
  - DNS resolution for `*.domain` points to public IP
  - Domain format validation (RFC compliant)
- **Subdomains Created**:
  - `console.stage.kube-dc.com` - Main UI
  - `login.stage.kube-dc.com` - Keycloak
  - `kube-api.stage.kube-dc.com` - Kubernetes API
  - `grafana.stage.kube-dc.com` - Monitoring
  - `prometheus.stage.kube-dc.com` - Metrics

#### **VLAN Configuration** (Auto-detected/Manual)
```yaml
vlans:
  external_cloud:
    id: 4013
    interface: enp0s31f6
    cidr: "100.65.0.0/16"
    gateway: "100.65.0.1"
    type: cloud
    mtu: 1400
  external_public:
    id: 4011  
    interface: enp0s31f6
    cidr: "168.119.17.48/28"
    gateway: "168.119.17.49"
    type: public
    mtu: 1400
```

#### **Cluster Network CIDRs** (Configurable)
- **Pod CIDR**: `10.100.0.0/16` (default)
- **Service CIDR**: `10.101.0.0/16` (default)
- **Join CIDR**: `172.30.0.0/22` (default)
- **Cluster DNS**: `10.101.0.11` (default)
- **Validation**: No CIDR conflicts with existing networks

### 3. SSL Certificate Configuration

#### **Certificate Type** (Required)
- **Options**:
  - `letsencrypt` - Automatic Let's Encrypt certificates
  - `custom` - User-provided certificates
  - `self-signed` - Self-signed certificates (development only)
- **Default**: `letsencrypt`

#### **Let's Encrypt Configuration** (if selected)
- **Email**: Required for certificate notifications
- **Validation**: Email format validation
- **Auto-renewal**: Configured automatically

#### **Custom Certificate Configuration** (if selected)
```yaml
ssl:
  certificate_path: /path/to/cert.pem
  private_key_path: /path/to/key.pem
  ca_certificate_path: /path/to/ca.pem  # Optional
```

### 4. Organization & Project Setup

#### **Default Organization** (Required)
```yaml
organization:
  name: "shalb"                    # Organization identifier
  display_name: "Shalb Demo Org"  # Human-readable name
  description: "Demo Organization"  # Optional description
  email: "admin@stage.kube-dc.com" # Admin contact email
  project_limit: 10                # Maximum projects allowed
```

#### **Default Project** (Required)
```yaml
project:
  name: "demo"                     # Project identifier
  display_name: "Demo Project"     # Human-readable name
  cidr_block: "10.0.10.0/24"      # Project network CIDR
  egress_network_type: "cloud"     # External network type
  description: "Demo project"      # Optional description
```

### 5. Authentication Configuration

#### **Admin User** (Required)
```yaml
admin_user:
  username: "admin"                # Default admin username
  email: "admin@stage.kube-dc.com" # Admin email
  password: "auto-generated"       # Auto-generated secure password
  first_name: "Admin"              # Optional
  last_name: "User"                # Optional
```

#### **Keycloak Configuration** (Advanced)
```yaml
keycloak:
  admin_username: "admin"          # Keycloak master admin
  admin_password: "auto-generated" # Auto-generated secure password
  database_password: "auto-generated" # Database password
  theme: "kube-dc"                 # Custom theme
```

### 6. Storage Configuration

#### **Monitoring Storage** (Configurable)
```yaml
monitoring:
  prometheus_storage: "20Gi"       # Prometheus data storage
  retention_size: "17GiB"          # Data retention size
  retention_period: "365d"         # Data retention period
  grafana_storage: "5Gi"           # Grafana storage
  loki_storage: "10Gi"             # Loki log storage
```

#### **Platform Storage** (Configurable)
```yaml
storage:
  storage_class: "local-path"      # Default storage class
  vm_storage_class: "local-path"   # VM storage class
  backup_storage: "50Gi"           # Backup storage allocation
```

### 7. Resource Limits & Quotas

#### **System Resource Allocation** (Auto-calculated)
```yaml
resources:
  system_reserved:
    cpu: "2000m"                   # CPU reserved for system
    memory: "4Gi"                  # Memory reserved for system
  kube_dc_allocation:
    cpu: "4000m"                   # CPU for Kube-DC components
    memory: "8Gi"                  # Memory for Kube-DC components
  workload_allocation:
    cpu: "remaining"               # Remaining CPU for workloads
    memory: "remaining"            # Remaining memory for workloads
```

### 8. Advanced Configuration

#### **RKE2 Version** (Configurable)
- **Parameter**: `rke2_version`
- **Default**: `v1.32.1+rke2r1`
- **Validation**: Version exists and is compatible

#### **Kube-DC Version** (Configurable)
- **Parameter**: `kube_dc_version`
- **Default**: `v0.1.33`
- **Validation**: Version exists in registry

#### **Component Versions** (Expert Mode)
```yaml
versions:
  kubevirt: "v1.6.0"
  kubevirt_cdi: "v1.62.0"
  kube_ovn: "v1.14.4"
  keycloak: "24.3.0"
  prometheus: "67.4.0"
  cert_manager: "v1.14.4"
```

#### **Debug & Development** (Optional)
```yaml
debug:
  enabled: false                   # Enable debug logging
  verbose_logs: false              # Extra verbose logging
  development_mode: false          # Development features
  skip_ssl_verification: false     # Skip SSL verification (dev only)
```

## Installation Phases: Minimal First, Configure Later

### Phase-Based Installation Strategy

The installer supports a **minimal initial deployment** with post-installation configuration:

```
Phase 1: Bare Minimum Installation (Required)
    ↓
Phase 2: Network Configuration (Optional - Post-Install)
    ↓
Phase 3: Worker Nodes (Optional - Post-Install)
    ↓
Phase 4: Advanced Features (Optional - Post-Install)
```

### Phase 1: Bare Minimum Installation (15-20 minutes)

**Required Inputs (Only 5 questions!):**
1. **Domain or IP**: `stage.kube-dc.com` or `168.119.17.61`
2. **Email**: `admin@example.com`
3. **Organization Name**: `myorg`
4. **Project Name**: `demo`
5. **SSL Type**: `letsencrypt` / `self-signed` / `custom`

**What Gets Installed:**

```
┌─────────────────────────────────────────────────────────────┐
│  Single Node (168.119.17.61)                                │
│                                                              │
│  ┌────────────────────────────────────────────────────┐    │
│  │  RKE2 Kubernetes Cluster                           │    │
│  │  - Control plane + worker on same node             │    │
│  │  - Pod CIDR: 10.100.0.0/16                         │    │
│  │  - Service CIDR: 10.101.0.0/16                     │    │
│  └────────────────────────────────────────────────────┘    │
│                                                              │
│  ┌────────────────────────────────────────────────────┐    │
│  │  Kube-OVN Networking (Internal Only)               │    │
│  │  - Overlay network for pods                        │    │
│  │  - Join network: 172.30.0.0/22                     │    │
│  │  - NO external VLANs configured yet                │    │
│  └────────────────────────────────────────────────────┘    │
│                                                              │
│  ┌────────────────────────────────────────────────────┐    │
│  │  Kube-DC Platform                                  │    │
│  │  ✅ Controllers (Organization, Project, EIP, FIP)  │    │
│  │  ✅ UI (console.stage.kube-dc.com)                 │    │
│  │  ✅ Keycloak (login.stage.kube-dc.com)             │    │
│  │  ✅ KubeVirt (VM support)                          │    │
│  │  ✅ Monitoring (Prometheus, Grafana, Loki)         │    │
│  └────────────────────────────────────────────────────┘    │
│                                                              │
│  ┌────────────────────────────────────────────────────┐    │
│  │  Default Resources                                 │    │
│  │  - Organization: myorg                             │    │
│  │  - Project: demo (10.0.10.0/24)                    │    │
│  │  - Admin user with credentials                     │    │
│  └────────────────────────────────────────────────────┘    │
└─────────────────────────────────────────────────────────────┘
```

**What You CAN Do After Bare Minimum Install:**
- ✅ Access Kube-DC UI
- ✅ Create VMs with internal networking
- ✅ Deploy pods and services (ClusterIP, NodePort)
- ✅ Manage users and organizations
- ✅ View monitoring dashboards
- ✅ Test the platform functionality

**What's NOT Configured Yet (Post-Install Options):**
- ❌ External VLAN networks (4011, 4013)
- ❌ Floating IPs / External IPs for VMs
- ❌ LoadBalancer services with public IPs
- ❌ Worker nodes
- ❌ High availability
- ❌ Advanced backup/restore

**Result:** Fully functional Kube-DC platform for internal workloads. Perfect for testing, development, or environments without external network requirements.

### Phase 2: External Network Configuration (Post-Install)

**When to Configure:** After verifying base installation works

**Configuration Options:**

#### Option A: Add External Networks via UI
```bash
# Access Kube-DC UI
https://console.stage.kube-dc.com

# Navigate to: Settings → External Networks → Add Network
# Configure VLAN 4013 (Cloud Network)
# Configure VLAN 4011 (Public Network)
```

#### Option B: Add External Networks via CLI Tool
```bash
# Run post-install configuration script
./kube-dc-configure.sh --add-external-network

# Interactive prompts:
🌐 External Network Configuration
================================

[1/5] Network Type: cloud / public
      Selection: cloud

[2/5] VLAN ID: 4013
      Auto-detected: ✅ VLAN 4013 available on enp0s31f6

[3/5] Network CIDR: 100.65.0.0/16
      Gateway: 100.65.0.1

[4/5] MTU: 1400 (recommended for VLAN)

[5/5] Exclude IPs: 100.65.0.1..100.65.0.100

✅ External network 'ext-cloud' configured
✅ Kube-OVN updated
✅ Projects can now use egressNetworkType: cloud
```

#### Option C: Manual YAML Configuration
```bash
# Apply external network configuration
kubectl apply -f - <<EOF
apiVersion: kubeovn.io/v1
kind: Vlan
metadata:
  name: vlan4013
spec:
  id: 4013
  provider: ext-cloud
---
apiVersion: kubeovn.io/v1
kind: Subnet
metadata:
  name: ext-cloud
  labels:
    network.kube-dc.com/external-network-type: "cloud"
spec:
  cidrBlock: 100.65.0.0/16
  gateway: 100.65.0.1
  vlan: vlan4013
  # ... additional config
EOF
```

**What This Enables:**
- ✅ Projects can use external IPs
- ✅ LoadBalancer services work
- ✅ Floating IPs for VMs
- ✅ External connectivity for workloads

### Phase 3: Worker Node Addition (Post-Install)

**When to Add:** When you need more capacity or want HA

**Process:**

#### Step 1: Prepare Worker Node
```bash
# On worker node (168.119.17.62)
# Installer provides ready-to-use script

# Download worker preparation script
curl -fsSL https://console.stage.kube-dc.com/api/cluster/worker-script | bash

# Or manual preparation
./kube-dc-configure.sh --prepare-worker-node
```

#### Step 2: Join Worker to Cluster
```bash
# On master node, get join token
./kube-dc-configure.sh --get-worker-token

# Output:
Worker Join Command:
==================
ssh root@168.119.17.62 'bash -s' < <(curl -fsSL https://console.stage.kube-dc.com/api/cluster/join-worker?token=XXXXX)

# Or via UI:
# Settings → Cluster Nodes → Add Worker Node
# Copy/paste SSH command
```

#### Step 3: Configure Worker Networking
```bash
# Installer auto-configures:
- VLAN interfaces (if external networks configured)
- OVN networking
- Node labels and taints
- Storage provisioner

# Verification:
kubectl get nodes
# NAME                STATUS   ROLES                  AGE
# kube-dc-master-1    Ready    control-plane,master   1h
# kube-dc-worker-1    Ready    worker                 5m
```

**What This Enables:**
- ✅ Workload distribution across nodes
- ✅ Higher capacity for VMs and pods
- ✅ Node affinity and anti-affinity
- ✅ Preparation for HA setup

### Phase 4: Advanced Features (Post-Install)

**Configure When Needed:**

#### A. High Availability
```bash
./kube-dc-configure.sh --enable-ha
# Adds additional master nodes
# Configures etcd cluster
# Sets up load balancer for API server
```

#### B. Backup & Disaster Recovery
```bash
./kube-dc-configure.sh --enable-backup
# Configures Velero
# Sets up backup schedules
# Configures backup storage (S3, NFS, etc.)
```

#### C. Additional External Networks
```bash
./kube-dc-configure.sh --add-external-network
# Add more VLANs as needed
# Configure multiple public IP ranges
# Set up network policies
```

#### D. Advanced Monitoring
```bash
./kube-dc-configure.sh --enable-advanced-monitoring
# Add custom Grafana dashboards
# Configure alerting rules
# Set up log aggregation
```

#### E. GitOps Integration
```bash
./kube-dc-configure.sh --enable-gitops
# Install ArgoCD or Flux
# Configure repository connections
# Set up automated deployments
```

## Installation Presets

### Preset 1: Bare Minimum (Recommended for First Install)
```yaml
preset: bare-minimum
deployment_type: single-node
inputs_required: 5               # Only 5 questions!
installation_time: 15-20 minutes
features:
  - Basic Kubernetes cluster
  - Kube-DC UI and controllers
  - Internal networking only
  - One organization + project
  - Self-signed SSL (or Let's Encrypt)
post_install_options:
  - Add external networks
  - Add worker nodes
  - Configure monitoring
  - Enable backups
```

### Preset 2: Quick Start with External Network
```yaml
preset: quick-start
deployment_type: single-node
inputs_required: 8               # 5 basic + 3 network
installation_time: 20-25 minutes
features:
  - Everything in bare-minimum
  - One external VLAN network
  - External IPs enabled
  - LoadBalancer services
post_install_options:
  - Add more external networks
  - Add worker nodes
  - Configure HA
```

### Preset 3: Production Ready
```yaml
preset: production
deployment_type: multi-node
inputs_required: 15+             # Full configuration
installation_time: 30-40 minutes
features:
  - Multi-node cluster
  - Multiple external networks
  - High availability
  - Advanced monitoring
  - Backup configuration
  - Custom SSL certificates
```

## Validation & Safety Features

### Pre-flight Checks
1. **System Requirements**
   - OS version and compatibility
   - CPU, memory, and storage availability
   - Required packages and dependencies
   - Kernel version and modules

2. **Network Connectivity**
   - Internet access for downloads
   - DNS resolution functionality
   - Port availability (80, 443, 6443, 9345)
   - VLAN interface accessibility

3. **Permissions & Access**
   - Root/sudo access verification
   - SSH key accessibility for multi-node
   - File system write permissions
   - Service management capabilities

### Runtime Validation
1. **Configuration Validation**
   - CIDR overlap detection
   - IP address reachability
   - Domain DNS resolution
   - Certificate validity

2. **Resource Monitoring**
   - Deployment progress tracking
   - Resource utilization monitoring
   - Error detection and reporting
   - Rollback capability on failure

### Post-Installation Verification
1. **Service Health Checks**
   - Kubernetes cluster status
   - Kube-DC component readiness
   - UI accessibility testing
   - Authentication functionality

2. **Network Connectivity Tests**
   - Pod-to-pod communication
   - External network access
   - Load balancer functionality
   - DNS resolution within cluster

## Error Handling & Recovery

### Rollback Capabilities
- **Configuration Rollback**: Restore previous working configuration
- **Service Rollback**: Revert to previous component versions
- **System Rollback**: Restore system state before installation
- **Network Rollback**: Restore original network configuration

### Error Recovery Strategies
- **Automatic Retry**: Retry failed operations with exponential backoff
- **Manual Intervention**: Pause for user input on critical failures
- **Skip Options**: Allow skipping non-critical components
- **Debug Mode**: Enhanced logging and debugging information

## Security Considerations

### Credential Management
- **Auto-generated Passwords**: Cryptographically secure password generation
- **Credential Storage**: Secure storage in Kubernetes secrets
- **Access Control**: Role-based access control (RBAC) setup
- **Certificate Management**: Automatic certificate rotation

### Network Security
- **Firewall Configuration**: Automatic firewall rule setup
- **Network Policies**: Default network policies for isolation
- **TLS Encryption**: End-to-end TLS encryption for all communications
- **Secret Encryption**: Kubernetes secret encryption at rest

## Monitoring & Observability

### Installation Monitoring
- **Progress Tracking**: Real-time installation progress
- **Resource Usage**: Monitor CPU, memory, and storage during installation
- **Network Activity**: Track network operations and downloads
- **Error Logging**: Comprehensive error logging and reporting

### Post-Installation Monitoring
- **Health Dashboards**: Pre-configured Grafana dashboards
- **Alert Rules**: Default Prometheus alert rules
- **Log Aggregation**: Centralized logging with Loki
- **Performance Metrics**: System and application performance monitoring

## Future Enhancements

### Phase 2 Features
- **Multi-Cloud Support**: AWS, GCP, Azure deployment options
- **Backup & Restore**: Automated backup and disaster recovery
- **Upgrade Management**: In-place upgrade capabilities
- **Configuration Management**: GitOps-based configuration management

### Integration Options
- **CI/CD Integration**: Jenkins, GitLab CI, GitHub Actions
- **Infrastructure as Code**: Terraform, Ansible integration
- **Monitoring Integration**: External monitoring system integration
- **Identity Provider Integration**: LDAP, Active Directory, SAML

## Implementation Timeline

### Phase 1: Core Installer (4-6 weeks)
- Week 1-2: Interactive shell installer framework
- Week 3-4: Configuration wizard and validation
- Week 5-6: Integration with existing cdev templates

### Phase 2: Web UI (3-4 weeks)
- Week 7-8: React frontend development
- Week 9-10: Backend API and WebSocket integration
- Week 11: Testing and refinement

### Phase 3: Advanced Features (2-3 weeks)
- Week 12-13: Multi-node support and HA configuration
- Week 14: Documentation and user guides

## Success Criteria

### Installation Success Metrics
- **Time to Deploy**: < 30 minutes for single-node deployment
- **Success Rate**: > 95% successful installations
- **User Experience**: < 10 user inputs required for basic deployment
- **Error Recovery**: < 5 minutes to recover from common failures

### Platform Readiness Metrics
- **UI Accessibility**: Console accessible within 2 minutes of completion
- **Authentication**: User login functional immediately
- **Workload Deployment**: Able to deploy VMs within 5 minutes
- **Monitoring**: Dashboards populated with data within 10 minutes

This comprehensive installer design addresses all aspects of Kube-DC deployment while maintaining simplicity for end users and providing advanced configuration options for expert users.
