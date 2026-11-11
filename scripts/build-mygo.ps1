param([switch]$SkipTests, [ValidatePattern('^\d+\.\d+\.\d+$')][string]$Version)

# build-mygo.ps1 builds Lux's Vue assets and pure Go MyGo desktop executable.
# WebView2 is supplied by Windows rather than bundled with the application.
# MyGo's pinned CLI embeds the Windows icon, manifest, and version resources.
# A versioned output directory keeps earlier executables available. The release
# artifact is Lux.exe alone; license texts are embedded in its settings page.

$ErrorActionPreference='Stop'
$workspacePath=Split-Path -Parent $PSScriptRoot
Push-Location $workspacePath
try {
    $configuredVersion=(Get-Content -LiteralPath 'mygo.json' -Raw | ConvertFrom-Json).version
    if (!$Version) {$Version=$configuredVersion}
    if ($Version -ne $configuredVersion) {throw 'Version must match mygo.json so executable metadata and release agree'}
    & npm.cmd --prefix frontend ci
    if ($LASTEXITCODE -ne 0) {throw 'Frontend dependency installation failed'}
    & npm.cmd --prefix frontend run build
    if ($LASTEXITCODE -ne 0) {throw 'Frontend validation or build failed'}
    if (!$SkipTests) {
        & node scripts/verify-relationships.cjs
        if ($LASTEXITCODE -ne 0) {throw 'Relationship analysis regression failed'}
        & go test ./...
        if ($LASTEXITCODE -ne 0) {throw 'Go regression failed'}
        & go vet ./...
        if ($LASTEXITCODE -ne 0) {throw 'Go vet failed'}
    }
    $portablePath=Join-Path $workspacePath "dist\Lux-$Version-mygo-windows-x64"
    & go run github.com/egoist/mygo/cmd/mygo@v0.8.3 build -platform windows/amd64 -skip-installer -skip-build-command -o $portablePath
    if ($LASTEXITCODE -ne 0) {throw 'Desktop build failed'}
    Get-Item -LiteralPath (Join-Path $portablePath 'windows-amd64\Lux.exe') | Select-Object FullName,Length
} finally {Pop-Location}
