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
    POSTGRES_PASSWORD='Dev-Postgres-Admin!2026'; CONTROL_DB_PASSWORD='Dev-Control-Reader!2026'; AUTH_DB_PASSWORD='Dev-Auth-Runtime!2026'; SHARD_DB_PASSWORD='Dev-Shard-Runtime!2026'; EXECUTOR_DB_PASSWORD='Dev-Executor-Runtime!2026'; EXECUTOR_ID='executor-smoke-01'
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
function Wait-HTTP([string]$BaseURL,[string]$Path,[int]$Status) {
    $deadline=[DateTime]::UtcNow.AddSeconds(60)
    while ([DateTime]::UtcNow -lt $deadline) {
        $check="import sys, httpx; response=httpx.get('$BaseURL$Path', timeout=4); sys.exit(0 if response.status_code == $Status else 1)"
        $null=& docker @compose --profile functional run --rm --no-deps --entrypoint python functional-tests -c $check 2>$null
        if ($LASTEXITCODE -eq 0) { return }
        Start-Sleep -Milliseconds 500
    }
    throw "$BaseURL$Path did not return HTTP $Status within 60 seconds."
}
function Wait-ContainerHealth([string]$Container,[string]$Name) {
    $deadline=[DateTime]::UtcNow.AddSeconds(60)
    while ([DateTime]::UtcNow -lt $deadline) {
        $status=& docker inspect --format '{{if .State.Health}}{{.State.Health.Status}}{{else}}none{{end}}' $Container 2>$null
        if ($LASTEXITCODE -eq 0 -and $status.Trim() -eq 'healthy') { return }
        Start-Sleep -Milliseconds 500
    }
    throw "$Name did not become healthy within 60 seconds."
}
function Assert-ExecutorIdentity([string]$Container,[string]$Identity) {
    $format='{{range .Config.Env}}{{if eq . "EXECUTOR_ID='+$Identity+'"}}{{.}}{{end}}{{end}}'
    $actual=& docker inspect --format $format $Container
    Assert ($LASTEXITCODE -eq 0 -and $actual.Trim() -eq "EXECUTOR_ID=$Identity") "Executor identity was not explicitly set to $Identity."
}
$replicaAName=$project+'-executor-replica-a'
$replicaBName=$project+'-executor-replica-b'

try {
    foreach ($name in $settings.Keys) {
        $savedEnvironment[$name]=[Environment]::GetEnvironmentVariable($name,'Process')
        Set-Item -Path "Env:$name" -Value $settings[$name]
    }
    Invoke-Compose -Arguments @('config','--quiet') | Out-Null
    Write-Host 'Building and starting a fresh isolated Compose project...'
    Invoke-Compose -Arguments @('up','--build','-d') | Out-Null
    Wait-HTTP 'http://app:8080' '/health/live' 200
    Wait-HTTP 'http://app:8080' '/health/ready' 200
    Assert ((Query 'card_issuer_control' 'SELECT count(*) FROM control.users;') -eq '4') 'Expected four staff accounts.'
    Assert ((Query 'card_issuer_control' 'SELECT count(*) FROM ci_meta.schema_migrations;') -eq '3') 'Control migrations missing.'
    Assert ((Query 'card_issuer_shard_01' 'SELECT count(*) FROM ci_meta.schema_migrations;') -eq '5') 'Shard migrations missing.'
    $appID=(Invoke-Compose -Arguments @('ps','-q','app')).Trim()
    $user=& docker inspect --format '{{.Config.User}}' $appID
    Assert ($LASTEXITCODE -eq 0 -and $user -eq '10001:10001') 'Application must run as non-root.'
    $executorID=(Invoke-Compose -Arguments @('ps','-q','executor')).Trim()
    Assert ((& docker inspect --format '{{.Config.User}}' $executorID) -eq '10001:10001') 'Executor must run as non-root.'
    $authorizationID=(Invoke-Compose -Arguments @('ps','-q','authorization')).Trim()
    Assert ((& docker inspect --format '{{.Config.User}}' $authorizationID) -eq '10001:10001') 'Authorization service must run as non-root.'
    Wait-ContainerHealth $authorizationID 'authorization'
    Assert ((Query 'postgres' "SELECT count(*) FROM pg_roles WHERE rolname IN ('ci_app_control','ci_app_auth','ci_app_shard','ci_app_executor') AND NOT (rolsuper OR rolcreatedb OR rolcreaterole OR rolbypassrls OR rolreplication);") -eq '4') 'Runtime privilege attributes are incorrect.'

    Write-Host 'Checking API and executor independent lifecycle...'
    Invoke-Compose -Arguments @('stop','executor') | Out-Null
    Wait-HTTP 'http://app:8080' '/health/live' 200
    Wait-HTTP 'http://app:8080' '/health/ready' 200
    Invoke-Compose -Arguments @('start','executor') | Out-Null
    Wait-ContainerHealth $executorID 'executor'
    Invoke-Compose -Arguments @('stop','app') | Out-Null
    Wait-ContainerHealth $executorID 'executor while API is stopped'
    Wait-HTTP 'http://executor:8090' '/health/ready' 200
    Invoke-Compose -Arguments @('start','app') | Out-Null
    Wait-HTTP 'http://app:8080' '/health/ready' 200

    Write-Host 'Checking competing executor replicas...'
    Invoke-Compose -Arguments @('stop','executor') | Out-Null
    Invoke-Compose -Arguments @('run','-d','--no-deps','--name',$replicaAName,'-e','EXECUTOR_ID=executor-smoke-02','executor') | Out-Null
    Invoke-Compose -Arguments @('run','-d','--no-deps','--name',$replicaBName,'-e','EXECUTOR_ID=executor-smoke-03','executor') | Out-Null
    Wait-ContainerHealth $replicaAName 'executor replica A'
    Wait-ContainerHealth $replicaBName 'executor replica B'
    Assert-ExecutorIdentity $replicaAName 'executor-smoke-02'
    Assert-ExecutorIdentity $replicaBName 'executor-smoke-03'

    Write-Host 'Running the Compose functional profile, including concurrency coverage...'
    Invoke-Compose -Arguments @('--profile','functional','run','--rm','--no-deps','-e','FUNCTIONAL_EXECUTOR_IDENTITIES=executor-smoke-02,executor-smoke-03','functional-tests') | Out-Null

    Write-Host 'Checking retained data and passwords through down/up...'
    Query 'card_issuer_control' "UPDATE control.users SET password_hash=password_hash||'changed' WHERE normalized_username='bank_operator';" | Out-Null
    $before=Query 'card_issuer_control' "SELECT md5(password_hash) FROM control.users WHERE normalized_username='bank_operator';"
    $auditBefore=Query 'card_issuer_control' 'SELECT count(*) FROM control.authentication_audit_events;'
    Invoke-Compose -Arguments @('down') | Out-Null
    Invoke-Compose -Arguments @('up','-d') | Out-Null
    Wait-HTTP 'http://app:8080' '/health/ready' 200
    Assert ((Query 'card_issuer_control' "SELECT md5(password_hash) FROM control.users WHERE normalized_username='bank_operator';") -eq $before) 'Restart reset staff password.'
    Assert ((Query 'card_issuer_control' 'SELECT count(*) FROM control.authentication_audit_events;') -eq $auditBefore) 'Restart duplicated authentication audits.'

    Write-Host 'Checking liveness during a PostgreSQL outage and readiness recovery...'
    Invoke-Compose -Arguments @('stop','postgres') | Out-Null
    Wait-HTTP 'http://app:8080' '/health/live' 200
    Wait-HTTP 'http://app:8080' '/health/ready' 503
    Invoke-Compose -Arguments @('start','postgres') | Out-Null
    Wait-HTTP 'http://app:8080' '/health/ready' 200

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
    Wait-HTTP 'http://app:8080' '/health/ready' 200
    Write-Host 'PASS: startup, functional authorization profile, independent API/executor lifecycle, replica identities, persistence, initialization gates, non-root execution and outage recovery.'
} finally {
    # Only this script's exact randomly named project may be removed, including its disposable volume.
    if ($project -notmatch '^ci-smoke-[0-9a-f]{32}$') { throw 'Unsafe cleanup project.' }
    foreach ($container in @($replicaAName,$replicaBName)) { & docker rm --force $container 2>$null | Out-Null }
    & docker @compose down --volumes --remove-orphans | Out-Null
    foreach ($name in $savedEnvironment.Keys) {
        if ($null -eq $savedEnvironment[$name]) { Remove-Item -Path "Env:$name" -ErrorAction SilentlyContinue } else { Set-Item -Path "Env:$name" -Value $savedEnvironment[$name] }
    }
}
