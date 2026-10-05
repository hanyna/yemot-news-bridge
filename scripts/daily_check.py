#!/usr/bin/env python3
"""בדיקה יומית של הקו — רצה ב-GitHub Actions (daily-check.yml), בלי Claude ובלי אישורים.

קוראת את ה-Issue "מצב הקו — דוח אוטומטי" שהגשר מעדכן (status.go), מסכמת את
24 השעות האחרונות — תקלות שמשביתות ופגיעה באיכות — ופותחת Issue "דוח יומי".
GitHub שולח על Issue חדש מייל לבעל המאגר. הדוח של אתמול נסגר.
בתקלה חמורה הריצה גם נכשלת → GitHub שולח עוד מייל על ריצה שנכשלה.
"""
import json
import os
import re
import sys
import urllib.parse
import urllib.request
from datetime import datetime, timedelta, timezone

API = os.environ.get("GITHUB_API_URL", "https://api.github.com")
REPO = os.environ.get("GITHUB_REPOSITORY", "hanyna/yemot-news-bridge")
TOKEN = os.environ.get("GITHUB_TOKEN", "")
STATUS_TITLE = "מצב הקו — דוח אוטומטי"
DAILY_PREFIX = "דוח יומי"
STALE = timedelta(minutes=30)
IL = timezone(timedelta(hours=3))  # רק להצגה
try:
    from zoneinfo import ZoneInfo
    IL = ZoneInfo("Asia/Jerusalem")
except Exception:
    pass

META = re.compile(r"<!-- line-status (\{.*?\}) -->")


def api(method, path, body=None):
    req = urllib.request.Request(API + path, method=method)
    req.add_header("Authorization", "Bearer " + TOKEN)
    req.add_header("Accept", "application/vnd.github+json")
    data = None
    if body is not None:
        data = json.dumps(body).encode()
        req.add_header("Content-Type", "application/json")
    with urllib.request.urlopen(req, data, timeout=30) as r:
        raw = r.read()
    return json.loads(raw) if raw else None


def paged(path):
    out, page = [], 1
    sep = "&" if "?" in path else "?"
    while True:
        items = api("GET", f"{path}{sep}per_page=100&page={page}")
        out += items
        if len(items) < 100 or page >= 10:
            return out
        page += 1


def parse_time(s):
    return datetime.fromisoformat(s.replace("Z", "+00:00")) if s else None


def meta_of(text):
    m = META.search(text or "")
    if not m:
        return None
    try:
        return json.loads(m.group(1))
    except ValueError:
        return None


def il(dt):
    return dt.astimezone(IL).strftime("%H:%M") if dt else "—"


def mins(sec):
    sec = float(sec or 0)
    return f"{int(sec)} שניות" if sec < 60 else f"{sec / 60:.1f} דקות"


def main():
    now = datetime.now(timezone.utc)
    since = now - timedelta(hours=24)
    today = now.astimezone(IL).strftime("%d.%m.%Y")

    issues = [i for i in paged(f"/repos/{REPO}/issues?state=open")
              if "pull_request" not in i]
    status = next((i for i in issues if i["title"] == STATUS_TITLE), None)

    severe, quality_notes, todo = [], [], []
    q = {}
    lat_n = lat_slow = 0
    lat_sum = lat_max = 0.0
    runs = cycles = failed = 0
    stopped = 0
    current = None

    if not status:
        severe.append("לא נמצא דוח מצב בכלל — כנראה שהגשר לא רץ.")
    else:
        current = meta_of(status["body"])
        comments = [c for c in paged(f"/repos/{REPO}/issues/{status['number']}/comments?since="
                                     + since.strftime("%Y-%m-%dT%H:%M:%SZ"))]
        metas = [m for m in (meta_of(c["body"]) for c in comments) if m]
        # ההפעלה הנוכחית (עדיין לא הסתיימה — אין לה תגובת סיכום)
        if current and not current.get("final"):
            metas.append(current)
        for m in metas:
            started = parse_time(m.get("started"))
            if started and started < since and m is not current:
                continue
            runs += 1
            cycles += m.get("cycles", 0)
            failed += m.get("failed_cycles", 0)
            if "נעצרה בתקלה" in (m.get("final") or ""):
                stopped += 1
            for k, v in (m.get("quality") or {}).items():
                q[k] = q.get(k, 0) + v
            lat = m.get("latency") or {}
            n = lat.get("messages", 0)
            lat_n += n
            lat_sum += lat.get("avg_sec", 0) * n
            lat_max = max(lat_max, lat.get("max_sec", 0))
            lat_slow += lat.get("over_5min", 0)

        updated = parse_time((current or {}).get("updated")) or parse_time(status["updated_at"])
        if not updated or now - updated > STALE:
            ago = int((now - updated).total_seconds() // 60) if updated else 0
            severe.append(f"הדוח לא התעדכן כבר {ago} דקות (מאז {il(updated)}) — הגשר לא רץ, והקו לא מתעדכן.")
            todo.append("להיכנס לטאב Actions במאגר ולבדוק את ההרצה האחרונה של \"Telegram to Yemot bridge\". "
                        "אם אין הרצה פעילה — ללחוץ Run workflow.")
        if current and current.get("fail_streak", 0) >= 3:
            severe.append(f"כרגע {current['fail_streak']} סבבים כושלים ברצף.")
        if stopped:
            severe.append(f"{stopped} הפעלות נעצרו בתקלה (10 סבבים כושלים ברצף).")

    # הרצות שנכשלו ב-GitHub Actions ב-24 השעות
    try:
        wr = api("GET", f"/repos/{REPO}/actions/workflows/bridge.yml/runs?per_page=50&created="
                 + urllib.parse.quote(">=" + since.strftime("%Y-%m-%dT%H:%M:%SZ")))
        failures = [r for r in wr.get("workflow_runs", []) if r.get("conclusion") == "failure"]
        if failures:
            severe.append(f"{len(failures)} הרצות של הגשר נכשלו. האחרונה: {failures[0]['html_url']}")
    except Exception as e:  # לא קריטי
        print("לא הצלחתי לקרוא את ההרצות:", e)

    # איכות
    voiced = q.get("voiced", 0)
    robotic = q.get("voice_failed", 0)
    if q.get("voice_paused", 0):
        quality_notes.append(f"הקול המוכן הושהה {q['voice_paused']} פעמים — בזמן הזה הודעות הושמעו בקול הממוחשב.")
        todo.append("כנראה שהמכסה של מפתחות הקול של Google נגמרת. אפשר להוסיף מפתח נוסף "
                    "(GEMINI_API_KEY_2 עד _5 ב-Settings → Secrets של המאגר).")
    if robotic:
        quality_notes.append(f"{robotic} הודעות נשארו בקול הממוחשב (הקול המוכן נכשל).")
    if q.get("photo_failed", 0):
        quality_notes.append(f"{q['photo_failed']} תמונות בלי תיאור (ניתוח התמונה נכשל).")
    if q.get("media_failed", 0):
        quality_notes.append(f"{q['media_failed']} סרטונים/הודעות קוליות שהקול שלהם לא עלה.")
    if q.get("media_retry", 0) >= 5:
        quality_notes.append(f"{q['media_retry']} פעמים קול של סרטון נכשל ונוסה שוב.")
    if q.get("tg_blocked", 0) >= 5:
        quality_notes.append(f"טלגרם חסם קריאה של ערוץ {q['tg_blocked']} פעמים — הודעות התעכבו.")
    if q.get("skipped", 0):
        severe.append(f"{q['skipped']} הודעות דולגו ולא עלו לקו בכלל!")
    if lat_slow:
        quality_notes.append(f"{lat_slow} הודעות עלו לקו יותר מ-5 דקות אחרי שפורסמו בטלגרם.")

    if severe:
        icon, head = "⚠️", "תקלות בקו"
    elif quality_notes:
        icon, head = "🟡", "הקו עובד, יש פגיעה באיכות"
    else:
        icon, head = "✅", "הקו תקין"
    title = f"{DAILY_PREFIX}: {icon} {head} — {today}"

    lines = [f"## {icon} {head}", ""]
    if severe:
        lines += ["### תקלות", *[f"- {s}" for s in severe], ""]
    lines += ["### 24 השעות האחרונות",
              f"- הפעלות של הגשר: {runs}, סבבי בדיקה: {cycles}, מהם נכשלו: {failed}",
              f"- הודעות שעלו לקו: {lat_n}",
              f"- קבצי קול מוכן שעלו (כל הודעה עולה לשלוחה 1 ולשלוחת הכתב): {voiced}, הודעות שנשארו בקול הממוחשב: {robotic}, השהיות של הקול המוכן: {q.get('voice_paused', 0)}",
              f"- תמונות עם תיאור: {q.get('photo_ok', 0)}, בלי תיאור: {q.get('photo_failed', 0)}",
              f"- קול של סרטונים/קוליות שעלה: {q.get('media_ok', 0)}, לא עלה: {q.get('media_failed', 0)}",
              f"- חסימות של טלגרם: {q.get('tg_blocked', 0)}, הודעות שדולגו: {q.get('skipped', 0)}"]
    if lat_n:
        lines.append(f"- עיכוב מהפרסום בטלגרם עד הקו: ממוצע {mins(lat_sum / lat_n)}, הכי איטי {mins(lat_max)}")
    lines.append("")
    if quality_notes:
        lines += ["### פגיעה באיכות", *[f"- {s}" for s in quality_notes], ""]
    if todo:
        lines += ["### מה לעשות", *[f"- {s}" for s in todo], ""]
    if status:
        lines.append(f"דוח המצב המלא: {status['html_url']}")
    body = "\n".join(lines)
    print(title)
    print(body)

    # הדוחות היומיים הקודמים נסגרים — נשאר פתוח רק של היום
    for i in issues:
        if i["title"].startswith(DAILY_PREFIX):
            api("PATCH", f"/repos/{REPO}/issues/{i['number']}", {"state": "closed"})
    api("POST", f"/repos/{REPO}/issues", {"title": title, "body": body})

    if severe:
        sys.exit(1)  # ריצה שנכשלה → עוד מייל מ-GitHub


if __name__ == "__main__":
    main()
