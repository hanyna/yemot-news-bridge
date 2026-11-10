#!/usr/bin/env python3
"""התראת SMS יומית — "הפטריוטים לא עלו". רצה ב-GitHub Actions (patriots-sms.yml).

קוראת את ה-Issue "מצב הקו — דוח אוטומטי" (status.go מעדכן אותו), מוציאה את
meta.patriots, ושולחת SMS דרך ימות המשיח (SendSms) — רק אם משהו לא תקין:
  - failed > 0             — ההחלפה בגרסה המוכנה נכשלה בפרק אחד או יותר.
  - last_run ישן מ-26 שעות  — כלי הפטריוטים (ה-VM) לא רץ.
  - אין דוח מצב / אין נתוני פטריוטים — אי אפשר לאמת שהפטריוטים עלו.
אם הכול תקין — לא נשלח דבר (זו הכוונה: SMS רק כשלא עלה).

השדות done / last_replaced מתאפסים בכל ריצה של הכלי, ולכן במצב תקין הם 0/ריק —
אסור להסתמך עליהם כדי לקבוע "עלה/לא עלה". לכן הבדיקה נשענת על failed ועל last_run.

מריצים אחרי ריצת הבוקר של ה-VM (06:30 שעון ישראל) + קריאת הגשר (עד 30 דק'Ԃ9.

סודות (Secrets, ב-Settings ← Secrets and variables ← Actions):
  YEMOT_API_KEY   — מפתח ה-API של ימות (אותו אחד של הגשר/המבזק).
  SMS_TO          — מספר/מספרי היעד (מופרדים בפסיק). חובה — בלי זה לא נשלח SMS.
  SMS_FROM        — אופציונלי: שם/מספר השולח, אם מוגדר ומאושר בחשבון ימות.
  YEMOT_SMS_TOKEN — אופציונלי: "מספר-מערכת:סיסמה", אם למפתח ה-API אין הרשאת SMS.

הרצה ידנית (workflow_dispatch) עם test=1 — שולחת SMS בדיקה מיד, בלי קשר למצב.
"""
import json
import os
import re
import sys
import urllib.error
import urllib.parse
import urllib.request
from datetime import datetime, timedelta, timezone

API = os.environ.get("GITHUB_API_URL", "https://api.github.com")
REPO = os.environ.get("GITHUB_REPOSITORY", "hanyna/yemot-news-bridge")
GH_TOKEN = os.environ.get("GITHUB_TOKEN", "")
STATUS_TITLE = "מצב הקו — דוח אוטומטי"

YM_BASE = "https://www.call2all.co.il/ym/api/"
YM_KEY = os.environ.get("YEMOT_API_KEY", "").strip()
YM_SMS_TOKEN = os.environ.get("YEMOT_SMS_TOKEN", "").strip()
SMS_TO = os.environ.get("SMS_TO", "").strip()
SMS_FROM = os.environ.get("SMS_FROM", "").strip()

STALE_PATRIOTS = timedelta(hours=26)  # כלי הפטריוטים רץ פעמיים ביום — 26ש' = פספוס ודאי

try:
    from zoneinfo import ZoneInfo
    IL = ZoneInfo("Asia/Jerusalem")
except Exception:  # noqa: BLE001
    IL = timezone(timedelta(hours=2))

META = re.compile(r"<!-- line-status (\{.*?\}) -->")


def gh(path):
    req = urllib.request.Request(API + path, method="GET")
    req.add_header("Authorization", "Bearer " + GH_TOKEN)
    req.add_header("Accept", "application/vnd.github+json")
    with urllib.request.urlopen(req, timeout=30) as r:  # noqa: S310
        return json.loads(r.read())


def parse_time(s):
    try:
        return datetime.fromisoformat(s.replace("Z", "+00:00")) if s else None
    except (ValueError, AttributeError):
        return None


def il(dt):
    return dt.astimezone(IL).strftime("%d.%m %H:%M") if dt else "—"


def mask(s):
    for sec in (SMS_TO, YM_KEY, YM_SMS_TOKEN):
        if sec:
            s = s.replace(sec, "***")
    return s


def status_issue():
    """ה-Issue של מצב הקו — קודם מנסים #1 (שם הוא נמצא), אחרת מחפשים לפי הכותרת."""
    try:
        one = gh(f"/repos/{REPO}/issues/1")
        if one.get("title") == STATUS_TITLE and "pull_request" not in one:
            return one
    except urllib.error.HTTPError:
        pass
    for i in gh(f"/repos/{REPO}/issues?state=open&per_page=100"):
        if i.get("title") == STATUS_TITLE and "pull_request" not in i:
            return i
    return None


def meta_of(issue):
    m = META.search((issue or {}).get("body") or "")
    if not m:
        return None
    try:
        return json.loads(m.group(1))
    except ValueError:
        return None


def send_sms(message):
    if not SMS_TO:
        print("אין SMS_TO (מספר יעד) — לא נשלח SMS. הוסיפו Secret בשם SMS_TO.")
        return False, "no SMS_TO"
    if not (YM_KEY or YM_SMS_TOKEN):
        print("אין YEMOT_API_KEY / YEMOT_SMS_TOKEN — לא נשלח SMS.")
        return False, "no key"
    form = {"phones": SMS_TO, "message": message}
    if SMS_FROM:
        form["from"] = SMS_FROM
    if YM_SMS_TOKEN:
        form["token"] = YM_SMS_TOKEN
    data = urllib.parse.urlencode(form).encode()
    req = urllib.request.Request(YM_BASE + "SendSms", data=data, method="POST")  # noqa: S310
    req.add_header("Content-Type", "application/x-www-form-urlencoded; charset=utf-8")
    if not YM_SMS_TOKEN:
        req.add_header("Authorization", YM_KEY)
    try:
        with urllib.request.urlopen(req, timeout=60) as r:  # noqa: S310
            body = r.read().decode("utf-8", "replace")
    except urllib.error.HTTPError as e:
        body = e.read().decode("utf-8", "replace") or f"HTTP {e.code}"
    except Exception as e:  # noqa: BLE001
        print("שגיאת רשת בשליחת SMS:", e)
        return False, str(e)
    print("תגובת ימות ל-SendSms:", mask(body))
    try:
        resp = json.loads(body)
        ok = str(resp.get("responseStatus", "")).upper() == "OK"
    except ValueError:
        ok = False
    return ok, body


def main():
    test = os.environ.get("SMS_TEST", "").strip().lower() in ("1", "true", "yes", "on")
    if test:
        ok, _ = send_sms("בדיקת התראת הפטריוטים — ההתראה מוגדרת ועובדת. (הודעת בדיקה)")
        print("בדיקה:", "נשלח" if ok else "נכשל")
        sys.exit(0 if ok else 1)

    now = datetime.now(timezone.utc)
    issue = status_issue()
    meta = meta_of(issue)
    reasons = []

    if issue is None:
        reasons.append("אין דוח מצב של הקו — כנראה הגשר לא רץ.")
    p = (meta or {}).get("patriots")
    if issue is not None and not p:
        reasons.append("אין נתוני פטריוטים בדוח — אי אפשר לאמת שהתוכנית עלתה.")
    elif p:
        failed = int(p.get("failed") or 0)
        last_run = parse_time(p.get("last_run"))
        found = p.get("found") or []
        if failed > 0:
            dates = ", ".join(str(d) for d in found[:3])
            tail = f" ({dates})" if dates else ""
            reasons.append(f"{failed} פרקים נכשלו בהחלפה לגרסה המוכנה{tail}.")
        if last_run is None or now - last_run > STALE_PATRIOTS:
            reasons.append(f"כלי הפטריוטים לא רץ מאז {il(last_run)} — ייתכן שה-VM מושבת.")

    if not reasons:
        print("הפטריוטים תקינים — לא נשלח SMS.")
        return

    msg = "הקו — הפטריוטים לא עלו: " + " ".join(reasons) + " בדקו את דוח מצב הקו."
    print("סיבות להתראה:", msg)
    ok, _ = send_sms(msg)
    if not ok:
        print("שליחת ה-SMS נכשלה — ראו את התגובה למעלה.", file=sys.stderr)
        sys.exit(1)
    print("נשלח SMS.")


if __name__ == "__main__":
    main()
