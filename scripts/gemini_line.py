#!/usr/bin/env python3
"""כיבוי / הפעלה של שלוחת קו ג'מיני (שלוחה 7) — רץ ב-GitHub Actions (gemini-line.yml).

קורא את gemini/state.txt:
  off — השלוחה מפסיקה לפעול: ההגדרה הנוכחית (type=api + api_link) נשמרת בצד
        בקובץ gemini-off.ini בתוך השלוחה, וה-ext.ini מוחלף בהפניה לתפריט הראשי.
        הגשר מפסיק להכריז על השלוחה בתפריט לבד (geminiReady ב-main.go).
  on  — ההגדרה השמורה מוחזרת ל-ext.ini, והשלוחה חוזרת לפעול.

ה-api_link מכיל מפתח סודי — הוא לא מודפס לעולם (הריפו ציבורי), רק עובר בין
שני הקבצים בתוך מערכת ימות המשיח.
"""
import json
import os
import sys
import urllib.error
import urllib.parse
import urllib.request

BASE = "https://www.call2all.co.il/ym/api/"
KEY = os.environ.get("YEMOT_API_KEY", "").strip()
ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
EXT = os.environ.get("GEMINI_EXT", "7").strip() or "7"
INI = f"ivr2:/{EXT}/ext.ini"
BACKUP = f"ivr2:/{EXT}/gemini-off.ini"
OFF_INI = "type=go_to_folder\ngo_to_folder=/"


def call(method, **form):
    req = urllib.request.Request(BASE + method, data=urllib.parse.urlencode(form).encode(), method="POST")
    req.add_header("Content-Type", "application/x-www-form-urlencoded; charset=utf-8")
    req.add_header("Authorization", KEY)
    try:
        with urllib.request.urlopen(req, timeout=60) as r:
            body = r.read().decode("utf-8", "replace")
    except urllib.error.HTTPError as e:
        body = e.read().decode("utf-8", "replace")
    except Exception as e:  # noqa: BLE001
        sys.exit(f"שגיאת רשת ב-{method}: {e}")
    try:
        return json.loads(body)
    except ValueError:
        sys.exit(f"תגובה לא צפויה מימות המשיח ב-{method}")


def read(path):
    """תוכן קובץ טקסט, או "" כשאינו קיים."""
    r = call("GetTextFile", what=path)
    if r.get("responseStatus") != "OK":
        return ""
    return (r.get("contents") or "").replace("\r\n", "\n").strip()


def write(path, text):
    r = call("UploadTextFile", what=path, contents=text)
    if r.get("responseStatus") != "OK":
        sys.exit(f"העלאה נכשלה ({path.rsplit('/', 1)[-1]}): {r.get('responseStatus')}")
    if read(path) != text.strip():
        sys.exit(f"הקובץ {path.rsplit('/', 1)[-1]} לא נשמר כמו שנשלח — עוצר.")


def values(ini):
    out = {}
    for line in ini.splitlines():
        if "=" in line:
            k, v = line.split("=", 1)
            out[k.strip().lower()] = v.strip()
    return out


def is_api(ini):
    v = values(ini)
    return v.get("type") == "api" and bool(v.get("api_link"))


def main():
    if not KEY:
        sys.exit("חסר YEMOT_API_KEY")
    with open(os.path.join(ROOT, "gemini", "state.txt"), encoding="utf-8") as f:
        want = f.read().strip().lower()
    if want not in ("on", "off"):
        sys.exit("gemini/state.txt צריך להכיל on או off")
    cur = read(INI)
    cur_type = values(cur).get("type", "(אין)")
    print(f"שלוחה {EXT}: מצב נוכחי type={cur_type}; מבוקש: {want}")

    if want == "off":
        if is_api(cur):
            write(BACKUP, cur)
            print("ההגדרה של קו ג'מיני נשמרה בצד (gemini-off.ini).")
        elif not is_api(read(BACKUP)):
            sys.exit("השלוחה אינה מוגדרת כקו ג'מיני ואין הגדרה שמורה — לא נוגע בה.")
        else:
            print("השלוחה כבר לא מוגדרת כקו ג'מיני; ההגדרה השמורה קיימת.")
        write(INI, OFF_INI)
        print(f"שלוחה {EXT} כבויה: מפנה לתפריט הראשי.")
    else:
        if is_api(cur):
            print(f"שלוחה {EXT} כבר פעילה כקו ג'מיני — אין מה לשנות.")
            return
        saved = read(BACKUP)
        if not is_api(saved):
            sys.exit("אין הגדרה שמורה של קו ג'מיני (gemini-off.ini) — אי אפשר להפעיל.")
        write(INI, saved)
        print(f"שלוחה {EXT} פעילה שוב כקו ג'מיני.")


if __name__ == "__main__":
    main()
