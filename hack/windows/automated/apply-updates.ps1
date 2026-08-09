# apply-updates.ps1 — invoked by autounattend FirstLogonCommands (Order 3).
#
# GATING: runs only when the sysprep ConfigMap carries an `apply-updates.enabled`
# key (a file that lands next to this script on the sysprep CDROM) — that's how
# the MONTHLY REFRESH build differs from the base build. Env vars can't reach the
# guest, so a marker file is the channel.
#
# FAIL-CLOSED: on a hard update failure it writes C:\UPDATE_FAILED; autounattend
# Order 4 then SKIPS sysprep, the VM stays running, the pipeline watcher times out,
# and no stale "refreshed" golden gets published.
$ErrorActionPreference = 'Stop'
$marker = Join-Path (Split-Path -Parent $MyInvocation.MyCommand.Path) 'apply-updates.enabled'
if (-not (Test-Path $marker)) {
  Write-Output "apply-updates: skipped (no apply-updates.enabled marker -> base build)"
  exit 0
}
try {
  Write-Output "apply-updates: bootstrapping NuGet + PSWindowsUpdate"
  [Net.ServicePointManager]::SecurityProtocol = [Net.SecurityProtocolType]::Tls12
  Install-PackageProvider -Name NuGet -MinimumVersion 2.8.5.201 -Force -ErrorAction SilentlyContinue
  Set-PSRepository -Name PSGallery -InstallationPolicy Trusted -ErrorAction SilentlyContinue
  # VERSION-PINNED: an unpinned Install-Module takes whatever PSGallery serves
  # that day, so two "identical" monthly bakes can differ in their update tooling
  # (codex review MEDIUM). Bump deliberately, like any other dependency.
  Install-Module -Name PSWindowsUpdate -RequiredVersion 2.2.1.5 -Force -Confirm:$false -ErrorAction Stop
  Import-Module PSWindowsUpdate
  Write-Output "apply-updates: installing all updates (no auto-reboot; sysprep follows)"
  # -IgnoreReboot: a single controlled shutdown happens via sysprep afterward.
  Get-WindowsUpdate -AcceptAll -Install -IgnoreReboot -ErrorAction Stop | Out-String | Write-Output
  Write-Output "apply-updates: done"
} catch {
  Write-Output "apply-updates: FAILED — $_"
  New-Item -Path 'C:\UPDATE_FAILED' -ItemType File -Force | Out-Null
  Set-Content -Path 'C:\UPDATE_FAILED' -Value ("{0} | {1}" -f (Get-Date -Format o), $_)
  exit 1
}
