param([Parameter(ValueFromRemainingArguments=$true)][string[]]$Arguments)
Set-StrictMode -Version Latest
$ErrorActionPreference='Stop'
$container=[Environment]::GetEnvironmentVariable('CI_AUTH_TEST_CONTAINER')
if ([string]::IsNullOrEmpty($container) -or $container -notmatch '^ci-auth-pg-[0-9a-f]{32}$') { throw 'Invalid temporary PostgreSQL container.' }
& docker exec -e PGHOST=127.0.0.1 -e PGPORT=5432 -e "PGUSER=$env:PGUSER" -e "PGPASSWORD=$env:PGPASSWORD" $container psql @Arguments
exit $LASTEXITCODE
