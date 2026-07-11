$ErrorActionPreference = "Stop"

$repoRoot = Split-Path -Parent $PSScriptRoot
$cacheRoot = Join-Path $repoRoot ".cache"

$env:GOCACHE = Join-Path $cacheRoot "go-build"
$env:STATICCHECK_CACHE = Join-Path $cacheRoot "staticcheck"

New-Item -ItemType Directory -Force -Path $env:GOCACHE, $env:STATICCHECK_CACHE | Out-Null

$staticcheckVersion = "2025.1.1"
# Build Staticcheck with the module's Go toolchain. This avoids failures when a
# globally installed binary was compiled by an older Go release.
if ($args.Count -eq 0) {
  & go run "honnef.co/go/tools/cmd/staticcheck@$staticcheckVersion" -- "./..."
} else {
  & go run "honnef.co/go/tools/cmd/staticcheck@$staticcheckVersion" -- @args
}

if ($LASTEXITCODE -ne 0) {
  exit $LASTEXITCODE
}
