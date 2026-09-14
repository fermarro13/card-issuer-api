@echo off
setlocal EnableExtensions DisableDelayedExpansion
if "%CI_AUTH_TEST_CONTAINER%"=="" exit /b 1
set "psql_args="
set "sql_file="
:next
if "%~1"=="" goto run
if /i "%~1"=="-f" goto file
if /i "%~1"=="--file" goto file
if /i "%~1"=="-v" goto setvalue
if /i "%~1"=="--set" goto setvalue
set "psql_args=%psql_args% "%~1""
shift
goto next
:file
if "%~2"=="" exit /b 1
set "sql_file=%~2"
shift
shift
goto next
:setvalue
set "set_name=%~2"
echo(%set_name%| findstr /l "=" >nul
if not errorlevel 1 goto normal
if "%~3"=="" exit /b 1
set "psql_args=%psql_args% "%~1" "%~2=%~3""
shift
shift
shift
goto next
:normal
set "psql_args=%psql_args% "%~1""
shift
goto next
:run
if not "%sql_file%"=="" (
  type "%sql_file%" | docker exec -i -e PGHOST=127.0.0.1 -e PGPORT=5432 -e PGUSER -e PGPASSWORD -e CONTROL_DB_PASSWORD -e AUTH_DB_PASSWORD -e SHARD_DB_PASSWORD %CI_AUTH_TEST_CONTAINER% psql %psql_args%
) else (
  docker exec -i -e PGHOST=127.0.0.1 -e PGPORT=5432 -e PGUSER -e PGPASSWORD -e CONTROL_DB_PASSWORD -e AUTH_DB_PASSWORD -e SHARD_DB_PASSWORD %CI_AUTH_TEST_CONTAINER% psql %psql_args%
)
exit /b %ERRORLEVEL%
