#!/usr/bin/env bash
# התקנת "הפטריוטים המלא" על שרת לינוקס בענן (Oracle Cloud Always Free / כל Ubuntu-Debian).
# מה הסקריפט עושה:
#   1. מתקין ffmpeg + yt-dlp (ומעדכן את yt-dlp אוטומטית פעם ביום — יוטיוב משתנה לעתים קרובות)
#   2. מעתיק את yemot-patriots ל-/opt/yemot-patriots ושומר את ההגדרות ב-patriots.env
#   3. רושם טיימר (systemd) שמריץ את הכלי כל יום ב-23:40 וב-06:30 שעון ישראל
#   4. מריץ פעם אחת עכשיו כדי לבדוק שהכל עובד
# הרצה:  sudo bash install.sh        (מהתיקייה שבה הקבצים)
set -euo pipefail

DIR=/opt/yemot-patriots
ENVF=$DIR/patriots.env
SRC="$(cd "$(dirname "$0")" && pwd)"

if [ "$(id -u)" -ne 0 ]; then
  echo "יש להריץ עם sudo:  sudo bash install.sh"; exit 1
fi

echo
echo "=== התקנת 'הפטריוטים המלא' על השרת ==="
mkdir -p "$DIR/state" "$DIR/work"

# 1) הבינארי המתאים למעבד (ARM = Oracle A1, AMD64 = E2.Micro / כל שרת רגיל)
case "$(uname -m)" in
  aarch64|arm64) BIN="$SRC/yemot-patriots-arm64" ;;
  x86_64|amd64)  BIN="$SRC/yemot-patriots-amd64" ;;
  *) echo "מעבד לא מוכר: $(uname -m)"; exit 1 ;;
esac
[ -f "$BIN" ] || { echo "לא נמצא $BIN ליד הסקריפט"; exit 1; }
install -m 755 "$BIN" "$DIR/yemot-patriots"
echo "✓ התוכנה הותקנה ב-$DIR"

# 2) מפתח הקו
EXISTING=""
if [ -f "$ENVF" ]; then
  EXISTING="$(grep -E '^YEMOT_API_KEY=' "$ENVF" | head -1 | cut -d= -f2- | tr -d '\r' || true)"
fi
if [ -n "$EXISTING" ]; then
  echo "✓ כבר קיים מפתח קו שמור. Enter = להשאיר, או הדביקו מפתח חדש:"
else
  echo "הדביקו את המפתח הקבוע של הקו (מעמוד 'מפתחות גישה' בימות המשיח):"
fi
read -r -s -p "מפתח: " KEY < /dev/tty; echo
KEY="${KEY:-$EXISTING}"
[ -n "$KEY" ] || { echo "לא הוזן מפתח."; exit 1; }

# 3) עוגיות יוטיוב (אופציונלי, אבל מומלץ בשרת ענן — יוטיוב חוסם לפעמים כתובות של שרתים)
COOK=""
if [ -f "$SRC/cookies.txt" ]; then
  install -m 600 "$SRC/cookies.txt" "$DIR/cookies.txt"; COOK="$DIR/cookies.txt"
  echo "✓ נמצא cookies.txt — יוטיוב יזהה את החשבון שלך"
elif [ -f "$DIR/cookies.txt" ]; then
  COOK="$DIR/cookies.txt"; echo "✓ משתמשים ב-cookies.txt הקיים"
else
  echo "ℹ אין cookies.txt (אפשר להוסיף אחר-כך: להעתיק ל-$DIR/cookies.txt)"
fi

# 4) ffmpeg + yt-dlp
export DEBIAN_FRONTEND=noninteractive
echo "מתקין ffmpeg (דקה-שתיים)..."
apt-get update -qq >/dev/null
apt-get install -y -qq ffmpeg curl ca-certificates python3 >/dev/null
echo "מוריד yt-dlp עדכני..."
curl -fsSL -o /usr/local/bin/yt-dlp https://github.com/yt-dlp/yt-dlp/releases/latest/download/yt-dlp
chmod 755 /usr/local/bin/yt-dlp
echo "✓ ffmpeg $(ffmpeg -version | head -1 | cut -d' ' -f3)  |  yt-dlp $(yt-dlp --version)"

# 5) שעון ישראל — הכלי מחשב לפי Asia/Jerusalem בכל מקרה, אבל שהלוגים יהיו קריאים
timedatectl set-timezone Asia/Jerusalem 2>/dev/null || true

# 6) קובץ הגדרות
cat > "$ENVF" <<EOF
# הגדרות "הפטריוטים המלא" (שרת). שורה שמתחילה ב-# היא הערה.
YEMOT_API_KEY=$KEY
PATRIOTS_EXT=3/3
PATRIOTS_START=21:00
PATRIOTS_DAYS_BACK=3
PATRIOTS_STATE_DIR=$DIR/state
# בשרת אין סינון נטפרי — אימות TLS רגיל
PATRIOTS_INSECURE_TLS=off
# קובץ עוגיות של יוטיוב (ייצוא מהדפדפן בפורמט Netscape). ריק = בלי
YT_COOKIES=$COOK
# PATRIOTS_CHANNEL=https://www.youtube.com/@c14news
# PATRIOTS_CHANNEL_NAME=C14
# PATRIOTS_SHOW=הפטריוטים
EOF
chmod 600 "$ENVF"
echo "✓ הגדרות נשמרו ב-$ENVF"

# 7) שירות + טיימר
cat > /etc/systemd/system/yemot-patriots.service <<EOF
[Unit]
Description=Yemot Patriots - upload full episode to the line
After=network-online.target
Wants=network-online.target

[Service]
Type=oneshot
WorkingDirectory=$DIR/work
EnvironmentFile=$ENVF
ExecStart=$DIR/yemot-patriots patriots
TimeoutStartSec=3600
Nice=10
EOF

cat > /etc/systemd/system/yemot-patriots.timer <<'EOF'
[Unit]
Description=Yemot Patriots - 23:40 and 06:30 Israel time

[Timer]
OnCalendar=*-*-* 23:40:00 Asia/Jerusalem
OnCalendar=*-*-* 06:30:00 Asia/Jerusalem
Persistent=true
RandomizedDelaySec=120

[Install]
WantedBy=timers.target
EOF

# עדכון yt-dlp יומי (יוטיוב משנה דברים, ו-yt-dlp ישן מפסיק לעבוד)
cat > /etc/systemd/system/ytdlp-update.service <<'EOF'
[Unit]
Description=Update yt-dlp

[Service]
Type=oneshot
ExecStart=/usr/local/bin/yt-dlp -U
EOF
cat > /etc/systemd/system/ytdlp-update.timer <<'EOF'
[Unit]
Description=Update yt-dlp daily

[Timer]
OnCalendar=*-*-* 22:30:00 Asia/Jerusalem
Persistent=true

[Install]
WantedBy=timers.target
EOF

systemctl daemon-reload
systemctl enable --now yemot-patriots.timer ytdlp-update.timer >/dev/null
echo "✓ טיימר נרשם: כל יום 23:40 ו-06:30 (שעון ישראל)"

# 8) פקודות נוחות
cat > /usr/local/bin/patriots <<EOF
#!/usr/bin/env bash
# patriots            = הרצה עכשיו (עם פלט על המסך)
# patriots log        = הלוג של ההרצות האחרונות
# patriots status     = מתי ההרצה הבאה
case "\${1:-run}" in
  run)    sudo systemctl start yemot-patriots.service; sudo journalctl -u yemot-patriots -n 60 --no-pager -o cat ;;
  log)    sudo journalctl -u yemot-patriots -n \${2:-120} --no-pager -o cat ;;
  live)   sudo journalctl -u yemot-patriots -f -o cat ;;
  status) systemctl list-timers yemot-patriots.timer --no-pager ;;
  *) echo "patriots [run|log|live|status]" ;;
esac
EOF
chmod 755 /usr/local/bin/patriots

echo
echo "=== הרצת בדיקה עכשיו (עד כמה דקות) ==="
systemctl start yemot-patriots.service || true
journalctl -u yemot-patriots -n 60 --no-pager -o cat | tail -40
echo
echo "=== הושלם ==="
echo "פקודות:  patriots  (הרצה עכשיו)   patriots log  (לוג)   patriots status  (ההרצה הבאה)"
systemctl list-timers yemot-patriots.timer --no-pager | head -3
