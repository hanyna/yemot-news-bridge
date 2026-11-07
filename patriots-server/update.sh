#!/usr/bin/env bash
# עדכון גרסה של "הפטריוטים המלא" בשרת — מחליף רק את התוכנה, ההגדרות נשארות.
# הרצה:  sudo bash update.sh
set -euo pipefail
DIR=/opt/yemot-patriots
SRC="$(cd "$(dirname "$0")" && pwd)"
[ "$(id -u)" -eq 0 ] || { echo "יש להריץ עם sudo:  sudo bash update.sh"; exit 1; }
[ -f "$DIR/patriots.env" ] || { echo "לא מותקן עדיין — הריצו קודם install.sh מהחבילה המלאה"; exit 1; }
case "$(uname -m)" in
  aarch64|arm64) BIN="$SRC/yemot-patriots-arm64" ;;
  x86_64|amd64)  BIN="$SRC/yemot-patriots-amd64" ;;
  *) echo "מעבד לא מוכר: $(uname -m)"; exit 1 ;;
esac
install -m 755 "$BIN" "$DIR/yemot-patriots"
if [ -f "$SRC/cookies.txt" ]; then
  install -m 600 "$SRC/cookies.txt" "$DIR/cookies.txt"
  grep -q '^YT_COOKIES=.' "$DIR/patriots.env" || sed -i "s|^YT_COOKIES=.*|YT_COOKIES=$DIR/cookies.txt|" "$DIR/patriots.env"
  echo "✓ cookies.txt עודכן"
fi
/usr/local/bin/yt-dlp -U >/dev/null 2>&1 || true
echo "✓ עודכן. מריץ בדיקה..."
systemctl start yemot-patriots.service || true
journalctl -u yemot-patriots -n 40 --no-pager -o cat
