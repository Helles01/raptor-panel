# Starts, stops or checks the local test database (MariaDB from D:\Pluginok\tools, listening on 127.0.0.1:3306 only).
# The credentials are in .raptor-test.env, which is not in git. Usage: scripts\testdb.ps1 start|stop|status
param([Parameter(Mandatory = $true)][ValidateSet('start', 'stop', 'status')][string]$Action)
$ErrorActionPreference = 'Stop'
$bin = 'D:\Pluginok\tools\mariadb-11.8.9-winx64\bin'
$data = 'D:\Pluginok\tools\mariadb-data'
$envFile = Join-Path (Split-Path $PSScriptRoot -Parent) '.raptor-test.env'
function Get-RootPassword { [regex]::Match((Get-Content $envFile -Raw), '(?m)^MARIADB_ROOT_PASSWORD=(.+)$').Groups[1].Value.Trim() }
function Test-Up {
    # A native program's messages on stderr must not stop the script.
    $old = $ErrorActionPreference; $ErrorActionPreference = 'Continue'
    & "$bin\mariadb-admin.exe" --protocol=tcp -h 127.0.0.1 -P 3306 ping 2>&1 | Out-Null
    $ok = ($LASTEXITCODE -eq 0)
    $ErrorActionPreference = $old
    $ok
}
switch ($Action) {
    'status' { if (Test-Up) { 'running' } else { 'stopped' } }
    'start' {
        if (Test-Up) { 'already running'; return }
        Start-Process -FilePath "$bin\mariadbd.exe" -ArgumentList "--datadir=$data", '--bind-address=127.0.0.1', '--port=3306', '--character-set-server=utf8mb4', '--collation-server=utf8mb4_unicode_ci', '--console' -WindowStyle Hidden -RedirectStandardError 'D:\Pluginok\tools\mariadb.log' | Out-Null
        for ($i = 0; $i -lt 60; $i++) { Start-Sleep -Milliseconds 500; if (Test-Up) { 'running'; return } }
        throw 'the database did not start; see D:\Pluginok\tools\mariadb.log'
    }
    'stop' {
        $env:MYSQL_PWD = Get-RootPassword
        try { & "$bin\mariadb-admin.exe" --protocol=tcp -h 127.0.0.1 -P 3306 -u root shutdown } finally { Remove-Item Env:MYSQL_PWD -ErrorAction SilentlyContinue }
        'stopped'
    }
}
