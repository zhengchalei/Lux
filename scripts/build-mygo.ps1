param([switch]$SkipTests, [ValidatePattern('^\d+\.\d+\.\d+$')][string]$Version = '0.2.9')

# build-mygo.ps1 builds Lux's Vue assets and pure Go MyGo desktop executable.
# WebView2 is supplied by Windows rather than bundled with the application.
# Packaging uses an explicit output directory, leaving the prior Rust portable
# version available while the migration is being validated with real client data.
# Source licenses and runtime dependency notices travel with the portable binary,
# so extracting a release preserves attribution independently from GitHub access.

$ErrorActionPreference='Stop'
$workspacePath=Split-Path -Parent $PSScriptRoot
Push-Location $workspacePath
try {
    & npm.cmd --prefix frontend ci
    if ($LASTEXITCODE -ne 0) {throw 'Frontend dependency installation failed'}
    & npm.cmd --prefix frontend run build
    if ($LASTEXITCODE -ne 0) {throw 'Frontend validation or build failed'}
    if (!$SkipTests) {
        & go test ./...
        if ($LASTEXITCODE -ne 0) {throw 'Go regression failed'}
        & go vet ./...
        if ($LASTEXITCODE -ne 0) {throw 'Go vet failed'}
    }
    $portablePath=Join-Path $workspacePath "dist\Lux-$Version-mygo-windows-x64"
    New-Item -ItemType Directory -Path $portablePath -Force | Out-Null
    & go build -trimpath -ldflags '-s -w -H=windowsgui' -o (Join-Path $portablePath 'Lux.exe') .
    if ($LASTEXITCODE -ne 0) {throw 'Desktop build failed'}
    foreach($document in @('README.md','NOTICE.md','LICENSE','THIRD_PARTY_LICENSES.txt')) {
        Copy-Item -LiteralPath (Join-Path $workspacePath $document) -Destination $portablePath -Force
    }
    $portableDocs=Join-Path $portablePath 'docs'
    New-Item -ItemType Directory -Path $portableDocs -Force | Out-Null
    foreach($document in @('architecture.md','mygo-verification.md','ui-product-model.md')) {
        Copy-Item -LiteralPath (Join-Path $workspacePath "docs\$document") -Destination $portableDocs -Force
    }
    Compress-Archive -LiteralPath $portablePath -DestinationPath (Join-Path $workspacePath "dist\Lux-$Version-mygo-windows-x64.zip") -Force
    Get-Item -LiteralPath (Join-Path $portablePath 'Lux.exe'),(Join-Path $workspacePath "dist\Lux-$Version-mygo-windows-x64.zip") | Select-Object FullName,Length
} finally {Pop-Location}
