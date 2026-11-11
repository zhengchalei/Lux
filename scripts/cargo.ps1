param([Parameter(ValueFromRemainingArguments = $true)][string[]]$CargoArguments)

# cargo.ps1 runs Cargo with the workspace-local Microsoft C++ tools when present.
# The environment applies only to this process and its children. Windows SDK
# headers and libraries are read from the already installed SDK on this machine.
$ErrorActionPreference = 'Stop'
$workspacePath = Split-Path -Parent $PSScriptRoot
$localCompilerRoot = Join-Path $workspacePath '.tools\msvc\VC\Tools\MSVC'
if (Test-Path -LiteralPath $localCompilerRoot) {
    $compilerVersion = Get-ChildItem -LiteralPath $localCompilerRoot -Directory | Sort-Object Name -Descending | Select-Object -First 1
    $compilerBin = Join-Path $compilerVersion.FullName 'bin\Hostx64\x64'
    $sdkRoot = "${env:ProgramFiles(x86)}\Windows Kits\10"
    $sdkVersion = Get-ChildItem -LiteralPath (Join-Path $sdkRoot 'Lib') -Directory | Sort-Object Name -Descending | Select-Object -First 1
    $sdkBin = Join-Path $sdkRoot ('bin\' + $sdkVersion.Name + '\x64')
    $env:PATH = $compilerBin + ';' + $sdkBin + ';' + $env:PATH
    $env:LIB = (Join-Path $compilerVersion.FullName 'lib\x64') + ';' + (Join-Path $sdkVersion.FullName 'um\x64') + ';' + (Join-Path $sdkVersion.FullName 'ucrt\x64')
    $includeRoot = Join-Path $sdkRoot ('Include\' + $sdkVersion.Name)
    $env:INCLUDE = (Join-Path $compilerVersion.FullName 'include') + ';' + (Join-Path $includeRoot 'ucrt') + ';' + (Join-Path $includeRoot 'shared') + ';' + (Join-Path $includeRoot 'um') + ';' + (Join-Path $includeRoot 'winrt')
    $env:CARGO_TARGET_X86_64_PC_WINDOWS_MSVC_LINKER = Join-Path $compilerBin 'link.exe'
    $env:CC = Join-Path $compilerBin 'cl.exe'
    $env:AR = Join-Path $compilerBin 'lib.exe'
    $env:VCToolsInstallDir = $compilerVersion.FullName + '\'
    $env:WindowsSdkDir = $sdkRoot + '\'
    $env:WindowsSDKVersion = $sdkVersion.Name + '\'
}
$env:CARGO_BUILD_JOBS = '4'
$localShaderCompiler = Join-Path $workspacePath '.tools\sdk\fxc.exe'
if (Test-Path -LiteralPath $localShaderCompiler) {
    $env:GPUI_FXC_PATH = $localShaderCompiler
}
Push-Location -LiteralPath $workspacePath
try { & cargo @CargoArguments; exit $LASTEXITCODE } finally { Pop-Location }
