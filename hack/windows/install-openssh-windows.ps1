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
if (!(Get-NetFirewallRule -Name "OpenSSH-Server-In-TCP" -ErrorAction SilentlyContinue | Select-Object Name, Enabled)) {
    Write-Output "Creating firewall rule for SSH..."
    New-NetFirewallRule -Name 'OpenSSH-Server-In-TCP' -DisplayName 'OpenSSH Server (sshd)' -Enabled True -Direction Inbound -Protocol TCP -Action Allow -LocalPort 22
} else {
    Write-Output "Firewall rule already exists."
}

Write-Host "Verifying SSH service status..."
Get-Service sshd

Write-Host "OpenSSH installation completed!"
