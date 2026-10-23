#!/usr/bin/env python3
"""מבזק לקו — רץ ב-GitHub Actions (alert.yml), עם המפתח מ-Secrets.

קורא את alert/message.txt:
  שורה ראשונה "#בדוק"  — רק לבדוק כמה מנויים ברשימת הצינתוקים (בלי להעלות ובלי לצנטק).
  שורה ראשונה "#מחק"   — למחוק את ההודעה הזמנית (M0000.tts) מהשלוחה הראשית.
  אחרת               — הטקסט מועלה כהודעה זמנית M0000.tts (מושמעת בכניסה לקו, לפני
                       התפריט) ונשלח צינתוק לרשימה (alert/list.txt, ברירת מחדל 800).
התוצאה נכתבת ל-alert/result.md (ומשם ל-Issue), כדי שאפשר יהיה לקרוא אותה בלי הלוג.

מפתחות (Secrets): YEMOT_API_KEY — קבצים (ההודעה הזמנית). YEMOT_TZINTUK_TOKEN —
אופציונלי, "מספר-מערכת:סיסמה", לצינתוק ולקריאת הרשימה כשלמפתח ה-API אין הרשאה
למודול הצינתוקים (API_KEY_ACL_REJECT). כשאין — משתמשים במפתח ה-API גם לזה.
"""
import json
import os
import re
import sys
import urllib.parse
import urllib.request

BASE = "https://www.call2all.co.il/ym/api/"
KEY = os.environ.get("YEMOT_API_KEY", "").strip()
TZ_TOKEN = os.environ.get("YEMOT_TZINTUK_TOKEN", "").strip()
ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
ALERT = os.path.join(ROOT, "alert")
TEMP_FILE = "ivr2:/M0000.tts"


def call(method, token=None, **form):
    """token=None: מפתח ה-API בכותרת. אחרת (מספר-מערכת:סיסמה) — בפרמטר token."""
    if token:
        form["token"] = token
    data = urllib.parse.urlencode(form).encode()
    req = urllib.request.Request(BASE + method, data=data, method="POST")
    req.add_header("Content-Type", "application/x-www-form-urlencoded; charset=utf-8")
    if not token:
        req.add_header("Authorization", KEY)
    try:
        with urllib.request.urlopen(req, timeout=60) as r:
            body = r.read().decode("utf-8", "replace")
    except urllib.error.HTTPError as e:
        body = e.read().decode("utf-8", "replace") or f"HTTP {e.code}"
    except Exception as e:  # noqa: BLE001
        return None, f"שגיאת רשת: {e}"
    try:
        return json.loads(body), body
    except ValueError:
        return None, body


def read(name, default=""):
    p = os.path.join(ALERT, name)
    if not os.path.exists(p):
        return default
    with open(p, encoding="utf-8") as f:
        return f.read().strip() or default


def mask(s):
    """מסתיר מספרי טלפון וסיסמאות בדוח (נשאר רק 3 ספרות אחרונות)."""
    s = re.sub(r"0\d{1,2}-?\d{7}", lambda m: "***" + m.group(0)[-3:], s)
    if TZ_TOKEN:
        s = s.replace(TZ_TOKEN, "***")
    return s


def count_list(list_id):
    """מנסה כמה דרכים לקרוא את רשימת הצינתוקים ולספור מנויים."""
    lines = []
    found = None
    attempts = [
        ("TzintukimListManagement", {"action": "get", "list": list_id}),
        ("TzintukimListManagement", {"action": "list", "listId": list_id}),
        ("GetTzintukList", {"list": list_id}),
    ]
    for method, form in attempts:
        r, body = call(method, token=TZ_TOKEN or None, **form)
        snippet = mask(body[:600].replace("\n", " "))
        lines.append(f"- `{method}` {json.dumps(form, ensure_ascii=False)} → {snippet}")
        if not r or r.get("responseStatus") != "OK":
            continue
        text = json.dumps(r, ensure_ascii=False)
        phones = set(re.findall(r"0\d{8,9}", text))
        if phones:
            found = len(phones)
            break
    return found, lines


def main():
    if not KEY:
        print("אין YEMOT_API_KEY", file=sys.stderr)
        sys.exit(1)
    msg = read("message.txt")
    list_id = read("list.txt", "800")
    first = msg.splitlines()[0].strip() if msg else ""
    report = []
    how = "מספר-מערכת:סיסמה" if TZ_TOKEN else "מפתח ה-API"

    if first.startswith("#בדוק") or not msg:
        n, lines = count_list(list_id)
        report.append(f"## בדיקת רשימת צינתוקים {list_id} (עם {how})")
        report.append(f"**מנויים: {n if n is not None else 'לא הצלחתי לקרוא את הרשימה'}**")
        report.extend(lines)
    elif first.startswith("#מחק"):
        r, body = call("FileAction", action="delete", what=TEMP_FILE)
        ok = bool(r) and r.get("responseStatus") == "OK"
        report.append("## מחיקת ההודעה הזמנית")
        report.append(("✅ נמחקה" if ok else "⚠️ נכשל: ") + ("" if ok else mask(body[:300])))
    else:
        text = " ".join(l.strip() for l in msg.splitlines() if l.strip())
        report.append("## מבזק")
        report.append(f"> {text}")
        r, body = call("UploadTextFile", what=TEMP_FILE, contents=text)
        ok1 = bool(r) and r.get("responseStatus") == "OK"
        report.append(("✅ ההודעה הזמנית הועלתה" if ok1 else "⚠️ העלאה נכשלה: " + mask(body[:300])))
        if ok1:
            r, body = call("RunTzintuk", token=TZ_TOKEN or None, phones=f"tzl:{list_id}")
            ok2 = bool(r) and r.get("responseStatus") == "OK"
            report.append((f"✅ צינתוק נשלח לרשימה {list_id} (עם {how}) — " if ok2 else f"⚠️ צינתוק נכשל (עם {how}): ")
                          + mask(body[:300]))
            n, _ = count_list(list_id)
            if n is not None:
                report.append(f"מנויים ברשימה: {n}")
        if not ok1:
            sys.exit(1)

    out = "\n".join(report)
    print(out)
    with open(os.path.join(ALERT, "result.md"), "w", encoding="utf-8") as f:
        f.write(out + "\n")


if __name__ == "__main__":
    main()
