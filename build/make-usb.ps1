# Writes a NetClone ISO to a physical USB disk on Windows.
#
#   .\make-usb.ps1 -Iso ..\out\netclone-1.0.0-amd64.iso -DiskNumber 3
#
# !!! The selected disk is ERASED completely. !!!
# Run this in an elevated PowerShell (Administrator).

[CmdletBinding()]
param(
    [Parameter(Mandatory = $true)][string]$Iso,
    [Parameter(Mandatory = $true)][int]$DiskNumber,
    [switch]$Yes
)

$ErrorActionPreference = "Stop"

function Assert-Admin {
    $id = [Security.Principal.WindowsIdentity]::GetCurrent()
    $pr = New-Object Security.Principal.WindowsPrincipal($id)
    if (-not $pr.IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)) {
        throw "Yonetici (Administrator) olarak calistirin."
    }
}
Assert-Admin

if (-not (Test-Path -LiteralPath $Iso)) { throw "ISO bulunamadi: $Iso" }
$isoItem = Get-Item -LiteralPath $Iso
$isoStream = [System.IO.File]::OpenRead($isoItem.FullName)

$disk = Get-Disk -Number $DiskNumber -ErrorAction Stop
Write-Host ""
Write-Host "Hedef disk:" -ForegroundColor Yellow
$disk | Format-List Number, FriendlyName, SerialNumber, Size, BusType, IsSystem, IsBoot
Write-Host ("ISO: {0} ({1:N0} bayt)" -f $isoItem.Name, $isoItem.Length)

if ($disk.IsSystem -or $disk.IsBoot) { throw "Bu disk sistem diski, yazma reddedildi." }
if ($isoItem.Length -gt $disk.Size) { throw "ISO diskten buyuk." }

if (-not $Yes) {
    Write-Host ""
    Write-Host "DIKKAT: $DiskNumber numarali diskin TUM VERISI SILINECEK." -ForegroundColor Red
    $answer = Read-Host "Onaylamak icin disk numarasini yazin ($DiskNumber)"
    if ($answer -ne "$DiskNumber") { throw "Iptal edildi." }
}

Write-Host "==> Disk temizleniyor..." -ForegroundColor Cyan
Clear-Disk -Number $DiskNumber -RemoveData -RemoveOEM -Confirm:$false -ErrorAction SilentlyContinue
Update-HostStorageCache

$path = "\\.\PhysicalDrive$DiskNumber"
Write-Host "==> Yaziliyor: $path" -ForegroundColor Cyan

$dst = New-Object System.IO.FileStream($path, [System.IO.FileMode]::Open,
    [System.IO.FileAccess]::Write, [System.IO.FileShare]::None)
try {
    $buf = New-Object byte[] (4 * 1024 * 1024)
    $written = 0L
    $lastReport = 0L
    while (($read = $isoStream.Read($buf, 0, $buf.Length)) -gt 0) {
        # Raw device writes must be sector aligned; pad the final block.
        $len = $read
        if ($len % 512 -ne 0) {
            $pad = 512 - ($len % 512)
            [Array]::Clear($buf, $len, $pad)
            $len += $pad
        }
        $dst.Write($buf, 0, $len)
        $written += $read
        if ($written - $lastReport -ge (64 * 1024 * 1024)) {
            $pct = [math]::Round(($written / $isoItem.Length) * 100, 1)
            Write-Progress -Activity "USB'ye yaziliyor" -Status "$pct%" -PercentComplete $pct
            $lastReport = $written
        }
    }
    $dst.Flush()
}
finally {
    $dst.Close()
    $isoStream.Close()
    Write-Progress -Activity "USB'ye yaziliyor" -Completed
}

Write-Host ""
Write-Host "Tamamlandi. USB'yi cikarip hedef bilgisayarda boot edin." -ForegroundColor Green
