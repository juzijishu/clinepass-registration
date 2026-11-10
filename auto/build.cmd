@echo off
setlocal EnableExtensions DisableDelayedExpansion
call "%~dp0start.cmd" --build-only
exit /b %errorlevel%
