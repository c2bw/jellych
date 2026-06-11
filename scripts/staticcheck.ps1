$ErrorActionPreference = "Stop"

$repoRoot = Split-Path -Parent $PSScriptRoot
$cacheRoot = Join-Path $repoRoot ".cache"

$env:GOCACHE = Join-Path $cacheRoot "go-build"
$env:STATICCHECK_CACHE = Join-Path $cacheRoot "staticcheck"

New-Item -ItemType Directory -Force -Path $env:GOCACHE, $env:STATICCHECK_CACHE | Out-Null

if ($args.Count -eq 0) {
  staticcheck ./...
} else {
  staticcheck @args
}

if ($LASTEXITCODE -ne 0) {
  exit $LASTEXITCODE
}
