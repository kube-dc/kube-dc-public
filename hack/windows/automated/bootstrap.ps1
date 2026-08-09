# bootstrap.ps1 — the ENTIRE in-guest build, run as ONE FirstLogonCommand.
#
# WHY ONE SCRIPT: <Order> in FirstLogonCommands does NOT serialize on modern
# Windows — the commands can run concurrently, so sysprep could seal while
# OpenSSH/QGA/cloudbase-init were still installing (codex review CRITICAL,
# docs/prd/windows-bake-review-2026-08-06.md). A single script is strictly
# ordered and fail-closed: any step that fails writes C:\BUILD_FAILED and we do
# NOT seal, so a broken image can never be published.
#
# ORDER: drivers already loaded by Setup -> QGA -> OpenSSH -> cloudbase-init ->
#        (optional) Windows Update -> cleanup -> sysprep(generalize, via
#        cloudbase-init's Unattend.xml so CLONES run cloudbase-init at OOBE).
$ErrorActionPreference = 'Stop'
$log = 'C:\bootstrap.log'
function Log($m) { "$(Get-Date -Format o)  $m" | Tee-Object -FilePath $log -Append }
function Die($m) {
  Log "FATAL: $m"
  Set-Content -Path 'C:\BUILD_FAILED' -Value $m -Force
  exit 1
}
function Find-OnDrives($rel) {
  # Build the path by STRING CONCATENATION, not Join-Path. Join-Path validates the
  # drive and throws "Cannot find drive. A drive with the name 'H' does not exist."
  # — and with $ErrorActionPreference = 'Stop' that is fatal. It only bites on a
  # lookup that is NOT satisfied by an earlier drive, so it stayed hidden until the
  # 2026-08-08 bake reached the OPTIONAL apply-updates.enabled marker, which is
  # deliberately absent. Everything before it had been found on D:-G:, and the bake
  # died one step short of sealing.
  foreach ($d in @('D:','E:','F:','G:','H:','I:')) {
    $p = "$d\$rel"
    if (Test-Path -LiteralPath $p -ErrorAction SilentlyContinue) { return $p }
  }
  return $null
}

# RESUME SAFETY NET. This script runs as a FirstLogonCommand, and Windows does not
# re-run those after a restart — so a single servicing reboot (Windows Update, a
# driver install, anything) silently ends the bake with the golden disk unsealed and
# no error anywhere. That is exactly what happened on 2026-08-07.
#
# Registering a SYSTEM scheduled task that re-launches this script at every boot
# makes a reboot survivable: the machine comes back up, the task fires without
# needing anyone to log in, and the script picks up again. Every step here is
# idempotent (service checks, Test-Path guards, MSI reinstalls), so re-running is
# safe. The task is deleted immediately before sysprep so it never reaches the
# golden image.
$resumeTask = 'kube-dc-bake-resume'
function Register-Resume($self) {
  $a = New-ScheduledTaskAction -Execute 'powershell.exe' `
    -Argument "-ExecutionPolicy Bypass -NoProfile -File `"$self`""
  $t = New-ScheduledTaskTrigger -AtStartup
  $p = New-ScheduledTaskPrincipal -UserId 'SYSTEM' -RunLevel Highest
  $s = New-ScheduledTaskSettingsSet -AllowStartIfOnBatteries -DontStopIfGoingOnBatteries `
    -ExecutionTimeLimit ([TimeSpan]::FromHours(4))
  Register-ScheduledTask -TaskName $resumeTask -Action $a -Trigger $t -Principal $p `
    -Settings $s -Force | Out-Null
}
function Unregister-Resume {
  Unregister-ScheduledTask -TaskName $resumeTask -Confirm:$false -ErrorAction SilentlyContinue
}

try {
  Log "=== kube-dc Windows golden bootstrap ==="
  # $PSCommandPath is on the read-only assets CDROM, whose drive letter can move
  # across a reboot; copy to a fixed local path so the resume task always resolves.
  $selfLocal = 'C:\kube-dc-bootstrap.ps1'
  # On a RESUMED run we are already executing C:\kube-dc-bootstrap.ps1, and copying a
  # file onto itself is a terminating error in PowerShell — which killed the
  # 2026-08-08 bake three seconds after the resume task correctly relaunched it.
  # The QGA installer reboots the machine, so this path is the normal one, not an
  # edge case.
  if ($PSCommandPath -ne $selfLocal) {
    Copy-Item -Path $PSCommandPath -Destination $selfLocal -Force
  }
  Register-Resume $selfLocal
  Log "resume task '$resumeTask' registered — a servicing reboot will not lose this bake"

  # ---------- 1. QEMU guest agent (AgentConnected + guestOSInfo) ----------
  # NB: the agent ALSO needs the vioserial driver — Setup installs it from the
  # virtio ISO (autounattend DriverPaths). Without vioserial the service runs but
  # KubeVirt never sees it: that is the exact defect of the 2026-07-09 golden.
  $ga = Find-OnDrives 'guest-agent\qemu-ga-x86_64.msi'
  if (-not $ga) { $ga = Find-OnDrives 'guest-agent\qemu-ga-x64.msi' }
  if (-not $ga) { Die 'qemu-ga MSI not found on any drive (virtio ISO missing?)' }
  Log "installing QGA from $ga"
  $p = Start-Process msiexec.exe -ArgumentList "/i `"$ga`" /qn /norestart" -Wait -PassThru
  if ($p.ExitCode -ne 0) { Die "qemu-ga MSI exit $($p.ExitCode)" }
  $svc = Get-Service QEMU-GA -ErrorAction SilentlyContinue
  if (-not $svc) { $svc = Get-Service 'QEMU Guest Agent' -ErrorAction SilentlyContinue }
  if (-not $svc) { Die 'QEMU-GA service missing after install' }
  Set-Service -Name $svc.Name -StartupType Automatic
  Start-Service -Name $svc.Name
  Log "QGA service '$($svc.Name)' running"

  # ---------- 2. OpenSSH + RDP (capabilities tenants rely on) ----------
  # OFFLINE FIRST. `Add-WindowsCapability -Online` fetches from Windows Update, and
  # on the 2026-08-07 bake it ran 40 minutes and then triggered a servicing reboot —
  # which is fatal, because this whole script is a FirstLogonCommand and those DO NOT
  # RE-RUN after a restart. The bake died before sysprep with an unsealed disk.
  # The baked Win32-OpenSSH zip has no such dependency. The capability path is kept
  # only as a fallback for media that predates the zip.
  $sshZip = Find-OnDrives 'OpenSSH-Win64.zip'
  $sshOff = Find-OnDrives 'install-openssh-offline.ps1'
  if ($sshZip -and $sshOff) {
    Log "installing OpenSSH offline from $sshZip"
    & powershell -ExecutionPolicy Bypass -NoProfile -File $sshOff -ZipPath $sshZip 2>&1 | Tee-Object -FilePath $log -Append
    if ($LASTEXITCODE -ne 0) { Die "offline openssh install exit $LASTEXITCODE" }
  } else {
    Log 'WARNING: no baked OpenSSH zip — falling back to Windows Update (may reboot and kill this bake)'
    $ssh = Find-OnDrives 'install-openssh-windows.ps1'
    if (-not $ssh) { Die 'no OpenSSH installer found on the attached media' }
    Log "running $ssh"
    & powershell -ExecutionPolicy Bypass -NoProfile -File $ssh 2>&1 | Tee-Object -FilePath $log -Append
    if ($LASTEXITCODE -ne 0) { Die "openssh script exit $LASTEXITCODE" }
  }
  if (-not (Get-Service sshd -ErrorAction SilentlyContinue)) { Die 'sshd service absent after install' }
  Set-Service -Name sshd -StartupType Automatic
  # Verify RDP was actually enabled (the script swallows some errors).
  $deny = (Get-ItemProperty 'HKLM:\System\CurrentControlSet\Control\Terminal Server' -Name fDenyTSConnections).fDenyTSConnections
  if ($deny -ne 0) { Die 'RDP still disabled (fDenyTSConnections != 0)' }
  # CRITICAL: Windows OpenSSH IGNORES %USERPROFILE%\.ssh\authorized_keys for any
  # account in the Administrators group — it reads
  # C:\ProgramData\ssh\administrators_authorized_keys instead. cloudbase-init
  # writes the PER-USER file, and our tenant account (kube-dc) is an
  # Administrator, so without this the injected key lands where sshd will never
  # look and key auth silently fails (codex re-review). Disable the admin
  # override so the per-user file is authoritative for everyone.
  $sshdCfg = 'C:\ProgramData\ssh\sshd_config'
  if (-not (Test-Path $sshdCfg)) { Die "sshd_config missing at $sshdCfg" }
  $cfg = Get-Content $sshdCfg
  $cfg = $cfg | ForEach-Object {
    if ($_ -match '^\s*Match\s+Group\s+administrators') { "#$_" }
    elseif ($_ -match 'administrators_authorized_keys')    { "#$_" }
    else { $_ }
  }
  Set-Content -Path $sshdCfg -Value $cfg -Encoding ASCII -Force
  if (Select-String -Path $sshdCfg -Pattern '^\s*Match\s+Group\s+administrators' -Quiet) {
    Die 'failed to disable the administrators_authorized_keys override in sshd_config'
  }
  Restart-Service sshd -ErrorAction SilentlyContinue
  Log 'sshd_config: per-user authorized_keys now authoritative for admins'
  Log 'OpenSSH + RDP verified' 

  # ---------- 3. cloudbase-init (the Windows cloud-init) ----------
  # THIS is how tenants get their SSH key, password, hostname and disk growth.
  # QEMU's guest-ssh-add-authorized-keys is Unix-only, so the guest agent can
  # NEVER inject keys on Windows (codex review BLOCKER) — cloudbase-init reads
  # the cloudInitNoCloud disk KubeVirt attaches and does it properly.
  # The MSI is BAKED into the windows-bake-assets containerDisk (an ISO attached
  # as a CDROM), not pushed through a Secret/ConfigMap (~1 MiB cap) and not
  # downloaded here — a guest-side download would make the bake depend on
  # upstream availability and on guest egress, and would not be reproducible.
  $cbi = Find-OnDrives 'CloudbaseInitSetup_x64.msi'
  if (-not $cbi) { Die 'CloudbaseInitSetup_x64.msi not found — is the windows-bake-assets containerDisk attached?' }
  # The assets image records the hash it downloaded; enforce it if present.
  $shaFile = Find-OnDrives 'cloudbase-init.sha256'
  if ($shaFile) {
    $want = (Get-Content $shaFile -Raw).Trim().ToLower()
    $got  = (Get-FileHash -Path $cbi -Algorithm SHA256).Hash.ToLower()
    if ($got -ne $want) { Die "cloudbase-init sha256 mismatch: got $got want $want" }
    Log 'cloudbase-init sha256 verified against the baked manifest'
  }
  Log "installing cloudbase-init from $cbi"
  # RUN_SERVICE_AS_LOCAL_SYSTEM: needed to create users / resize volumes.
  $p = Start-Process msiexec.exe -ArgumentList "/i `"$cbi`" /qn /norestart RUN_SERVICE_AS_LOCAL_SYSTEM=1" -Wait -PassThru
  if ($p.ExitCode -ne 0) { Die "cloudbase-init MSI exit $($p.ExitCode)" }
  $cbDir = 'C:\Program Files\Cloudbase Solutions\Cloudbase-Init'
  if (-not (Test-Path $cbDir)) { Die 'cloudbase-init install dir missing' }

  # Config: NoCloud datasource (what KubeVirt's cloudInitNoCloud disk presents),
  # create the catalog cloudUser, inject its SSH keys, set hostname, EXTEND the
  # system volume (this replaces extend-disk.ps1 — the minimal-image model now
  # rides on cloudbase-init's own ExtendVolumesPlugin).
  $conf = @"
[DEFAULT]
username=kube-dc
groups=Administrators
inject_user_password=true
config_drive_raw_hhd=true
config_drive_cdrom=true
config_drive_vfat=true
bsdtar_path=$cbDir\bin\bsdtar.exe
mtools_path=$cbDir\bin\
verbose=true
logdir=$cbDir\log\
logfile=cloudbase-init.log
local_scripts_path=$cbDir\LocalScripts\
metadata_services=cloudbaseinit.metadata.services.nocloudservice.NoCloudConfigDriveService,cloudbaseinit.metadata.services.configdrive.ConfigDriveService
plugins=cloudbaseinit.plugins.common.mtu.MTUPlugin,cloudbaseinit.plugins.windows.extendvolumes.ExtendVolumesPlugin,cloudbaseinit.plugins.common.sethostname.SetHostNamePlugin,cloudbaseinit.plugins.windows.createuser.CreateUserPlugin,cloudbaseinit.plugins.common.setuserpassword.SetUserPasswordPlugin,cloudbaseinit.plugins.common.sshpublickeys.SetUserSSHPublicKeysPlugin,cloudbaseinit.plugins.common.userdata.UserDataPlugin
allow_reboot=false
stop_service_on_exit=false
check_latest_version=false
"@
  Set-Content -Path "$cbDir\conf\cloudbase-init.conf" -Value $conf -Encoding ASCII -Force
  Set-Content -Path "$cbDir\conf\cloudbase-init-unattend.conf" -Value $conf -Encoding ASCII -Force
  Log 'cloudbase-init configured (NoCloud + user/password/sshkeys/hostname/extend-volumes)'

  # ---------- 4. optional Windows Update pass ----------
  $upd = Find-OnDrives 'apply-updates.ps1'
  if ($upd -and (Find-OnDrives 'apply-updates.enabled')) {
    Log 'running Windows Update pass'
    & powershell -ExecutionPolicy Bypass -NoProfile -File $upd 2>&1 | Tee-Object -FilePath $log -Append
    if (Test-Path 'C:\UPDATE_FAILED') { Die 'Windows Update pass failed — not sealing' }
  } else { Log 'update pass skipped (no apply-updates.enabled marker)' }

  # ---------- 5. cleanup BEFORE sealing ----------
  # Cached answer file: if left, clones re-run the BUILD answer at OOBE
  # (fixed hostname, AutoLogon, another sysprep) — codex BLOCKER.
  foreach ($f in @('C:\Windows\Panther\unattend.xml','C:\Windows\Panther\autounattend.xml','C:\Windows\System32\Sysprep\unattend.xml')) {
    if (Test-Path $f) { Remove-Item $f -Force; Log "removed cached answer $f" }
  }
  # SSH host keys are generated on first sshd start — leaving them makes EVERY
  # tenant VM share one host identity (codex CRITICAL). sshd regenerates on boot.
  Stop-Service sshd -ErrorAction SilentlyContinue
  Get-ChildItem 'C:\ProgramData\ssh\ssh_host_*' -ErrorAction SilentlyContinue | Remove-Item -Force
  Log 'removed baked SSH host keys'

  # The resume task exists only to survive a reboot DURING the bake. It must never
  # reach the golden image, or every tenant clone would boot running a bake script.
  Unregister-Resume
  Remove-Item 'C:\kube-dc-bootstrap.ps1' -Force -ErrorAction SilentlyContinue
  Log 'resume task removed — image will not carry it'

  # BITLOCKER GATE. Windows 11 24H2 encrypts the OS volume automatically during OOBE
  # on hardware with a TPM and Secure Boot — which every build VM has. An encrypted
  # golden is sealed to the BUILD machine's TPM: it cannot be exported, cannot be
  # inspected, and every clone from it is broken. The 2026-08-07 bake shipped exactly
  # that and it was only caught by reading raw bytes off the disk (the partition
  # began "-FVE-FS-" instead of NTFS).
  # autounattend.xml sets PreventDeviceEncryption in specialize to stop it happening.
  # This is the second line of defence: if the volume is encrypted or still
  # decrypting, refuse to seal rather than publish an unusable image.
  $bde = Get-BitLockerVolume -MountPoint 'C:' -ErrorAction SilentlyContinue
  if ($bde) {
    if ($bde.VolumeStatus -ne 'FullyDecrypted' -or $bde.ProtectionStatus -ne 'Off') {
      Log "BitLocker active on C: (status=$($bde.VolumeStatus) protection=$($bde.ProtectionStatus)) — decrypting"
      Disable-BitLocker -MountPoint 'C:' -ErrorAction SilentlyContinue | Out-Null
      $deadline = (Get-Date).AddMinutes(90)
      while ((Get-Date) -lt $deadline) {
        $bde = Get-BitLockerVolume -MountPoint 'C:'
        if ($bde.VolumeStatus -eq 'FullyDecrypted') { break }
        Log "  decrypting: $($bde.EncryptionPercentage)% encrypted"
        Start-Sleep -Seconds 60
      }
    }
    $bde = Get-BitLockerVolume -MountPoint 'C:'
    if ($bde.VolumeStatus -ne 'FullyDecrypted') {
      Die "C: is still $($bde.VolumeStatus) — refusing to seal an encrypted golden"
    }
    Log 'BitLocker verified off — C: fully decrypted'
  } else {
    Log 'BitLocker cmdlets unavailable — assuming no device encryption'
  }

  # ---------- 6. seal ----------
  # Use cloudbase-init's OWN Unattend.xml: it wires cloudbase-init into the
  # clone's oobeSystem pass, so a tenant clone boots straight into
  # cloudbase-init (no OOBE prompt, no answer file of ours to maintain).
  $unattend = "$cbDir\conf\Unattend.xml"
  if (-not (Test-Path $unattend)) { Die "cloudbase-init Unattend.xml missing at $unattend" }
  Log 'sysprep /generalize /oobe /shutdown with the cloudbase-init unattend'
  Set-Content -Path 'C:\BUILD_OK' -Value (Get-Date -Format o) -Force
  & C:\Windows\System32\Sysprep\sysprep.exe /generalize /oobe /shutdown "/unattend:$unattend"
} catch {
  Die $_.Exception.Message
}
