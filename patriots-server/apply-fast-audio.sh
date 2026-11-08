#!/usr/bin/env bash
# תיקון מהירות ההמרה על השרת: dynaudnorm מהיר במקום loudnorm האיטי + timeout ארוך.
# מריצים על המכונה (מתוך Cloud Shell):
#   gcloud compute ssh patriots --zone=us-central1-a -- 'curl -fsSL https://raw.githubusercontent.com/hanyna/yemot-news-bridge/main/patriots-server/apply-fast-audio.sh | sudo bash'
set -euo pipefail
DIR=/opt/yemot-patriots
ENVF=$DIR/patriots.env

echo "[1/3] מגדיר המרה מהירה ב-$ENVF..."
grep -q '^XCODE_PODCAST_NORM=' "$ENVF" || echo 'XCODE_PODCAST_NORM=dynaudnorm' >> "$ENVF"
grep -q '^XCODE_TIMEOUT_MIN='   "$ENVF" || echo 'XCODE_TIMEOUT_MIN=40'        >> "$ENVF"
sed -i 's/^XCODE_PODCAST_NORM=.*/XCODE_PODCAST_NORM=dynaudnorm/; s/^XCODE_TIMEOUT_MIN=.*/XCODE_TIMEOUT_MIN=40/' "$ENVF"
echo "    $(grep -E '^XCODE_' "$ENVF" | tr '\n' ' ')"

echo "[2/3] מושך ובונה מחדש..."
cd /opt/src && git pull -q
/usr/local/go/bin/go build -ldflags='-s -w' -o "$DIR/yemot-patriots" .
echo "    ✓ נבנה: $("$DIR/yemot-patriots" patriots 2>&1 | head -1 | sed 's/^[0-9/ :]*//')"

echo "[3/3] מריץ עכשיו (ברקע)..."
systemctl start --no-block yemot-patriots.service
echo "    ✓ רץ. מעקב:  patriots live   |   סטטוס אחרון בעוד כמה דקות ב-GitHub/דשבורד."
