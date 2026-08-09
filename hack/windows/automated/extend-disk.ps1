# extend-disk.ps1 — baked into the golden as a boot-time scheduled task.
# Enables the MINIMAL-image model: the golden ships at the 64Gi floor; when a
# tenant clones it onto a LARGER disk, this grows C: to fill it on first boot.
# Idempotent: no-op when C: already spans the disk (runs cheaply every boot).
$ErrorActionPreference='SilentlyContinue'
$p=Get-Partition -DriveLetter C
$max=(Get-PartitionSupportedSize -DriveLetter C).SizeMax
if ($p -and $max -gt ($p.Size + 64MB)) {
  Resize-Partition -DriveLetter C -Size $max
  Write-Output "extend-disk: C: grown to $max"
} else { Write-Output "extend-disk: no-op" }
