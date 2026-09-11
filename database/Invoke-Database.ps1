#requires -Version 7.0
[CmdletBinding()]
param(
    [ValidateSet('Bootstrap','Migrate','SetupTest')][string]$Action = 'Migrate',
    [string]$Psql = 'psql',
    [ValidatePattern('^[a-zA-Z_][a-zA-Z0-9_]{0,62}$')][string]$ControlDatabase = 'card_issuer_control',
    [ValidatePattern('^[a-zA-Z_][a-zA-Z0-9_]{0,62}$')][string]$ShardDatabase = 'card_issuer_shard_01',
    [ValidatePattern('^[a-zA-Z_][a-zA-Z0-9_]{0,62}$')][string]$AdminDatabase = 'postgres',
    [ValidatePattern('^[a-zA-Z0-9_-]{1,63}$')][string]$ShardId = 'shard_01',
    [string]$MigrationRoot = (Join-Path $PSScriptRoot 'migrations')
)
Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'
if ($ControlDatabase -ieq $ShardDatabase) { throw 'Control and shard database names must differ.' }
if ($AdminDatabase -ieq $ControlDatabase -or $AdminDatabase -ieq $ShardDatabase) { throw 'Application databases must differ from the administrative database.' }
$null = Get-Command $Psql -ErrorAction Stop

function Invoke-Sql([string]$Database, [string]$Sql) {
    $temporary = [IO.Path]::GetTempFileName()
    try {
        [IO.File]::WriteAllText($temporary, $Sql, [Text.UTF8Encoding]::new($false))
        & $Psql -X --no-password --set=ON_ERROR_STOP=1 --set=VERBOSITY=terse --dbname=$Database --file=$temporary
        if ($LASTEXITCODE -ne 0) { throw "psql failed for $Database (exit $LASTEXITCODE)." }
    } finally {
        Remove-Item -LiteralPath $temporary -ErrorAction SilentlyContinue
    }
}

function Invoke-Migrations([string]$Database, [string]$Kind) {
    $files = @(Get-ChildItem -LiteralPath (Join-Path $MigrationRoot $Kind) -Filter '*.sql' -File | Sort-Object Name)
    if ($files.Count -eq 0) { throw "No $Kind migrations found." }
    $versions = @{}
    $snapshots = foreach ($file in $files) {
        if ($file.Name -notmatch '^([0-9]{3,})_[a-z0-9_]+\.sql$') { throw "Invalid migration filename: $($file.Name)" }
        $version = [long]$Matches[1]
        if ($versions.ContainsKey($version)) { throw "Duplicate migration version $version" }
        $versions[$version] = $true
        # Hash the exact normalized text executed, so checkout line endings do not change checksums.
        $body = [IO.File]::ReadAllText($file.FullName).Replace("`r`n", "`n")
        $hash = [Convert]::ToHexString([Security.Cryptography.SHA256]::HashData([Text.Encoding]::UTF8.GetBytes($body))).ToLowerInvariant()
        [pscustomobject]@{ Version=$version; Name=$file.Name; Body=$body; Hash=$hash }
    }
    $snapshots = @($snapshots | Sort-Object Version)
    $knownVersions = ($snapshots.Version -join ',')
    $sql = [Text.StringBuilder]::new()
    [void]$sql.AppendLine(@"
SET ROLE ci_owner;
SELECT pg_advisory_lock(170017,2);
DO `$`$ BEGIN
  IF current_setting('server_version_num')::int < 170000 THEN RAISE EXCEPTION 'PostgreSQL 17 or newer is required'; END IF;
END `$`$;
BEGIN;
CREATE SCHEMA IF NOT EXISTS ci_meta AUTHORIZATION ci_owner;
REVOKE ALL ON SCHEMA ci_meta FROM PUBLIC;
CREATE TABLE IF NOT EXISTS ci_meta.database_identity (singleton boolean PRIMARY KEY DEFAULT true CHECK(singleton), kind text NOT NULL);
INSERT INTO ci_meta.database_identity(singleton,kind) VALUES (true,'$Kind') ON CONFLICT DO NOTHING;
DO `$`$ BEGIN
  IF (SELECT kind FROM ci_meta.database_identity) <> '$Kind' THEN RAISE EXCEPTION 'Database migration kind mismatch'; END IF;
END `$`$;
CREATE TABLE IF NOT EXISTS ci_meta.schema_migrations (version bigint PRIMARY KEY, filename text NOT NULL, checksum text NOT NULL, applied_at timestamptz NOT NULL DEFAULT now());
COMMIT;
DO `$`$ BEGIN
  IF EXISTS (SELECT FROM ci_meta.schema_migrations WHERE version NOT IN ($knownVersions)) THEN
    RAISE EXCEPTION 'Applied migration is missing from this checkout';
  END IF;
END `$`$;
"@)
    # Validate the complete history before applying anything new.
    foreach ($m in $snapshots) {
        [void]$sql.AppendLine(@"
DO `$`$ BEGIN
  IF EXISTS (SELECT FROM ci_meta.schema_migrations WHERE version=$($m.Version) AND (checksum<>'$($m.Hash)' OR filename<>'$($m.Name)')) THEN
    RAISE EXCEPTION 'Applied migration changed: $($m.Name)';
  END IF;
  IF NOT EXISTS (SELECT FROM ci_meta.schema_migrations WHERE version=$($m.Version)) AND EXISTS (SELECT FROM ci_meta.schema_migrations WHERE version>$($m.Version)) THEN
    RAISE EXCEPTION 'Out-of-order migration: $($m.Name)';
  END IF;
END `$`$;
"@)
    }
    foreach ($m in $snapshots) {
        [void]$sql.AppendLine(@"
SELECT EXISTS(SELECT FROM ci_meta.schema_migrations WHERE version=$($m.Version)) AS applied \gset
\if :applied
  \echo 'Already applied: $Kind/$($m.Name)'
\else
  BEGIN;
$($m.Body)
  INSERT INTO ci_meta.schema_migrations(version,filename,checksum) VALUES ($($m.Version),'$($m.Name)','$($m.Hash)');
  COMMIT;
\endif
"@)
    }
    [void]$sql.AppendLine('SELECT pg_advisory_unlock(170017,2);')
    Invoke-Sql $Database $sql.ToString()
}

if ($Action -in 'Bootstrap','SetupTest') {
    $bootstrap = [IO.File]::ReadAllText((Join-Path $PSScriptRoot 'bootstrap.sql'))
    Invoke-Sql $AdminDatabase ("\set control_db '$ControlDatabase'`n\set shard_db '$ShardDatabase'`n" + $bootstrap)
}
if ($Action -in 'Migrate','SetupTest') {
    Invoke-Migrations $ControlDatabase 'control'
    Invoke-Migrations $ShardDatabase 'shard'
}
if ($Action -eq 'SetupTest') {
    Invoke-Sql $ShardDatabase ([IO.File]::ReadAllText((Join-Path $PSScriptRoot 'seeds/shard.sql')))
    $seed = [IO.File]::ReadAllText((Join-Path $PSScriptRoot 'seeds/control.sql'))
    Invoke-Sql $ControlDatabase ("\set shard_id '$ShardId'`n" + $seed)
    Write-Host 'Test setup complete. Application passwords: database/TEST-CREDENTIALS.md'
}
