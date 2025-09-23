# Install OpenSSH on Windows 11 via PowerShell
# Run this script as Administrator

Write-Host "Checking OpenSSH availability..."
Get-WindowsCapability -Online | Where-Object Name -like 'OpenSSH*'

Write-Host "Installing OpenSSH Client..."
Add-WindowsCapability -Online -Name OpenSSH.Client~~~~0.0.1.0

Write-Host "Installing OpenSSH Server..."
Add-WindowsCapability -Online -Name OpenSSH.Server~~~~0.0.1.0

Write-Host "Starting SSH service..."
Start-Service sshd

Write-Host "Setting SSH service to start automatically..."
Set-Service -Name sshd -StartupType 'Automatic'

Write-Host "Configuring Windows Firewall..."

# SSH Firewall Rule (with Public profile support)
if (!(Get-NetFirewallRule -Name "OpenSSH-Server-In-TCP" -ErrorAction SilentlyContinue | Select-Object Name, Enabled)) {
    Write-Output "Creating firewall rule for SSH (all profiles including Public)..."
    New-NetFirewallRule -Name 'OpenSSH-Server-In-TCP' -DisplayName 'OpenSSH Server (sshd)' -Enabled True -Direction Inbound -Protocol TCP -Action Allow -LocalPort 22 -Profile Any
} else {
    Write-Output "SSH firewall rule already exists."
}

# ICMP (Ping) Firewall Rules
Write-Host "Configuring ICMP (Ping) firewall rules..."
if (!(Get-NetFirewallRule -Name "ICMP-In-IPv4" -ErrorAction SilentlyContinue | Select-Object Name, Enabled)) {
    Write-Output "Creating firewall rule for ICMP IPv4 (Ping)..."
    New-NetFirewallRule -Name 'ICMP-In-IPv4' -DisplayName 'ICMP Allow incoming V4 echo request' -Enabled True -Direction Inbound -Protocol ICMPv4 -IcmpType 8 -Action Allow
} else {
    Write-Output "ICMP IPv4 firewall rule already exists."
}

if (!(Get-NetFirewallRule -Name "ICMP-In-IPv6" -ErrorAction SilentlyContinue | Select-Object Name, Enabled)) {
    Write-Output "Creating firewall rule for ICMP IPv6 (Ping)..."
    New-NetFirewallRule -Name 'ICMP-In-IPv6' -DisplayName 'ICMP Allow incoming V6 echo request' -Enabled True -Direction Inbound -Protocol ICMPv6 -IcmpType 128 -Action Allow
} else {
    Write-Output "ICMP IPv6 firewall rule already exists."
}

# RDP Configuration and Firewall Rules
Write-Host "Configuring Remote Desktop (RDP)..."
try {
    # Enable Remote Desktop
    Set-ItemProperty -Path 'HKLM:\System\CurrentControlSet\Control\Terminal Server' -name "fDenyTSConnections" -value 0
    Write-Output "Remote Desktop enabled in registry."
    
    # Enable RDP service
    Enable-NetFirewallRule -DisplayGroup "Remote Desktop"
    Write-Output "Remote Desktop firewall group enabled."
    
    # Create custom RDP firewall rule (all profiles including Public)
    if (!(Get-NetFirewallRule -Name "RDP-In-Custom" -ErrorAction SilentlyContinue | Select-Object Name, Enabled)) {
        Write-Output "Creating custom firewall rule for RDP (all profiles including Public)..."
        New-NetFirewallRule -Name 'RDP-In-Custom' -DisplayName 'Remote Desktop (RDP-In Custom)' -Enabled True -Direction Inbound -Protocol TCP -Action Allow -LocalPort 3389 -Profile Any
    } else {
        Write-Output "Custom RDP firewall rule already exists."
    }
    
    # Start and configure Remote Desktop Services
    Set-Service -Name "TermService" -StartupType Automatic
    Start-Service -Name "TermService"
    Write-Output "Remote Desktop service started and set to automatic."
    
} catch {
    Write-Warning "Error configuring RDP: $($_.Exception.Message)"
}

Write-Host "Verifying services status..."
Get-Service sshd | Select-Object Name, Status, StartType
Get-Service TermService | Select-Object Name, Status, StartType

Write-Host "Verifying firewall rules..."
Get-NetFirewallRule -Name "OpenSSH-Server-In-TCP" | Select-Object Name, DisplayName, Enabled, Direction, Profile
Get-NetFirewallRule -Name "RDP-In-Custom" | Select-Object Name, DisplayName, Enabled, Direction, Profile
Get-NetFirewallRule -Name "ICMP-In-IPv4" | Select-Object Name, DisplayName, Enabled, Direction
Get-NetFirewallRule -Name "ICMP-In-IPv6" | Select-Object Name, DisplayName, Enabled, Direction

Write-Host "Getting network interface information..."
Get-NetIPAddress | Where-Object {$_.AddressFamily -eq "IPv4" -and $_.IPAddress -ne "127.0.0.1"} | Select-Object IPAddress, InterfaceAlias

Write-Host ""
Write-Host "============================================"
Write-Host "Windows Remote Access Configuration Completed!"
Write-Host ""
Write-Host "✅ SSH Server: Running and enabled (port 22)"
Write-Host "✅ Remote Desktop: Enabled and configured (port 3389)"
Write-Host "✅ ICMP Ping: Enabled (IPv4 and IPv6)"
Write-Host "✅ Firewall Rules: Created with Public profile support"
Write-Host ""
Write-Host "Services Status:"
Write-Host "- OpenSSH Server (sshd): Automatic startup"
Write-Host "- Terminal Services (TermService): Automatic startup"
Write-Host ""
Write-Host "Network Access:"
Write-Host "- SSH: ssh username@<VM_IP>"
Write-Host "- RDP: mstsc /v:<VM_IP>"
Write-Host "- Ping: ping <VM_IP>"
Write-Host ""
Write-Host "Optional: Additional services (run if needed):"
Write-Host "# Enable WinRM (ports 5985/5986):"
Write-Host "New-NetFirewallRule -Name 'WinRM-HTTP-In' -DisplayName 'WinRM HTTP-In' -Enabled True -Direction Inbound -Protocol TCP -Action Allow -LocalPort 5985 -Profile Any"
Write-Host "New-NetFirewallRule -Name 'WinRM-HTTPS-In' -DisplayName 'WinRM HTTPS-In' -Enabled True -Direction Inbound -Protocol TCP -Action Allow -LocalPort 5986 -Profile Any"
Write-Host ""
Write-Host "QEMU Guest Agent: No additional firewall rules needed (uses virtio-serial)"
Write-Host "============================================"
