# Windows build. Install Go, open PowerShell in this folder, run:
#
#     .\build.ps1
#
# That is the whole build: no C compiler, no Visual Studio, no Node. It writes
# nexora-migrate.exe next to this script. Double-click it and the wizard opens
# in your browser.

$ErrorActionPreference = "Stop"
$env:CGO_ENABLED = "0"
$version = if ($args.Count -gt 0) { $args[0] } else { "dev" }

Write-Host "building nexora-migrate.exe ..."
go build -trimpath -ldflags "-s -w -X main.version=$version" -o nexora-migrate.exe .
Write-Host "done: $((Get-Item nexora-migrate.exe).Length / 1MB -as [int]) MB"
Write-Host "run it with:  .\nexora-migrate.exe"
