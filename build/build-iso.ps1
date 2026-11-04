# Builds the NetClone ISO on Windows using Docker Desktop.
#
#   .\build-iso.ps1
#
# Requires Docker Desktop (Linux containers) and internet access the first
# time (the builder image downloads Debian packages).

[CmdletBinding()]
param()

$ErrorActionPreference = "Stop"
$root = Split-Path -Parent $PSScriptRoot

if (-not (Get-Command docker -ErrorAction SilentlyContinue)) {
    Write-Host "Docker bulunamadi." -ForegroundColor Red
    Write-Host "Docker Desktop kurun veya ISO'yu bir Debian/Ubuntu makinesinde 'bash build/build-iso.sh' ile olusturun."
    exit 1
}

Write-Host "==> Docker imaji hazirlaniyor (ilk seferde birkac dakika surer)..." -ForegroundColor Cyan
docker build -t netclone-builder "$PSScriptRoot"
if ($LASTEXITCODE -ne 0) { throw "docker build basarisiz" }

Write-Host "==> ISO olusturuluyor..." -ForegroundColor Cyan
docker run --rm --privileged -v "${root}:/work" netclone-builder bash /work/build/build-iso.sh
if ($LASTEXITCODE -ne 0) { throw "ISO build basarisiz (yukaridaki loglara bakin)" }

$iso = Get-ChildItem -Path (Join-Path $root "out") -Filter "netclone-*.iso" |
    Sort-Object LastWriteTime -Descending | Select-Object -First 1
if ($iso) {
    Write-Host ""
    Write-Host "Hazir: $($iso.FullName)" -ForegroundColor Green
    Write-Host "USB'ye yazmak icin: .\build\make-usb.ps1 -Iso `"$($iso.FullName)`" -DiskNumber <N>"
}
