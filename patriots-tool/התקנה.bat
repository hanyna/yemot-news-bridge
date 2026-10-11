@echo off
chcp 65001 >nul
rem התקנת "הפטריוטים המלא" — מריץ את setup.ps1 עם הרשאות מנהל.
net session >nul 2>&1
if %errorlevel% neq 0 (
    powershell -NoProfile -Command "Start-Process -Verb RunAs -FilePath '%~f0'"
    exit /b
)
powershell -NoProfile -ExecutionPolicy Bypass -File "%~dp0setup.ps1"
pause
