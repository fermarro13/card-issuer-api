param([Parameter(ValueFromRemainingArguments=$true)][string[]]$Arguments)
Set-StrictMode -Version Latest
$ErrorActionPreference='Stop'
$container=[Environment]::GetEnvironmentVariable('CI_AUTH_TEST_CONTAINER')
if ([string]::IsNullOrEmpty($container) -or $container -notmatch '^ci-auth-pg-[0-9a-f]{32}$') { throw 'Invalid temporary PostgreSQL container.' }
$psqlArguments=[System.Collections.Generic.List[string]]::new()
$sqlFiles=[System.Collections.Generic.List[string]]::new()
for ($i=0; $i -lt $Arguments.Count; $i++) {
    if (($Arguments[$i] -eq '-f' -or $Arguments[$i] -eq '--file') -and $i+1 -lt $Arguments.Count) {
        $i++
        $sqlFiles.Add($Arguments[$i])
        continue
    }
    $psqlArguments.Add($Arguments[$i])
}
if ($sqlFiles.Count -eq 0) {
    & docker exec -i -e PGHOST=127.0.0.1 -e PGPORT=5432 -e PGUSER -e PGPASSWORD -e CONTROL_DB_PASSWORD -e AUTH_DB_PASSWORD -e SHARD_DB_PASSWORD $container psql @psqlArguments
} else {
    foreach ($sqlFile in $sqlFiles) {
        [System.IO.File]::ReadAllText($sqlFile) | & docker exec -i -e PGHOST=127.0.0.1 -e PGPORT=5432 -e PGUSER -e PGPASSWORD -e CONTROL_DB_PASSWORD -e AUTH_DB_PASSWORD -e SHARD_DB_PASSWORD $container psql @psqlArguments
        if ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }
    }
}
exit $LASTEXITCODE
