#requires -Version 7.0
[CmdletBinding()]
param([ValidateRange(1024,65535)][int]$AppPort=18080)
Set-StrictMode -Version Latest
$ErrorActionPreference='Stop'
$root=Split-Path $PSScriptRoot -Parent
$project='ci-smoke-'+[guid]::NewGuid().ToString('N')
$compose=@('compose','--project-name',$project,'--project-directory',$root,'--env-file',(Join-Path $root '.env.example'),'-f',(Join-Path $root 'compose.yaml'))
$savedEnvironment=@{}
$settings=@{
    APP_PORT="$AppPort"; CONTROL_DATABASE='card_issuer_control'; SHARD_DATABASE='card_issuer_shard_01'; SHARD_ID='shard_01'
    POSTGRES_PASSWORD='Dev-Postgres-Admin!2026'; CONTROL_DB_PASSWORD='Dev-Control-Reader!2026'; AUTH_DB_PASSWORD='Dev-Auth-Runtime!2026'; SHARD_DB_PASSWORD='Dev-Shard-Runtime!2026'
    AUTH_JWT_PRIVATE_KEY_B64='nWGxne/9WmC6hEr0kuwsxERJxWl7MmkZcDusAxyuf2DXWpgBgrEKt9VL/tPJZAc6DuFy89qmIyWvAhpo9wdRGg'; AUTH_JWT_ISSUER='card-issuer-api'; AUTH_JWT_AUDIENCE='card-issuer-api'
}

function Invoke-Compose([string[]]$Arguments) {
    $output=& docker @compose @Arguments 2>&1
    if ($LASTEXITCODE -ne 0) { throw "Compose command failed: $($Arguments -join ' ')`n$($output -join "`n")" }
    return ($output -join "`n")
}
function Query([string]$Database,[string]$Statement) {
    $result=$Statement | & docker @compose exec -T postgres psql -X -w -qAt -v ON_ERROR_STOP=1 -U postgres -d $Database 2>&1
    if ($LASTEXITCODE -ne 0) {throw 'Database verification failed.'}
    return ($result -join "`n").Trim()
}
function Assert([bool]$Condition,[string]$Message) { if (!$Condition) { throw $Message } }
function Wait-HTTP([string]$Path,[int]$Status) {
    $deadline=[DateTime]::UtcNow.AddSeconds(60)
    while ([DateTime]::UtcNow -lt $deadline) {
        try {
            $response=Invoke-WebRequest "http://127.0.0.1:$AppPort$Path" -SkipHttpErrorCheck -TimeoutSec 4
            if ([int]$response.StatusCode -eq $Status) { return }
        } catch { }
        Start-Sleep -Milliseconds 500
    }
    throw "$Path did not return HTTP $Status within 60 seconds."
}

try {
    foreach ($name in $settings.Keys) {$savedEnvironment[$name]=[Environment]::GetEnvironmentVariable($name);[Environment]::SetEnvironmentVariable($name,$settings[$name])}
    Invoke-Compose -Arguments @('config','--quiet') | Out-Null
    Write-Host 'Building and starting a fresh isolated Compose project...'
    Invoke-Compose -Arguments @('up','--build','-d') | Out-Null
    Wait-HTTP '/health/live' 200
    Wait-HTTP '/health/ready' 200
    Assert ((Query 'card_issuer_control' 'SELECT count(*) FROM control.users;') -eq '4') 'Expected four staff accounts.'
    Assert ((Query 'card_issuer_control' 'SELECT count(*) FROM ci_meta.schema_migrations;') -eq '1') 'Control migration missing.'
    Assert ((Query 'card_issuer_shard_01' 'SELECT count(*) FROM ci_meta.schema_migrations;') -eq '1') 'Shard migration missing.'
    $appID=(Invoke-Compose -Arguments @('ps','-q','app')).Trim()
    $user=& docker inspect --format '{{.Config.User}}' $appID
    Assert ($LASTEXITCODE -eq 0 -and $user -eq '10001:10001') 'Application must run as non-root.'
    Assert ((Query 'postgres' "SELECT count(*) FROM pg_roles WHERE rolname IN ('ci_app_control','ci_app_auth','ci_app_shard') AND NOT (rolsuper OR rolcreatedb OR rolcreaterole OR rolbypassrls OR rolreplication);") -eq '3') 'Runtime privilege attributes are incorrect.'

    Write-Host 'Checking retained data and passwords through down/up...'
    Query 'card_issuer_control' "UPDATE control.users SET password_hash=password_hash||'changed' WHERE normalized_username='bank_operator';" | Out-Null
    $before=Query 'card_issuer_control' "SELECT md5(password_hash) FROM control.users WHERE normalized_username='bank_operator';"
    Invoke-Compose -Arguments @('down') | Out-Null
    Invoke-Compose -Arguments @('up','-d') | Out-Null
    Wait-HTTP '/health/ready' 200
    Assert ((Query 'card_issuer_control' "SELECT md5(password_hash) FROM control.users WHERE normalized_username='bank_operator';") -eq $before) 'Restart reset staff password.'
    Assert ((Query 'card_issuer_control' 'SELECT count(*) FROM control.authentication_audit_events;') -eq '4') 'Restart duplicated test accounts/audits.'

    Write-Host 'Checking liveness during a PostgreSQL outage and readiness recovery...'
    Invoke-Compose -Arguments @('stop','postgres') | Out-Null
    Wait-HTTP '/health/live' 200
    Wait-HTTP '/health/ready' 503
    Invoke-Compose -Arguments @('start','postgres') | Out-Null
    Wait-HTTP '/health/ready' 200

    Write-Host 'Checking that failed initialization blocks a newly starting app...'
    $checksum=Query 'card_issuer_control' 'SELECT checksum FROM ci_meta.schema_migrations WHERE version=1;'
    Query 'card_issuer_control' "UPDATE ci_meta.schema_migrations SET checksum='intentional-test-corruption' WHERE version=1;" | Out-Null
    Invoke-Compose -Arguments @('rm','--stop','--force','app','db-init') | Out-Null
    $null=& docker @compose up -d app 2>&1
    Assert ($LASTEXITCODE -ne 0) 'Initialization failure was ignored.'
    Assert ([string]::IsNullOrWhiteSpace((Invoke-Compose -Arguments @('ps','--status','running','-q','app')))) 'App ran despite failed initialization.'
    Assert ($checksum -match '^[0-9a-f]{64}$') 'Unexpected migration checksum.'
    Query 'card_issuer_control' "UPDATE ci_meta.schema_migrations SET checksum='$checksum' WHERE version=1;" | Out-Null
    Invoke-Compose -Arguments @('rm','--force','db-init') | Out-Null
    Invoke-Compose -Arguments @('up','-d') | Out-Null
    Wait-HTTP '/health/ready' 200
    Write-Host 'PASS: clean startup, persistence, initialization gates, non-root execution and outage recovery.'
} finally {
    # Only this script's exact randomly named project may be removed, including its disposable volume.
    if ($project -notmatch '^ci-smoke-[0-9a-f]{32}$') { throw 'Unsafe cleanup project.' }
    & docker @compose down --volumes --remove-orphans | Out-Null
    foreach ($name in $savedEnvironment.Keys) {[Environment]::SetEnvironmentVariable($name,$savedEnvironment[$name])}
}
