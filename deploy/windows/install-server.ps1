param(
  [string]$InstallDir = "C:\Nex-v3",
  [string]$Addr = ":8080",
  [string]$PublicUrl = ""
)

$ErrorActionPreference = "Stop"
$PkgRoot = Split-Path -Parent (Split-Path -Parent $MyInvocation.MyCommand.Path)

New-Item -ItemType Directory -Force -Path "$InstallDir\bin", "$InstallDir\runners", "$InstallDir\data", "$InstallDir\config" | Out-Null
Copy-Item -Force "$PkgRoot\bin\nex-server.exe" "$InstallDir\bin\nex-server.exe"
Copy-Item -Force -Recurse "$PkgRoot\runners\*" "$InstallDir\runners\"
Copy-Item -Force -Recurse "$PkgRoot\config\*" "$InstallDir\config\"

$EnvFile = "$InstallDir\config\nex-server.env"
if (-not (Test-Path $EnvFile)) {
@"
NEX_ADDR=$Addr
NEX_DATA_DIR=$InstallDir\data
NEX_RUNNERS_DIR=$InstallDir\runners
NEX_PUBLIC_URL=$PublicUrl
"@ | Set-Content -Path $EnvFile -Encoding UTF8
}

$RunnerArgs = "-addr `"$Addr`" -data-dir `"$InstallDir\data`" -runners-dir `"$InstallDir\runners`""
if ($PublicUrl -ne "") {
  $RunnerArgs += " -public-url `"$PublicUrl`""
}

$Action = New-ScheduledTaskAction -Execute "$InstallDir\bin\nex-server.exe" -Argument $RunnerArgs -WorkingDirectory $InstallDir
$Trigger = New-ScheduledTaskTrigger -AtStartup
Register-ScheduledTask -TaskName "Nex-v3-Server" -Action $Action -Trigger $Trigger -Force | Out-Null
Start-ScheduledTask -TaskName "Nex-v3-Server"
Write-Host "Nex-v3 Server installed to $InstallDir and started as Scheduled Task 'Nex-v3-Server'."
