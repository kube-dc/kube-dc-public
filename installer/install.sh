#!/bin/bash
#
# Kube-DC Installer - Bare Minimum Installation
# This script installs Kube-DC with minimal configuration (5 questions only)
#

set -e

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m' # No Color

# Helper functions
log_info() {
    echo -e "${BLUE}ℹ${NC} $1"
}

log_success() {
    echo -e "${GREEN}✅${NC} $1"
}

log_warning() {
    echo -e "${YELLOW}⚠${NC} $1"
}

log_error() {
    echo -e "${RED}❌${NC} $1"
}

log_header() {
    echo ""
    echo -e "${BLUE}━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━${NC}"
    echo -e "${BLUE}  $1${NC}"
    echo -e "${BLUE}━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━${NC}"
    echo ""
}

# Banner
clear
cat << "EOF"
╔═══════════════════════════════════════════════════════════════╗
║                                                               ║
║   ██╗  ██╗██╗   ██╗██████╗ ███████╗      ██████╗  ██████╗     ║
║   ██║ ██╔╝██║   ██║██╔══██╗██╔════╝      ██╔══██╗██╔════╝     ║
║   █████╔╝ ██║   ██║██████╔╝█████╗  █████╗██║  ██║██║          ║
║   ██╔═██╗ ██║   ██║██╔══██╗██╔══╝  ╚════╝██║  ██║██║          ║
║   ██║  ██╗╚██████╔╝██████╔╝███████╗      ██████╔╝╚██████╗     ║
║   ╚═╝  ╚═╝ ╚═════╝ ╚═════╝ ╚══════╝      ╚═════╝  ╚═════╝     ║
║                                                               ║
║              Kubernetes Data Center Platform                  ║
║                    Installer v1.0                             ║
║                                                               ║
╚═══════════════════════════════════════════════════════════════╝

EOF

log_info "Welcome to the Kube-DC Installer!"
echo ""
log_info "This installer will set up a bare minimum Kube-DC platform with:"
echo "  • RKE2 Kubernetes cluster (single-node)"
echo "  • Kube-OVN networking (internal only)"
echo "  • Kube-DC controllers and UI"
echo "  • Keycloak authentication"
echo "  • KubeVirt for VMs"
echo "  • Basic monitoring (Prometheus, Grafana)"
echo ""
log_warning "Installation time: ~15-20 minutes"
log_warning "You'll be asked only 5 simple questions"
echo ""
read -p "Press Enter to continue or Ctrl+C to cancel..."

# Pre-flight checks
log_header "Step 1/7: Pre-flight Checks"

# Check if running as root
if [[ $EUID -eq 0 ]]; then
   log_error "This script must NOT be run as root"
   exit 1
fi

# Check operating system
if ! grep -q "Ubuntu" /etc/os-release; then
    log_error "This installer only supports Ubuntu"
    log_info "Detected: $(grep PRETTY_NAME /etc/os-release | cut -d'"' -f2)"
    exit 1
fi

log_success "Running on Ubuntu"

# Check system resources
TOTAL_CPU=$(nproc)
TOTAL_MEM=$(free -g | awk '/^Mem:/{print $2}')

log_info "System resources:"
echo "  CPU cores: ${TOTAL_CPU}"
echo "  Memory: ${TOTAL_MEM}GB"
echo ""

if [ "$TOTAL_CPU" -lt 4 ]; then
    log_error "Minimum 4 CPU cores required (found: ${TOTAL_CPU})"
    log_error "CPU cores: ${TOTAL_CPU} (✗ 4 cores minimum required)"
    exit 1
fi

if [[ $TOTAL_DISK -ge 80 ]]; then
    log_success "Disk space: ${TOTAL_DISK}GB (✓ >= 100GB recommended)"
else
    log_warning "Disk space: ${TOTAL_DISK}GB (⚠ 100GB recommended)"
fi

# Check network
log_info "Checking network connectivity..."
if ping -c 1 8.8.8.8 &> /dev/null; then
    log_success "Internet connectivity: OK"
else
    log_error "No internet connectivity"
    exit 1
fi

# Check sudo
log_info "Checking sudo access..."
if sudo -n true 2>/dev/null; then
    log_success "Sudo access: OK (passwordless)"
    SUDO_NEEDS_PASSWORD=false
elif sudo -v 2>/dev/null; then
    log_success "Sudo access: OK (with password)"
    SUDO_NEEDS_PASSWORD=true
    # Keep sudo session alive
    log_info "Keeping sudo session active during installation..."
    sudo -v
else
    log_error "Sudo access required"
    exit 1
fi

# Configuration wizard
log_header "Configuration Wizard (5 Questions)"

# Question 1: Domain
echo ""
log_info "Question 1/5: Domain Name"
echo "  A domain with wildcard DNS (*.yourdomain.com) pointing to this server"
echo "  Required for SSL certificates - IP addresses are not supported"
echo "  Examples: kube-dc.example.com, stage.company.io"
echo ""
echo "  ⚠️  Important: Configure DNS before installation:"
echo "     *.yourdomain.com → <your-public-ip-or-floating-ip>"
echo ""
log_info "Note: If using Floating IP, point DNS to the FIP, not the internal IP"
echo ""
read -p "  Domain name: " DOMAIN
while [[ -z "$DOMAIN" ]] || [[ "$DOMAIN" =~ ^[0-9]+\.[0-9]+\.[0-9]+\.[0-9]+$ ]]; do
  if [[ -z "$DOMAIN" ]]; then
    log_error "Domain cannot be empty"
  else
    log_error "IP addresses are not supported - please use a domain name"
  fi
  read -p "  Domain name: " DOMAIN
done
log_success "Using: $DOMAIN"

# Question 2: Email
echo ""
log_info "Question 2/5: Administrator Email"
echo "  Used for SSL certificates and admin notifications"
echo ""
read -p "  Email [admin@${DOMAIN}]: " EMAIL
EMAIL=${EMAIL:-admin@${DOMAIN}}
log_success "Using: $EMAIL"

# Question 3: Organization name
echo ""
log_info "Question 3/5: Organization Name"
echo "  A unique identifier for your organization (lowercase, no spaces)"
echo "  Examples: mycompany, acme, demo"
echo ""
read -p "  Organization name [demo]: " ORG_NAME
ORG_NAME=${ORG_NAME:-demo}
log_success "Using: $ORG_NAME"

# Question 4: Project name
echo ""
log_info "Question 4/5: Initial Project Name"
echo "  Your first project within the organization"
echo "  Examples: production, staging, dev"
echo ""
read -p "  Project name [default]: " PROJECT_NAME
PROJECT_NAME=${PROJECT_NAME:-default}
log_success "Using: $PROJECT_NAME"

# Question 5: SSL type
echo ""
log_info "Question 5/5: SSL Certificate Type"
echo "  1) Let's Encrypt (automatic, requires valid domain)"
echo "  2) Self-signed (for testing/development)"
echo "  3) Custom (provide your own certificates)"
echo ""
read -p "  Select option [1]: " SSL_OPTION
SSL_OPTION=${SSL_OPTION:-1}

case $SSL_OPTION in
    1)
        SSL_TYPE="letsencrypt"
        log_success "Using: Let's Encrypt (automatic)"
        ;;
    2)
        SSL_TYPE="self-signed"
        log_success "Using: Self-signed certificates"
        ;;
    3)
        SSL_TYPE="custom"
        log_success "Using: Custom certificates"
        log_warning "You'll need to provide certificate files later"
        ;;
    *)
        SSL_TYPE="letsencrypt"
        log_success "Using: Let's Encrypt (default)"
        ;;
esac

# Summary
log_header "Configuration Summary"
echo ""
echo "  Domain/IP:     $DOMAIN"
echo "  Email:         $EMAIL"
echo "  Organization:  $ORG_NAME"
echo "  Project:       $PROJECT_NAME"
echo "  SSL Type:      $SSL_TYPE"
echo ""
read -p "Proceed with installation? (yes/no) [yes]: " CONFIRM
CONFIRM=${CONFIRM:-yes}

if [[ ! "$CONFIRM" =~ ^[Yy]([Ee][Ss])?$ ]]; then
    log_error "Installation cancelled"
    exit 0
fi

# Detect server IP for DNS verification and RKE2 configuration
DETECTED_IP=$(ip route get 8.8.8.8 | awk '{print $7; exit}')

# Detect external/public IP
log_info "Detecting network configuration..."
EXTERNAL_IP=$(curl -s --max-time 5 ifconfig.me 2>/dev/null || curl -s --max-time 5 api.ipify.org 2>/dev/null || echo "")

if [ -n "$EXTERNAL_IP" ]; then
    if [ "$EXTERNAL_IP" != "$DETECTED_IP" ]; then
        log_info "Internal IP: ${DETECTED_IP}"
        log_info "External IP: ${EXTERNAL_IP} (Floating IP or NAT detected)"
        log_warning "⚠️  Configure DNS to point to the external IP: ${EXTERNAL_IP}"
        RECOMMENDED_IP="$EXTERNAL_IP"
    else
        log_info "Server IP: ${DETECTED_IP} (directly accessible)"
        RECOMMENDED_IP="$DETECTED_IP"
    fi
else
    log_warning "Could not detect external IP (network may be restricted)"
    log_info "Internal IP: ${DETECTED_IP}"
    RECOMMENDED_IP="$DETECTED_IP"
fi

# DNS verification for Let's Encrypt
if [[ "$SSL_TYPE" == "letsencrypt" ]]; then
    log_header "DNS Verification"
    echo ""
    log_info "Verifying DNS records for Let's Encrypt certificates..."
    log_info "Internal IP detected: ${DETECTED_IP} (may differ from public/floating IP)"
    echo ""
    
    # List of subdomains to check
    SUBDOMAINS=("login" "console" "backend" "grafana")
    DNS_OK=true
    RESOLVED_IPS=()
    
    for subdomain in "${SUBDOMAINS[@]}"; do
        FQDN="${subdomain}.${DOMAIN}"
        log_info "Checking ${FQDN}..."
        
        # Try to resolve the domain
        if RESOLVED_IP=$(dig +short "$FQDN" A | tail -1 2>/dev/null); then
            if [ -z "$RESOLVED_IP" ]; then
                log_error "  ✗ No A record found for ${FQDN}"
                DNS_OK=false
            else
                log_success "  ✓ ${FQDN} → ${RESOLVED_IP}"
                RESOLVED_IPS+=("$RESOLVED_IP")
            fi
        else
            # Fallback to nslookup if dig is not available
            if RESOLVED_IP=$(nslookup "$FQDN" 8.8.8.8 2>/dev/null | awk '/^Address: / { print $2 }' | tail -1); then
                if [ -z "$RESOLVED_IP" ]; then
                    log_error "  ✗ No A record found for ${FQDN}"
                    DNS_OK=false
                else
                    log_success "  ✓ ${FQDN} → ${RESOLVED_IP}"
                    RESOLVED_IPS+=("$RESOLVED_IP")
                fi
            else
                log_error "  ✗ Failed to resolve ${FQDN}"
                DNS_OK=false
            fi
        fi
    done
    
    # Check if all subdomains resolve to the same IP
    if [ ${#RESOLVED_IPS[@]} -gt 0 ]; then
        UNIQUE_IPS=($(printf "%s\n" "${RESOLVED_IPS[@]}" | sort -u))
        if [ ${#UNIQUE_IPS[@]} -eq 1 ]; then
            log_info ""
            log_info "All subdomains resolve to: ${UNIQUE_IPS[0]}"
        else
            log_warning ""
            log_warning "Warning: Subdomains resolve to different IPs: ${UNIQUE_IPS[*]}"
        fi
    fi
    
    echo ""
    if [ "$DNS_OK" = false ]; then
        log_error "DNS verification failed!"
        echo ""
        log_warning "Let's Encrypt requires proper DNS configuration."
        echo ""
        echo "  ⚠️  Recommended: Configure wildcard DNS record:"
        echo "     *.${DOMAIN}  →  ${RECOMMENDED_IP}"
        echo ""
        echo "  Or configure individual A records for each subdomain:"
        for subdomain in "${SUBDOMAINS[@]}"; do
            echo "     ${subdomain}.${DOMAIN}  →  ${RECOMMENDED_IP}"
        done
        if [ "$EXTERNAL_IP" != "$DETECTED_IP" ] && [ -n "$EXTERNAL_IP" ]; then
            echo ""
            echo "  Note: Use external IP (${EXTERNAL_IP}), not internal IP (${DETECTED_IP})"
        fi
        echo ""
        log_warning "Options:"
        echo "  1. Fix DNS records and re-run the installer"
        echo "  2. Continue anyway (certificates will fail, you can fix DNS later)"
        echo "  3. Cancel and use self-signed certificates instead"
        echo ""
        read -p "Continue anyway? (yes/no) [no]: " CONTINUE_DNS
        CONTINUE_DNS=${CONTINUE_DNS:-no}
        
        if [[ ! "$CONTINUE_DNS" =~ ^[Yy]([Ee][Ss])?$ ]]; then
            log_error "Installation cancelled. Please fix DNS records and try again."
            exit 1
        fi
        
        log_warning "Continuing with incorrect DNS - SSL certificates will fail"
    else
        log_success "All DNS records are correctly configured!"
    fi
fi

# Installation process
log_header "Installation Process"

# Keep sudo alive in background if needed
if [[ "$SUDO_NEEDS_PASSWORD" == "true" ]]; then
    # Run sudo -v in background every 60 seconds
    (while true; do sudo -v; sleep 60; done) &
    SUDO_KEEPALIVE_PID=$!
    trap "kill $SUDO_KEEPALIVE_PID 2>/dev/null" EXIT
fi

# Fix hostname resolution to avoid "sudo: unable to resolve host" warnings
CURRENT_HOSTNAME=$(hostname)
if ! grep -qE "^127\.0\.(0|1)\.1[[:space:]]+${CURRENT_HOSTNAME}" /etc/hosts 2>/dev/null && \
   ! grep -qE "^${DETECTED_IP}[[:space:]]+${CURRENT_HOSTNAME}" /etc/hosts 2>/dev/null; then
    log_info "Configuring hostname resolution..."
    # Add hostname with internal IP
    echo "${DETECTED_IP} ${CURRENT_HOSTNAME}" | sudo tee -a /etc/hosts > /dev/null
    # Also add localhost mapping if not present
    if ! grep -qE "^127\.0\.1\.1[[:space:]]" /etc/hosts 2>/dev/null; then
        echo "127.0.1.1 ${CURRENT_HOSTNAME}" | sudo tee -a /etc/hosts > /dev/null
    fi
    log_success "Hostname configured: ${CURRENT_HOSTNAME} → ${DETECTED_IP}"
fi

# Create installation directory
INSTALL_DIR="$HOME/kube-dc-install"
mkdir -p "$INSTALL_DIR"
cd "$INSTALL_DIR"

log_info "Installation directory: $INSTALL_DIR"

# Step 1: Install cluster.dev
log_info "Step 1/7: Installing cluster.dev..."
if ! command -v cdev &> /dev/null; then
    curl -fsSL https://raw.githubusercontent.com/shalb/cluster.dev/master/scripts/get_cdev.sh | sh
    # Add cdev to PATH
    export PATH="$HOME/.cluster.dev/bin:$PATH"
    log_success "cluster.dev installed"
else
    log_success "cluster.dev already installed"
fi

# Ensure cdev is in PATH
export PATH="$HOME/.cluster.dev/bin:$PATH"

# Step 2: Install kubectl
log_info "Step 2/7: Installing kubectl..."
if ! command -v kubectl &> /dev/null; then
    curl -LO "https://dl.k8s.io/release/$(curl -L -s https://dl.k8s.io/release/stable.txt)/bin/linux/amd64/kubectl"
    chmod +x kubectl
    sudo mv kubectl /usr/local/bin/
    log_success "kubectl installed"
else
    log_success "kubectl already installed"
fi

# Step 3: System preparation
log_info "Step 3/7: Preparing system..."

# Disable IPv6 first (before RKE2 installation)
log_info "Disabling IPv6 to avoid conflicts..."
sudo sysctl -w net.ipv6.conf.all.disable_ipv6=1 > /dev/null 2>&1
sudo sysctl -w net.ipv6.conf.default.disable_ipv6=1 > /dev/null 2>&1
sudo sysctl -w net.ipv6.conf.lo.disable_ipv6=1 > /dev/null 2>&1

# Fix DNS immediately after disabling IPv6
log_info "Fixing DNS after IPv6 disable..."
sudo systemctl stop systemd-resolved 2>/dev/null || true
sudo systemctl disable systemd-resolved 2>/dev/null || true
sudo rm -f /etc/resolv.conf
echo "nameserver 8.8.8.8" | sudo tee /etc/resolv.conf > /dev/null
echo "nameserver 8.8.4.4" | sudo tee -a /etc/resolv.conf > /dev/null

# Update system
log_info "Updating package lists..."
sudo apt-get update -qq

# Verify DNS
log_info "Verifying DNS..."
if ! timeout 5 nslookup github.com > /dev/null 2>&1; then
    log_warning "DNS not responding, fixing..."
    
    # Try restarting systemd-resolved first
    sudo systemctl restart systemd-resolved 2>/dev/null || true
    sleep 2
    
    # If still broken, use direct DNS
    if ! timeout 5 nslookup github.com > /dev/null 2>&1; then
        sudo rm -f /etc/resolv.conf
        sudo tee /etc/resolv.conf > /dev/null << 'RESOLV'
nameserver 8.8.8.8
nameserver 8.8.4.4
nameserver 1.1.1.1
RESOLV
        log_success "DNS fixed with direct nameservers"
    fi
fi

# Update system
log_info "Updating package lists..."
sudo apt-get update -qq

# Install required packages
log_info "Installing required packages..."
sudo apt-get install -y -qq curl unzip iptables dnsutils git linux-headers-$(uname -r) > /dev/null 2>&1

# Configure sysctl
log_info "Configuring system parameters..."
# Check if already configured to avoid duplicates
if ! grep -q "Kube-DC optimizations" /etc/sysctl.conf 2>/dev/null; then
    sudo tee -a /etc/sysctl.conf > /dev/null << 'SYSCTL'
# Kube-DC optimizations
fs.inotify.max_user_watches=1524288
fs.inotify.max_user_instances=4024
net.ipv4.ip_forward = 1
# Disable IPv6 to avoid conflicts
net.ipv6.conf.all.disable_ipv6 = 1
net.ipv6.conf.default.disable_ipv6 = 1
net.ipv6.conf.lo.disable_ipv6 = 1
SYSCTL
fi

sudo sysctl -p > /dev/null 2>&1

# Load nf_conntrack module
log_info "Loading kernel modules..."
sudo modprobe nf_conntrack
if ! grep -q "nf_conntrack" /etc/modules 2>/dev/null; then
    echo "nf_conntrack" | sudo tee -a /etc/modules > /dev/null
fi

log_success "System prepared"

# Step 4: Install RKE2
log_info "Step 4/7: Installing RKE2 Kubernetes..."

# Check if RKE2 is already installed and running
if systemctl is-active --quiet rke2-server.service 2>/dev/null; then
    log_info "RKE2 is already running, skipping installation"
    export KUBECONFIG=/etc/rancher/rke2/rke2.yaml
    export PATH="/var/lib/rancher/rke2/bin:$PATH"
    log_success "Using existing RKE2 installation"
    # Verify it's working
    if kubectl get nodes &>/dev/null; then
        log_success "RKE2 cluster is healthy"
    else
        log_warning "RKE2 is running but cluster needs attention"
    fi
else
    log_info "Installing fresh RKE2 cluster..."
    
    # Create RKE2 config
sudo mkdir -p /etc/rancher/rke2/

sudo tee /etc/rancher/rke2/config.yaml > /dev/null << RKECONFIG
node-name: $(hostname)
disable-cloud-controller: true
disable: rke2-ingress-nginx
cni: none
cluster-cidr: "10.100.0.0/16"
service-cidr: "10.101.0.0/16"
cluster-dns: "10.101.0.11"
node-label:
  - kube-dc-manager=true
  - kube-ovn/role=master
kube-apiserver-arg: 
  - authentication-config=/etc/rancher/auth-conf.yaml
debug: true
node-external-ip: ${DETECTED_IP}
tls-san:
  - ${DOMAIN}
  - ${DETECTED_IP}
  - $(hostname)
  - 127.0.0.1
advertise-address: ${DETECTED_IP}
node-ip: ${DETECTED_IP}
RKECONFIG

# Create auth config
sudo tee /etc/rancher/auth-conf.yaml > /dev/null << 'AUTHCONF'
apiVersion: apiserver.config.k8s.io/v1beta1
kind: AuthenticationConfiguration
jwt: []
AUTHCONF

sudo chmod 666 /etc/rancher/auth-conf.yaml

# Install RKE2
export INSTALL_RKE2_VERSION="v1.32.1+rke2r1"
export INSTALL_RKE2_TYPE="server"
curl -sfL https://get.rke2.io | sudo sh -

# Start RKE2
sudo systemctl enable rke2-server.service
sudo systemctl start rke2-server.service

# Wait for RKE2 to be ready
log_info "Waiting for RKE2 to be ready..."
sleep 30

# Fix DNS after RKE2 installation (RKE2/containerd can break systemd-resolved)
log_info "Checking DNS after RKE2 installation..."
if ! nslookup github.com > /dev/null 2>&1; then
    log_warning "DNS broken after RKE2, fixing..."
    
    # Restart systemd-resolved
    sudo systemctl restart systemd-resolved
    sleep 2
    
    # If still broken, use direct DNS
    if ! nslookup github.com > /dev/null 2>&1; then
        log_warning "systemd-resolved not working, switching to direct DNS..."
        sudo rm -f /etc/resolv.conf
        sudo tee /etc/resolv.conf > /dev/null << 'RESOLV'
nameserver 8.8.8.8
nameserver 8.8.4.4
nameserver 1.1.1.1
RESOLV
        
        if nslookup github.com > /dev/null 2>&1; then
            log_success "DNS fixed with direct nameservers"
        else
            log_error "DNS still not working, installation may fail"
        fi
    else
        log_success "DNS fixed by restarting systemd-resolved"
    fi
else
    log_success "DNS working after RKE2"
fi

# Configure kubectl
mkdir -p ~/.kube
sudo cp /etc/rancher/rke2/rke2.yaml ~/.kube/config
sudo chown $(id -u):$(id -g) ~/.kube/config
chmod 600 ~/.kube/config

# Fix kubeconfig server address to use node IP instead of 127.0.0.1
sed -i "s|https://127.0.0.1:6443|https://${DETECTED_IP}:6443|g" ~/.kube/config
log_info "Kubeconfig configured to use ${DETECTED_IP}:6443"

# Wait for node to be ready
log_info "Waiting for Kubernetes node to be ready..."
for i in {1..60}; do
    if kubectl get nodes | grep -q "Ready"; then
        break
    fi
    sleep 5
done

    log_success "RKE2 Kubernetes installed"
fi

# Step 5: Generate cdev configuration
log_info "Step 5/7: Generating cluster.dev configuration..."

# Detect latest kube-dc version from Docker Hub
log_info "Checking Docker Hub for latest kube-dc version..."

# Function to get latest tag from Docker Hub
get_latest_docker_tag() {
    local repo=$1
    local response=$(curl -s --max-time 10 "https://registry.hub.docker.com/v2/repositories/${repo}/tags?page_size=100")
    
    # Extract version tags including dev versions
    local tag=$(echo "$response" | \
        grep -oE '"name"[[:space:]]*:[[:space:]]*"v[0-9]+\.[0-9]+\.[0-9]+(-dev[0-9]+)?"' | \
        grep -oE 'v[0-9]+\.[0-9]+\.[0-9]+(-dev[0-9]+)?' | \
        sort -V | \
        tail -1)
    
    echo "$tag"
}

# Get latest tags for each component
MANAGER_TAG=$(get_latest_docker_tag "shalb/kube-dc-manager")
FRONTEND_TAG=$(get_latest_docker_tag "shalb/kube-dc-ui-frontend")
BACKEND_TAG=$(get_latest_docker_tag "shalb/kube-dc-ui-backend")

# Use stable chart version, dev versions for images
KUBE_DC_CHART_VERSION="v0.1.34"  # Helm chart version (stable)

if [ -n "$MANAGER_TAG" ] && [ "$MANAGER_TAG" != "" ]; then
    log_success "Latest image versions from Docker Hub:"
    log_info "  Manager: ${MANAGER_TAG}"
    log_info "  Frontend: ${FRONTEND_TAG}"
    log_info "  Backend: ${BACKEND_TAG}"
    log_info "  Chart: ${KUBE_DC_CHART_VERSION} (stable)"
else
    # Fallback: use chart version for images too
    MANAGER_TAG="$KUBE_DC_CHART_VERSION"
    FRONTEND_TAG="$KUBE_DC_CHART_VERSION"
    BACKEND_TAG="$KUBE_DC_CHART_VERSION"
    log_warning "Could not fetch from Docker Hub, using default: ${KUBE_DC_CHART_VERSION}"
fi

# Download templates from GitHub public repository
log_info "Downloading Kube-DC templates from GitHub (main branch)..."
if [ ! -f "./templates/template.yaml" ]; then
    # Remove incomplete templates directory if it exists
    rm -rf ./templates
    
    # Download using curl/wget for simplicity
    log_info "Fetching templates archive..."
    mkdir -p ./templates
    cd ./templates
    
    # Download the specific directory as tarball
    curl -sL https://github.com/kube-dc/kube-dc-public/archive/refs/heads/main.tar.gz | \
        tar -xz --strip-components=5 kube-dc-public-main/installer/kube-dc/templates/kube-dc
    
    # Verify templates downloaded correctly
    if [ -f "template.yaml" ]; then
        log_success "Templates downloaded successfully"
    else
        cd ..
        rm -rf ./templates
        log_error "Failed to download templates - template.yaml not found"
        exit 1
    fi
    cd ..
else
    log_info "Templates already exist, skipping download"
fi

# Create project.yaml
cat > project.yaml << PROJYAML
kind: Project
name: kube-dc-deployment
backend: "default"
variables:
  kubeconfig: ~/.kube/config
  debug: true
PROJYAML

# Create stack.yaml using local templates
TEMPLATE_PATH="./templates/"
log_info "Using local templates from: ${TEMPLATE_PATH}"

cat > stack.yaml << STACKYAML
name: cluster
template: ${TEMPLATE_PATH}
kind: Stack
backend: default
variables:
  debug: "true"
  kubeconfig: $HOME/.kube/config
  
  cluster_config:
    pod_cidr: "10.100.0.0/16"
    svc_cidr: "10.101.0.0/16"
    join_cidr: "172.30.0.0/20"
    POD_CIDR: "10.100.0.0/16"
    SVC_CIDR: "10.101.0.0/16"
    JOIN_CIDR: "172.30.0.0/20"
    cluster_dns: "10.101.0.11"
    # Minimal external network config (required by template, but not actually used)
    # Real external networks can be added later via kube-dc-configure.sh
    default_external_network:
      type: cloud
      nodes_list:
        - $(hostname)
      name: ext-cloud
      vlan_id: "4000"
      interface: "lo"
      cidr: "100.64.0.0/16"
      gateway: "100.64.0.1"
      mtu: "1400"
      exclude_ips:
        - "100.64.0.1..100.64.0.100"
    
  node_external_ip: ${DETECTED_IP}
  email: "${EMAIL}"
  domain: "${DOMAIN}"
  install_terraform: true
  
  create_default:
    organization:
      name: ${ORG_NAME}
      description: "Default Organization"
      email: "${EMAIL}"
    project:
      name: ${PROJECT_NAME}
      cidr_block: "10.0.10.0/24"
      egress_network_type: "cloud"
  
  monitoring:
    prom_storage: 20Gi
    retention_size: 17GiB
    retention: 365d
  
  versions:
    kube_dc: "${KUBE_DC_CHART_VERSION}"
    manager: "${MANAGER_TAG}"
    frontend: "${FRONTEND_TAG}"
    backend: "${BACKEND_TAG}"
STACKYAML

log_success "Configuration generated"

# Step 6: Deploy Kube-DC
log_info "Step 6/7: Deploying Kube-DC platform..."
log_warning "This will take 15-20 minutes..."

# Clean any previous failed attempts
if [ -d ".cluster.dev" ]; then
    log_info "Cleaning previous deployment state..."
    rm -rf .cluster.dev
fi

# Run cdev
log_info "Running cdev plan..."
cdev plan

log_info "Running cdev apply..."
cdev apply --force

log_success "Kube-DC deployed"

# Step 7: Post-installation
log_info "Step 7/7: Post-installation tasks..."

# Get credentials
KEYCLOAK_PASSWORD=$(cdev output | grep keycloak_password | awk '{print $3}')
ORG_PASSWORD_CMD=$(cdev output | grep retrieve_organization_password | cut -d'=' -f2-)

log_success "Installation complete!"

# Final summary
log_header "Installation Complete! 🎉"
echo ""
log_success "Kube-DC is now running!"
echo ""
echo "📋 Access Information:"
echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
echo ""
echo "  🌐 Console URL:    https://console.${DOMAIN}"
echo "  🔐 Keycloak URL:   https://login.${DOMAIN}"
echo ""
echo "  👤 Organization:   ${ORG_NAME}"
echo "  📁 Project:        ${PROJECT_NAME}"
echo "  ✉️  Admin Email:    ${EMAIL}"
echo ""
echo "  🔑 Get organization password:"
echo "     ${ORG_PASSWORD_CMD}"
echo ""
echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
echo ""
log_info "What you can do now:"
echo "  ✅ Access the UI and manage the platform"
echo "  ✅ Create VMs with internal networking"
echo "  ✅ Deploy pods and services (ClusterIP, NodePort)"
echo "  ✅ Manage users and organizations"
echo ""
log_info "What's NOT configured yet (can add later):"
echo "  ❌ External VLAN networks"
echo "  ❌ Floating IPs / External IPs"
echo "  ❌ LoadBalancer services with public IPs"
echo "  ❌ Worker nodes"
echo ""
log_info "To add these features, run:"
echo "  ./kube-dc-configure.sh --add-external-network"
echo "  ./kube-dc-configure.sh --add-worker-node"
echo ""
log_success "Happy cloud computing! 🚀"
echo ""
