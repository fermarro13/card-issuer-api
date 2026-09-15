#requires -Version 7.0
[CmdletBinding()]
param([string]$Psql='psql')
Set-StrictMode -Version Latest
$ErrorActionPreference='Stop'

function Value-OrDefault([string]$Name,[string]$Default) {
    $value=[Environment]::GetEnvironmentVariable($Name)
    if ([string]::IsNullOrEmpty($value)) { return $Default }
    return $value
}

$controlDatabase=Value-OrDefault 'CONTROL_DATABASE' 'card_issuer_control'
$shardDatabase=Value-OrDefault 'SHARD_DATABASE' 'card_issuer_shard_01'
$shardId=Value-OrDefault 'SHARD_ID' 'shard_01'
foreach ($name in @('CONTROL_DB_PASSWORD','AUTH_DB_PASSWORD','SHARD_DB_PASSWORD','EXECUTOR_DB_PASSWORD')) {
    $password=[Environment]::GetEnvironmentVariable($name)
    if ([string]::IsNullOrEmpty($password) -or $password.Contains([char]0)) { throw "$name must be nonempty and contain no NUL characters." }
}

& (Join-Path $PSScriptRoot 'Invoke-Database.ps1') -Action SetupTest -Psql $Psql -ControlDatabase $controlDatabase -ShardDatabase $shardDatabase -ShardId $shardId
# Passwords are read by psql directly from its environment, never shell-interpolated or passed as arguments.
$runtimeSQL=Join-Path $PSScriptRoot 'compose-runtime.sql'
& $Psql -X -w -q --set=ON_ERROR_STOP=1 --set=VERBOSITY=terse --dbname postgres --file $runtimeSQL
if ($LASTEXITCODE -ne 0) { throw 'Runtime database account provisioning failed.' }

# Existing technical passwords are preserved. Verify the configured credentials instead of silently resetting them.
$savedUser=$env:PGUSER
$savedPassword=$env:PGPASSWORD
$savedSSL=$env:PGSSLMODE
try {
    $env:PGSSLMODE='disable'
    foreach ($connection in @(
        @{Database=$controlDatabase; User='ci_app_control'; Password=$env:CONTROL_DB_PASSWORD},
        @{Database=$controlDatabase; User='ci_app_auth'; Password=$env:AUTH_DB_PASSWORD},
        @{Database=$shardDatabase; User='ci_app_shard'; Password=$env:SHARD_DB_PASSWORD},
        @{Database=$controlDatabase; User='ci_app_executor'; Password=$env:EXECUTOR_DB_PASSWORD},
        @{Database=$shardDatabase; User='ci_app_executor'; Password=$env:EXECUTOR_DB_PASSWORD}
    )) {
        $env:PGUSER=$connection.User
        $env:PGPASSWORD=$connection.Password
        & $Psql -X -w -q --set=ON_ERROR_STOP=1 --set=VERBOSITY=terse --dbname $connection.Database --command 'SELECT 1;' | Out-Null
        if ($LASTEXITCODE -ne 0) { throw 'Runtime connection verification failed. Existing technical passwords are preserved; check your environment settings.' }
    }
} finally {
    $env:PGUSER=$savedUser
    $env:PGPASSWORD=$savedPassword
    $env:PGSSLMODE=$savedSSL
}
Write-Host 'Compose database initialization complete.'
