@echo off
setlocal enabledelayedexpansion

set "SCRIPT_DIR=%~dp0"
set "PS_SCRIPT=%SCRIPT_DIR%install.ps1"

if not exist "%PS_SCRIPT%" (
    echo [ERROR] install.ps1 not found in %SCRIPT_DIR%
    exit /b 1
)

echo [INFO] Running Ektara Windows installer...
powershell -NoProfile -ExecutionPolicy Bypass -File "%PS_SCRIPT%" %*

if %ERRORLEVEL% neq 0 (
    echo.
    echo [ERROR] Installation failed with exit code %ERRORLEVEL%.
    exit /b %ERRORLEVEL%
)

exit /b 0
