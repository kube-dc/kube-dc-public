# install-openssh-offline.ps1 — install OpenSSH Server from the BAKED Win32-OpenSSH
# release zip instead of `Add-WindowsCapability -Online`.
#
# WHY THIS EXISTS: Add-WindowsCapability pulls the capability payload from Windows
# Update. On the 2026-08-07 bake that single call ran for 40 minutes and then
# triggered a servicing reboot — and a reboot is fatal here, because the whole bake
# runs as a FirstLogonCommand and FirstLogonCommands DO NOT RE-RUN after a restart.
# The script died mid-flight, sysprep was never reached, and the golden disk was
# left unsealed with no way to resume.
#
# The offline zip removes the dependency entirely: no Windows Update, no servicing
# reboot, seconds instead of forty minutes, and the exact same sshd. This is what
# every image-builder pipeline does.
#
# The zip is fetched and checksummed at ASSETS-IMAGE build time
# (images/windows-bake-assets), not in the guest, so the bake stays reproducible.
param([Parameter(Mandatory = $true)][string]$ZipPath)

$ErrorActionPreference = 'Stop'
$dest = "$env:ProgramFiles\OpenSSH"

Write-Host "installing OpenSSH from $ZipPath"
if (-not (Test-Path $ZipPath)) { throw "OpenSSH zip not found at $ZipPath" }

# The release zip contains a single OpenSSH-Win64\ directory; flatten it into
# %ProgramFiles%\OpenSSH so the paths match the capability layout tenants expect.
$staging = Join-Path $env:TEMP 'openssh-stage'
if (Test-Path $staging) { Remove-Item $staging -Recurse -Force }
Expand-Archive -Path $ZipPath -DestinationPath $staging -Force
$src = Get-ChildItem $staging -Directory | Select-Object -First 1
if (-not $src) { throw 'unexpected OpenSSH zip layout — no top-level directory' }

if (Test-Path $dest) { Remove-Item $dest -Recurse -Force }
Move-Item -Path $src.FullName -Destination $dest -Force

Write-Host 'registering sshd + ssh-agent services'
& powershell.exe -ExecutionPolicy Bypass -NoProfile -File (Join-Path $dest 'install-sshd.ps1')
if ($LASTEXITCODE -ne 0) { throw "install-sshd.ps1 exit $LASTEXITCODE" }

Set-Service -Name sshd -StartupType Automatic
Start-Service sshd
if ((Get-Service sshd).Status -ne 'Running') { throw 'sshd did not start' }

# Default shell: PowerShell, so tenant `ssh vm "command"` behaves like everywhere else.
New-ItemProperty -Path 'HKLM:\SOFTWARE\OpenSSH' -Name DefaultShell `
  -Value "$env:SystemRoot\System32\WindowsPowerShell\v1.0\powershell.exe" `
  -PropertyType String -Force | Out-Null

if (-not (Get-NetFirewallRule -Name 'OpenSSH-Server-In-TCP' -ErrorAction SilentlyContinue)) {
  New-NetFirewallRule -Name 'OpenSSH-Server-In-TCP' -DisplayName 'OpenSSH Server (sshd)' `
    -Enabled True -Direction Inbound -Protocol TCP -Action Allow -LocalPort 22 -Profile Any | Out-Null
}

# ICMP so an operator can tell "VM is down" from "sshd is down".
foreach ($r in @(
    @{ n = 'ICMP-In-IPv4'; p = 'ICMPv4'; t = 8 },
    @{ n = 'ICMP-In-IPv6'; p = 'ICMPv6'; t = 128 })) {
  if (-not (Get-NetFirewallRule -Name $r.n -ErrorAction SilentlyContinue)) {
    New-NetFirewallRule -Name $r.n -DisplayName "$($r.n) echo request" -Enabled True `
      -Direction Inbound -Protocol $r.p -IcmpType $r.t -Action Allow | Out-Null
  }
}

# RDP — tenants get a console without needing a key.
Set-ItemProperty -Path 'HKLM:\System\CurrentControlSet\Control\Terminal Server' `
  -Name fDenyTSConnections -Value 0 -Force
Enable-NetFirewallRule -DisplayGroup 'Remote Desktop' -ErrorAction SilentlyContinue

Write-Host 'OpenSSH offline install complete'
exit 0
