# התקנת "הפטריוטים המלא" — כלי שמוריד את התוכנית המלאה המוכנה מיוטיוב ומעלה
# אותה לקו במקום ההקלטה מהשידור. רושם משימה מתוזמנת שמעירה את המחשב מהשינה.
$ErrorActionPreference = "Stop"
chcp 65001 > $null
$OutputEncoding = [System.Text.Encoding]::UTF8

$src = $PSScriptRoot
$dir = Join-Path $env:LOCALAPPDATA "YemotPatriots"
$exe = Join-Path $dir "yemot-patriots.exe"
$env1 = Join-Path $dir "patriots.env"

Write-Host ""
Write-Host "=== התקנת 'הפטריוטים המלא' ===" -ForegroundColor Cyan
New-Item -ItemType Directory -Force -Path $dir | Out-Null

# 1) העתקת התוכנה
$srcExe = Join-Path $src "yemot-patriots.exe"
if (-not (Test-Path $srcExe)) { throw "לא נמצא yemot-patriots.exe ליד קובץ ההתקנה." }
Copy-Item $srcExe $exe -Force
Write-Host "✓ התוכנה הותקנה בתיקייה: $dir"

# 2) מפתח הקו של ימות המשיח
$existingKey = ""
if (Test-Path $env1) {
    $m = (Get-Content $env1 -Encoding UTF8 | Select-String '^YEMOT_API_KEY=(.+)$')
    if ($m) { $existingKey = $m.Matches[0].Groups[1].Value.Trim() }
}
if ($existingKey) {
    Write-Host "✓ כבר קיים מפתח קו שמור. להשאיר אותו? (Enter = כן, או הדביקו מפתח חדש)"
    $k = Read-Host "מפתח"
    if ([string]::IsNullOrWhiteSpace($k)) { $k = $existingKey }
} else {
    Write-Host "הדביקו את המפתח הקבוע של הקו (מעמוד 'מפתחות גישה' בימות המשיח):"
    $k = Read-Host "מפתח"
    if ([string]::IsNullOrWhiteSpace($k)) { throw "לא הוזן מפתח." }
}

# 3) וידוא yt-dlp ו-ffmpeg (מורידים לתיקייה אם חסרים; המחשב בישראל לא חסום)
function Ensure-Tool($name, $url, $isZip, $pickNames) {
    $local = Join-Path $dir $name
    if (Test-Path $local) { return $dir }
    $onPath = Get-Command $name -ErrorAction SilentlyContinue
    if ($onPath) { return (Split-Path $onPath.Source) }
    Write-Host "מוריד $name ..." -ForegroundColor Yellow
    if ($isZip) {
        $zip = Join-Path $env:TEMP "patriots-dl.zip"
        Invoke-WebRequest -Uri $url -OutFile $zip -UseBasicParsing
        $tmp = Join-Path $env:TEMP "patriots-unzip"
        if (Test-Path $tmp) { Remove-Item $tmp -Recurse -Force }
        Expand-Archive -Path $zip -DestinationPath $tmp -Force
        foreach ($p in $pickNames) {
            $f = Get-ChildItem -Path $tmp -Recurse -Filter $p | Select-Object -First 1
            if ($f) { Copy-Item $f.FullName (Join-Path $dir $p) -Force }
        }
        Remove-Item $zip, $tmp -Recurse -Force -ErrorAction SilentlyContinue
    } else {
        Invoke-WebRequest -Uri $url -OutFile $local -UseBasicParsing
    }
    return $dir
}

# הכלי משתמש ב-yt-dlp וב-ffmpeg שמוריד השירים כבר התקין (אותם כלים, כבר
# עובדים דרך נטפרי). מאתרים אותם; רק אם לא קיימים — מורידים כגיבוי.
$moridDir = Join-Path $env:LOCALAPPDATA "MoridHashirim"
$ytFound = $null; $ffFound = $null
if (Test-Path $moridDir) {
    $ytFound = Get-ChildItem -Path $moridDir -Recurse -Filter "yt-dlp.exe" -ErrorAction SilentlyContinue | Select-Object -First 1
    $ffFound = Get-ChildItem -Path $moridDir -Recurse -Filter "ffmpeg.exe" -ErrorAction SilentlyContinue | Select-Object -First 1
}
$ytLine = "# YTDLP=        # ריק = הכלי מאתר לבד את זה של מוריד השירים"
$ffLine = "# FFMPEG_DIR=   # ריק = הכלי מאתר לבד את זה של מוריד השירים"
if ($ytFound -and $ffFound) {
    Write-Host "✓ נמצאו הכלים של מוריד השירים — נשתמש בהם (yt-dlp + ffmpeg)." -ForegroundColor Green
} else {
    Write-Host "לא נמצאו הכלים של מוריד השירים — מוריד yt-dlp ו-ffmpeg כגיבוי..." -ForegroundColor Yellow
    $ytDir = Ensure-Tool "yt-dlp.exe" "https://github.com/yt-dlp/yt-dlp/releases/latest/download/yt-dlp.exe" $false @()
    $ffDir = $dir
    if (-not (Test-Path (Join-Path $dir "ffmpeg.exe"))) {
        $ffDir = Ensure-Tool "ffmpeg.exe" "https://github.com/BtbN/FFmpeg-Builds/releases/download/latest/ffmpeg-master-latest-win64-gpl.zip" $true @("ffmpeg.exe","ffprobe.exe")
    }
    $ytLine = "YTDLP=" + (Join-Path $ytDir "yt-dlp.exe")
    $ffLine = "FFMPEG_DIR=$ffDir"
}

# 4) קובץ ההגדרות
@"
# הגדרות הכלי. אפשר לערוך בעריכה פשוטה (Notepad).
YEMOT_API_KEY=$k
PATRIOTS_PLAYLIST=PL8E54R76rowCcCOn_HQf36o1RF3_oD3z4
PATRIOTS_EXT=3/3
PATRIOTS_START=21:00
PATRIOTS_DAYS_BACK=2
$ytLine
$ffLine
# נטפרי/רימון: on = לדלג על אימות תעודה (ברירת מחדל). off = לכבות.
PATRIOTS_INSECURE_TLS=on
# אם יוטיוב מבקש אימות ('Sign in to confirm') — כתבו כאן את הדפדפן: chrome / edge / firefox
YT_COOKIES_BROWSER=
"@ | Set-Content -Path $env1 -Encoding UTF8
Write-Host "✓ הגדרות נשמרו: $env1"

# 5) משימה מתוזמנת שמעירה את המחשב ומריצה את הכלי בלילה (ובבוקר כגיבוי)
$action = New-ScheduledTaskAction -Execute $exe -Argument "patriots" -WorkingDirectory $dir
$t1 = New-ScheduledTaskTrigger -Daily -At 11:40PM
$t2 = New-ScheduledTaskTrigger -Daily -At 6:30AM
$set = New-ScheduledTaskSettingsSet -WakeToRun -StartWhenAvailable -AllowStartIfOnBatteries `
        -DontStopIfGoingOnBatteries -DontStopOnIdleEnd -ExecutionTimeLimit (New-TimeSpan -Hours 1) `
        -MultipleInstances IgnoreNew
$principal = New-ScheduledTaskPrincipal -UserId "$env:USERDOMAIN\$env:USERNAME" -LogonType Interactive -RunLevel Limited
Register-ScheduledTask -TaskName "YemotPatriots" -Action $action -Trigger @($t1,$t2) `
        -Settings $set -Principal $principal -Description "הפטריוטים המלא: הורדת התוכנית המוכנה מיוטיוב והעלאה לקו" -Force | Out-Null
Write-Host "✓ נרשמה משימה מתוזמנת 'YemotPatriots' — 23:40 ו-06:30, עם העָרה מהשינה." -ForegroundColor Green

Write-Host ""
Write-Host "ההתקנה הסתיימה." -ForegroundColor Cyan
$run = Read-Host "להריץ עכשיו פעם אחת לבדיקה? (y/n)"
if ($run -eq "y") {
    Write-Host "מריץ..." -ForegroundColor Yellow
    & $exe patriots
}
Write-Host ""
Read-Host "סיום. הקישו Enter לסגירה"
