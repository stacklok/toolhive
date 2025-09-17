@echo off
setlocal enabledelayedexpansion

REM E2E Test Runner for ToolHive
REM This script sets up the environment and runs the e2e tests

REM Set error handling
set "EXIT_CODE=0"

echo ToolHive E2E Test Runner
echo ================================

REM Set TOOLHIVE_DEV environment variable to true
set "TOOLHIVE_DEV=true"

REM Check if thv binary exists
if "%THV_BINARY%"=="" (
    set "THV_BINARY=thv.exe"
    where "%THV_BINARY%" >nul 2>&1
) else (
    dir "%THV_BINARY%" >nul 2>&1
)
if %errorlevel% neq 0 (
    echo Error: thv binary not found in PATH
    echo Please build the binary first with: task build
    echo Or set THV_BINARY environment variable to the binary path
    exit /b 1
)

echo ✓ Found thv binary: %THV_BINARY%

REM Check if container runtime is available
set "CONTAINER_RUNTIME="
where docker >nul 2>&1
if %errorlevel% equ 0 (
    set "CONTAINER_RUNTIME=docker"
    echo ✓ Found container runtime: docker
) else (
    where podman >nul 2>&1
    if %errorlevel% equ 0 (
        set "CONTAINER_RUNTIME=podman"
        echo ✓ Found container runtime: podman
    ) else (
        echo Error: Neither docker nor podman found
        echo Please install docker or podman to run MCP servers
        exit /b 1
    )
)

REM Set test timeout
if "%TEST_TIMEOUT%"=="" set "TEST_TIMEOUT=20m"
echo ✓ Test timeout: %TEST_TIMEOUT%

REM Export environment variables for tests
set "THV_BINARY=%THV_BINARY%"
set "TEST_TIMEOUT=%TEST_TIMEOUT%"

echo.
echo Running E2E Tests...
echo.

REM Run the tests
cd /d "%~dp0"

REM Build ginkgo command with conditional GitHub output flag
set "GINKGO_CMD=ginkgo run --timeout=%TEST_TIMEOUT%"
if defined GITHUB_ACTIONS (
    echo ✓ GitHub Actions detected, enabling GitHub output format
    set "GINKGO_CMD=%GINKGO_CMD% --github-output"
) else (
    set "GINKGO_CMD=%GINKGO_CMD% --vv --show-node-events --trace"
)

REM Optional label filter (LABEL_FILTER or E2E_LABEL_FILTER)
set "LABEL_FILTER_EFFECTIVE="
if defined LABEL_FILTER (
    set "LABEL_FILTER_EFFECTIVE=%LABEL_FILTER%"
) else (
    if defined E2E_LABEL_FILTER (
        set "LABEL_FILTER_EFFECTIVE=%E2E_LABEL_FILTER%"
    )
)

if defined LABEL_FILTER_EFFECTIVE (
    echo ✓ Using label filter: %LABEL_FILTER_EFFECTIVE%
    set GINKGO_CMD=%GINKGO_CMD% --label-filter="%LABEL_FILTER_EFFECTIVE%"
)

set "GINKGO_CMD=%GINKGO_CMD% ."

REM Execute the ginkgo command
%GINKGO_CMD%
if %errorlevel% equ 0 (
    echo.
    echo ✓ All E2E tests passed!
    exit /b 0
) else (
    echo.
    echo ✗ Some E2E tests failed
    exit /b 1
)
