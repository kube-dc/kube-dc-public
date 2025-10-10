#!/bin/bash
#
# Kube-DC Uninstaller
# This script completely removes Kube-DC and RKE2 from the system
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
    echo -e "${RED}━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━${NC}"
    echo -e "${RED}  $1${NC}"
    echo -e "${RED}━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━${NC}"
    echo ""
}

# Banner
clear
cat << "EOF"
╔═══════════════════════════════════════════════════════════════╗
║                                                               ║
║   ██╗  ██╗██╗   ██╗██████╗ ███████╗      ██████╗  ██████╗   ║
║   ██║ ██╔╝██║   ██║██╔══██╗██╔════╝      ██╔══██╗██╔════╝   ║
║   █████╔╝ ██║   ██║██████╔╝█████╗  █████╗██║  ██║██║        ║
║   ██╔═██╗ ██║   ██║██╔══██╗██╔══╝  ╚════╝██║  ██║██║        ║
║   ██║  ██╗╚██████╔╝██████╔╝███████╗      ██████╔╝╚██████╗   ║
║   ╚═╝  ╚═╝ ╚═════╝ ╚═════╝ ╚══════╝      ╚═════╝  ╚═════╝   ║
║                                                               ║
║              Kubernetes Data Center Platform                 ║
║                    UNINSTALLER                               ║
║                                                               ║
╚═══════════════════════════════════════════════════════════════╝

EOF

log_warning "⚠️  WARNING: This will completely remove Kube-DC and RKE2!"
log_warning "⚠️  All data, VMs, and configurations will be DELETED!"
echo ""
read -p "Are you sure you want to continue? (type 'yes' to confirm): " CONFIRM

if [[ "$CONFIRM" != "yes" ]]; then
    log_error "Uninstall cancelled"
    exit 0
fi

echo ""
log_warning "Last chance! Type 'DELETE EVERYTHING' to proceed:"
read -p "> " FINAL_CONFIRM

if [[ "$FINAL_CONFIRM" != "DELETE EVERYTHING" ]]; then
    log_error "Uninstall cancelled"
    exit 0
fi

log_header "Uninstalling Kube-DC"

# Step 1: Stop RKE2
log_info "Step 1/11: Stopping RKE2 services..."
if systemctl is-active --quiet rke2-server.service; then
    sudo systemctl stop rke2-server.service
    log_success "RKE2 server stopped"
else
    log_info "RKE2 server not running"
fi

if systemctl is-active --quiet rke2-agent.service; then
    sudo systemctl stop rke2-agent.service
    log_success "RKE2 agent stopped"
else
    log_info "RKE2 agent not running"
fi

# Step 2: Disable RKE2 services
log_info "Step 2/11: Disabling RKE2 services..."
sudo systemctl disable rke2-server.service 2>/dev/null || true
sudo systemctl disable rke2-agent.service 2>/dev/null || true
log_success "RKE2 services disabled"

# Step 3: Kill any remaining RKE2 processes
log_info "Step 3/11: Killing remaining RKE2 processes..."
sudo pkill -9 -f rke2 2>/dev/null || true
sudo pkill -9 -f containerd 2>/dev/null || true
sudo pkill -9 -f kubelet 2>/dev/null || true
sleep 2
log_success "Processes killed"

# Step 4: Unmount filesystems
log_info "Step 4/11: Unmounting RKE2 filesystems..."
# Find and unmount all RKE2-related mounts
mount | grep -E '/run/k3s|/var/lib/rancher|/var/lib/kubelet' | awk '{print $3}' | while read mount_point; do
    sudo umount -f "$mount_point" 2>/dev/null || true
done
log_success "Filesystems unmounted"

# Step 5: Remove RKE2 directories (alternative: use rke2-uninstall.sh if available)
log_info "Step 5/11: Removing RKE2 directories..."
sudo rm -rf /etc/rancher
sudo rm -rf /var/lib/rancher
sudo rm -rf /var/lib/kubelet
sudo rm -rf /var/lib/cni
sudo rm -rf /run/k3s
sudo rm -rf /run/flannel
sudo rm -rf /opt/cni
sudo rm -rf /etc/cni
log_success "RKE2 directories removed"

# Step 6: Remove RKE2 binaries
log_info "Step 6/11: Removing RKE2 binaries..."
sudo rm -rf /usr/local/bin/rke2*
sudo rm -rf /usr/local/lib/systemd/system/rke2*
sudo systemctl daemon-reload
log_success "RKE2 binaries removed"

# Step 7: Clean network interfaces
log_info "Step 7/11: Cleaning network interfaces..."

# Stop OVS/OVN services first
sudo systemctl stop openvswitch-switch 2>/dev/null || true
sudo systemctl stop ovn-controller 2>/dev/null || true

# Remove OVN/OVS interfaces
for iface in ovn0 ovs-system br-int; do
    sudo ip link delete "$iface" 2>/dev/null || true
done

# Remove CNI/Kube-OVN interfaces
for iface in $(ip link show | grep -E 'cni|flannel|veth|kube|ovn|geneve' | awk -F: '{print $2}' | tr -d ' '); do
    sudo ip link delete "$iface" 2>/dev/null || true
done

# Clean OVS bridges
sudo ovs-vsctl --if-exists del-br br-int 2>/dev/null || true

log_success "Network interfaces cleaned"

# Step 8: Remove OVN/OVS data
log_info "Step 8/11: Removing OVN/OVS data..."
sudo rm -rf /var/run/openvswitch
sudo rm -rf /var/run/ovn
sudo rm -rf /etc/openvswitch
sudo rm -rf /etc/ovn
sudo rm -rf /etc/origin/openvswitch
sudo rm -rf /etc/origin/ovn
sudo rm -rf /var/lib/openvswitch
sudo rm -rf /var/lib/ovn
sudo rm -rf /var/log/openvswitch
sudo rm -rf /var/log/ovn
sudo rm -rf /var/log/kube-ovn
sudo rm -rf /etc/cni/net.d/00-kube-ovn.conflist
sudo rm -rf /etc/cni/net.d/01-kube-ovn.conflist
log_success "OVN/OVS data removed"

# Step 9: Clean iptables rules
log_info "Step 9/11: Cleaning iptables rules..."
sudo iptables -F 2>/dev/null || true
sudo iptables -X 2>/dev/null || true
sudo iptables -t nat -F 2>/dev/null || true
sudo iptables -t nat -X 2>/dev/null || true
sudo iptables -t mangle -F 2>/dev/null || true
sudo iptables -t mangle -X 2>/dev/null || true
log_success "iptables rules cleaned"

# Step 10: Remove installation directories
log_info "Step 10/11: Removing installation directories..."
rm -rf ~/kube-dc-install
rm -rf ~/kube-dc-templates
rm -rf ~/.kube
rm -rf ~/.cluster.dev
rm -rf ~/.cdev
log_success "Installation directories removed"

# Step 11: Clean hostname entries
log_info "Step 11/11: Cleaning hostname entries..."
CURRENT_HOSTNAME=$(hostname)
if grep -q "${CURRENT_HOSTNAME}" /etc/hosts 2>/dev/null; then
    sudo sed -i "/${CURRENT_HOSTNAME}/d" /etc/hosts
    # Re-add localhost if needed
    if ! grep -q "127.0.0.1.*localhost" /etc/hosts; then
        echo "127.0.0.1 localhost" | sudo tee -a /etc/hosts > /dev/null
    fi
    log_success "Hostname entries cleaned"
else
    log_info "No hostname entries to clean"
fi

# Optional: Clean system configuration
log_info "Cleaning system configuration..."

# Remove sysctl changes (optional - comment out if you want to keep them)
if grep -q "Kube-DC optimizations" /etc/sysctl.conf 2>/dev/null; then
    sudo sed -i '/# Kube-DC optimizations/,+3d' /etc/sysctl.conf
    log_success "Sysctl configuration cleaned"
fi

# Remove kernel modules from autoload (optional)
if grep -q "nf_conntrack" /etc/modules 2>/dev/null; then
    sudo sed -i '/nf_conntrack/d' /etc/modules
    log_success "Kernel modules configuration cleaned"
fi

# Optional: Restore DNS if we modified it
if [ -f /etc/resolv.conf ] && ! [ -L /etc/resolv.conf ]; then
    log_info "Restoring systemd-resolved DNS..."
    sudo rm -f /etc/resolv.conf
    sudo ln -sf /run/systemd/resolve/stub-resolv.conf /etc/resolv.conf
    sudo systemctl restart systemd-resolved 2>/dev/null || true
    log_success "DNS restored"
fi

# Final cleanup
log_header "Uninstall Complete"
echo ""
log_success "Kube-DC and RKE2 have been completely removed!"
echo ""
log_info "What was removed:"
echo "  ✅ RKE2 Kubernetes cluster"
echo "  ✅ All Kube-DC components"
echo "  ✅ All VMs and workloads"
echo "  ✅ Network configurations"
echo "  ✅ Installation directories"
echo ""
log_info "System state:"
echo "  • RKE2 services: Stopped and disabled"
echo "  • Kubernetes data: Deleted"
echo "  • Network interfaces: Cleaned"
echo "  • Configuration files: Removed"
echo ""
log_warning "Note: The following were NOT removed:"
echo "  • Installed packages (kubectl, cdev, etc.)"
echo "  • System packages from apt-get"
echo "  • User data in /home"
echo ""
log_info "To remove additional packages, run:"
echo "  sudo apt-get remove kubectl"
echo "  rm -rf ~/.cdev"
echo ""
log_success "System is ready for a fresh installation!"
echo ""
