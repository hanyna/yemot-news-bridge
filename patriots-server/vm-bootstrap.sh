#!/usr/bin/env bash
# התקנה אוטומטית של "הפטריוטים המלא" על מכונת Google Cloud (e2-micro / Ubuntu).
# מריצים פעם אחת:
#   curl -fsSL https://raw.githubusercontent.com/hanyna/yemot-news-bridge/main/patriots-server/vm-bootstrap.sh | sudo bash
# אחר כך קובעים את מפתח הקו:  sudo patriots-setkey
set -euo pipefail
DIR=/opt/yemot-patriots
ENVF=$DIR/patriots.env
GOVER=go1.24.7
REPO=https://github.com/hanyna/yemot-news-bridge

echo "=== התקנת 'הפטריוטים המלא' (שרת ענן) ==="
mkdir -p "$DIR/state" "$DIR/work"
export DEBIAN_FRONTEND=noninteractive

echo "[1/5] חבילות (ffmpeg, git, python)..."
apt-get update -qq >/dev/null
apt-get install -y -qq ffmpeg curl ca-certificates git python3 >/dev/null

echo "[2/5] yt-dlp..."
curl -fsSL -o /usr/local/bin/yt-dlp https://github.com/yt-dlp/yt-dlp/releases/latest/download/yt-dlp
chmod 755 /usr/local/bin/yt-dlp

echo "[3/5] Go $GOVER + בניית התוכנה..."
if ! /usr/local/go/bin/go version 2>/dev/null | grep -q "$GOVER"; then
  curl -fsSL -o /tmp/go.tgz "https://go.dev/dl/${GOVER}.linux-amd64.tar.gz"
  rm -rf /usr/local/go && tar -C /usr/local -xzf /tmp/go.tgz && rm -f /tmp/go.tgz
fi
rm -rf /opt/src && git clone --depth 1 "$REPO" /opt/src
( cd /opt/src && /usr/local/go/bin/go build -ldflags="-s -w" -o "$DIR/yemot-patriots" . )
echo "✓ נבנתה התוכנה: $($DIR/yemot-patriots patriots 2>&1 | head -1 | sed 's/^[0-9/ :]*//')"

echo "[4/5] הגדרות + שעון ישראל..."
timedatectl set-timezone Asia/Jerusalem 2>/dev/null || true
if [ ! -f "$ENVF" ]; then
cat > "$ENVF" <<EOF
# הגדרות "הפטריוטים המלא" (שרת ענן). קובעים מפתח עם: sudo patriots-setkey
YEMOT_API_KEY=
PATRIOTS_EXT=3/3
PATRIOTS_START=21:00
PATRIOTS_DAYS_BACK=3
PATRIOTS_STATE_DIR=$DIR/state
PATRIOTS_INSECURE_TLS=off
# קובץ עוגיות יוטיוב (אם יוטיוב חוסם את ה-IP). ריק = בלי
YT_COOKIES=$DIR/cookies.txt
EOF
chmod 600 "$ENVF"
fi

echo "[5/5] שירות + טיימר (23:40, 06:30) + עדכון yt-dlp יומי..."
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

# פקודות נוחות
cat > /usr/local/bin/patriots <<EOF
#!/usr/bin/env bash
case "\${1:-run}" in
  run)    sudo systemctl start yemot-patriots.service; sudo journalctl -u yemot-patriots -n 80 --no-pager -o cat ;;
  log)    sudo journalctl -u yemot-patriots -n \${2:-150} --no-pager -o cat ;;
  live)   sudo journalctl -u yemot-patriots -f -o cat ;;
  status) systemctl list-timers yemot-patriots.timer --no-pager ;;
  update) sudo bash -c 'cd /opt/src && git pull -q && /usr/local/go/bin/go build -ldflags="-s -w" -o $DIR/yemot-patriots . && echo built'; /usr/local/bin/yt-dlp -U >/dev/null 2>&1 || true; echo "updated" ;;
  *) echo "patriots [run|log|live|status|update]" ;;
esac
EOF
chmod 755 /usr/local/bin/patriots
cat > /usr/local/bin/patriots-setkey <<EOF
#!/usr/bin/env bash
read -s -p "הדביקו את מפתח הקו (לא יוצג): " K; echo
[ -n "\$K" ] || { echo "לא הוזן מפתח."; exit 1; }
sudo sed -i "s|^YEMOT_API_KEY=.*|YEMOT_API_KEY=\$K|" $ENVF
echo "✓ המפתח נשמר. מריץ בדיקה..."
sudo systemctl start yemot-patriots.service
sudo journalctl -u yemot-patriots -n 60 --no-pager -o cat
EOF
chmod 755 /usr/local/bin/patriots-setkey

echo
echo "=== מוכן ==="
echo "קבעו מפתח קו:   sudo patriots-setkey"
echo "פקודות:  patriots | patriots log | patriots status | patriots update"
