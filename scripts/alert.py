#!/usr/bin/env python3
"""מבזק לקו — רץ ב-GitHub Actions (alert.yml), עם המפתחות מ-Secrets.

קורא את alert/message.txt:
  שורה ראשונה "#בדוק"  — רק לבדוק כמה מנויים ברשימת הצינתוקים (בלי להעלות ובלי לצנטק).
  שורה ראשונה "#מחק"   — למחוק את ההודעה הזמנית (M0000) מהשלוחה הראשית.
  שורה ראשונה "#בלי-צינתוק" — להעלות את ההודעה בלי לשלוח צינתוק (עדכון נוסח / קול).
  אחרת               — הטקסט מועלה כהודעה זמנית M0000 (מושמעת בכניסה לקו, לפני
                       התפריט) ונשלח צינתוק לרשימה (alert/list.txt, ברירת מחדל 800).

הקול: קודם מנסים קול מוכן של Google (אותם מודלי TTS כמו בגשר, voice.go) עם הנחיית
סגנון דרמטית של "מבזק" — ומעלים M0000.wav. אם אין מפתח / מכסה / תקלה — M0000.tts
(הקול הממוחשב של ימות המשיח), כדי שהמבזק יעלה בכל מקרה.
התוצאה נכתבת ל-alert/result.md (ומשם ל-Issue), כדי שאפשר יהיה לקרוא אותה בלי הלוג.

מפתחות (Secrets): YEMOT_API_KEY — ימות המשיח. YEMOT_TZINTUK_TOKEN — אופציונלי,
"מספר-מערכת:סיסמה", לצינתוק כשלמפתח ה-API אין הרשאה למודול הצינתוקים.
GEMINI_API_KEY (ו-_2.._5) — הקול המוכן.
"""
import base64
import json
import os
import re
import struct
import sys
import urllib.error
import urllib.parse
import urllib.request
import uuid

BASE = "https://www.call2all.co.il/ym/api/"
KEY = os.environ.get("YEMOT_API_KEY", "").strip()
TZ_TOKEN = os.environ.get("YEMOT_TZINTUK_TOKEN", "").strip()
ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
ALERT = os.path.join(ROOT, "alert")
TEMP_TTS = "ivr2:/M0000.tts"
TEMP_WAV = "ivr2:/M0000.wav"

# --- קול מוכן (כמו voice.go / style.go בגשר) ---
GEMINI_KEYS = [k for k in (os.environ.get(n, "").strip() for n in
               ["GEMINI_API_KEY", "GEMINI_API_KEY_2", "GEMINI_API_KEY_3", "GEMINI_API_KEY_4", "GEMINI_API_KEY_5"]) if k]
SPEECH_MODELS = ["gemini-3.8-flash-tts", "gemini-3.8-flash-lite-tts", "gemini-3.1-flash-tts-preview", "gemini-2.5-flash-preview-tts"]
STYLE_MODELS = {"gemini-3.8-flash-tts", "gemini-3.8-flash-lite-tts", "gemini-3.1-flash-tts-preview"}
SPEECH_VOICE = os.environ.get("ALERT_VOICE", "Charon").strip() or "Charon"
# הנחיית הסגנון למבזק — דרמטי, דחוף, בניגוד לקריין הרגיל של הגשר (style.go).
SPEECH_STYLE = os.environ.get("ALERT_STYLE", "").strip() or (
    "Native Israeli Hebrew speaker. Urgent BREAKING NEWS alert on the radio: dramatic, tense, gripping and "
    "energetic, fast pace, strong emphasis on the key words, rising urgency. Read every word exactly as written."
)
STYLE_ENDPOINT = "https://generativelanguage.googleapis.com/v1beta/interactions"
STYLE_REVISION = "2026-05-20"
GEN_ENDPOINT = "https://generativelanguage.googleapis.com/v1beta/models/%s:generateContent"


def call(method, token=None, **form):
    """token=None: מפתח ה-API בכותרת. אחרת (מספר-מערכת:סיסמה) — בפרמטר token."""
    if token:
        form["token"] = token
    data = urllib.parse.urlencode(form).encode()
    req = urllib.request.Request(BASE + method, data=data, method="POST")
    req.add_header("Content-Type", "application/x-www-form-urlencoded; charset=utf-8")
    if not token:
        req.add_header("Authorization", KEY)
    return _do(req)


def _do(req, timeout=120):
    try:
        with urllib.request.urlopen(req, timeout=timeout) as r:
            body = r.read().decode("utf-8", "replace")
    except urllib.error.HTTPError as e:
        body = e.read().decode("utf-8", "replace") or f"HTTP {e.code}"
    except Exception as e:  # noqa: BLE001
        return None, f"שגיאת רשת: {e}"
    try:
        return json.loads(body), body
    except ValueError:
        return None, body


def upload_file(path, filename, data, convert=True):
    """UploadFile (multipart) — קובץ קול; convertAudio=1: ימות המשיח ממירים לפורמט טלפון."""
    boundary = "----alert" + uuid.uuid4().hex
    parts = []

    def field(name, value):
        parts.append(f"--{boundary}\r\nContent-Disposition: form-data; name=\"{name}\"\r\n\r\n{value}\r\n".encode())

    field("path", path)
    if convert:
        field("convertAudio", "1")
    parts.append((f"--{boundary}\r\nContent-Disposition: form-data; name=\"file\"; filename=\"{filename}\"\r\n"
                  "Content-Type: audio/wav\r\n\r\n").encode() + data + b"\r\n")
    parts.append(f"--{boundary}--\r\n".encode())
    body = b"".join(parts)
    req = urllib.request.Request(BASE + "UploadFile", data=body, method="POST")
    req.add_header("Content-Type", f"multipart/form-data; boundary={boundary}")
    req.add_header("Authorization", KEY)
    return _do(req, timeout=600)


def read(name, default=""):
    p = os.path.join(ALERT, name)
    if not os.path.exists(p):
        return default
    with open(p, encoding="utf-8") as f:
        return f.read().strip() or default


def mask(s):
    """מסתיר מספרי טלפון, סיסמאות ומפתחות בדוח."""
    s = re.sub(r"0\d{1,2}-?\d{7}", lambda m: "***" + m.group(0)[-3:], s)
    for secret in [TZ_TOKEN, KEY, *GEMINI_KEYS]:
        if secret:
            s = s.replace(secret, "***")
    return s


def as_wav(data, mime):
    """PCM גולמי מ-Gemini → WAV (mono, 16bit). אם כבר WAV — כמו שהוא."""
    if data[:4] == b"RIFF" and data[8:12] == b"WAVE":
        return data
    rate = 24000
    m = re.search(r"rate=(\d+)", mime or "", re.I)
    if m:
        rate = int(m.group(1))
    hdr = b"RIFF" + struct.pack("<I", 36 + len(data)) + b"WAVEfmt " + struct.pack("<IHHIIHH", 16, 1, 1, rate, rate * 2, 2, 16)
    return hdr + b"data" + struct.pack("<I", len(data)) + data


def _gpost(url, key, body, hdr=None):
    req = urllib.request.Request(url, data=json.dumps(body).encode(), method="POST")
    req.add_header("Content-Type", "application/json")
    req.add_header("x-goog-api-key", key)
    for k, v in (hdr or {}).items():
        req.add_header(k, v)
    try:
        with urllib.request.urlopen(req, timeout=300) as r:
            return r.status, r.read()
    except urllib.error.HTTPError as e:
        return e.code, e.read()
    except Exception as e:  # noqa: BLE001
        return 0, str(e).encode()


def _find_audio(obj):
    """מוצא בלוק קול בתשובה (steps[].content[] / outputs / output_audio / inlineData)."""
    if isinstance(obj, dict):
        if obj.get("type") == "audio" and obj.get("data"):
            return obj["data"], obj.get("mime_type", "")
        if "inlineData" in obj and obj["inlineData"].get("data"):
            return obj["inlineData"]["data"], obj["inlineData"].get("mimeType", "")
        for v in obj.values():
            r = _find_audio(v)
            if r:
                return r
    elif isinstance(obj, list):
        for v in reversed(obj):
            r = _find_audio(v)
            if r:
                return r
    return None


def synthesize(text, notes):
    """טקסט → WAV בקול דרמטי. מנסה מפתחות ומודלים לפי הסדר; None אם הכל נכשל."""
    for key in GEMINI_KEYS:
        for model in SPEECH_MODELS:
            tries = []
            if model in STYLE_MODELS:
                tries.append(("style", STYLE_ENDPOINT, {
                    "model": model,
                    "input": [{"type": "user_input", "content": [{"type": "text", "text": text,
                               "annotations": [{"type": "speech_metadata", "style": SPEECH_STYLE}]}]}],
                    "response_format": {"type": "audio"},
                    "generation_config": {"speech_config": [{"voice": SPEECH_VOICE}]},
                }, {"Api-Revision": STYLE_REVISION}))
            tries.append(("plain", GEN_ENDPOINT % model, {
                "contents": [{"parts": [{"text": text}]}],
                "generationConfig": {"responseModalities": ["AUDIO"], "speechConfig": {
                    "languageCode": "he-IL",
                    "voiceConfig": {"prebuiltVoiceConfig": {"voiceName": SPEECH_VOICE}}}},
            }, None))
            for how, url, body, hdr in tries:
                status, raw = _gpost(url, key, body, hdr)
                if status != 200:
                    notes.append(f"- קול: {model} ({how}) → {status} {mask(raw[:160].decode('utf-8', 'replace'))}")
                    if status == 429:
                        break  # מכסה במודל הזה — למודל הבא
                    continue
                try:
                    found = _find_audio(json.loads(raw))
                except ValueError:
                    found = None
                if not found:
                    notes.append(f"- קול: {model} ({how}) → בלי קול בתשובה")
                    continue
                data, mime = found
                notes.append(f"- קול: {model} ({how}, {SPEECH_VOICE}) ✓")
                return as_wav(base64.b64decode(data), mime)
    return None


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
        phones = set(re.findall(r"0\d{8,9}", json.dumps(r, ensure_ascii=False)))
        if phones:
            found = len(phones)
            break
    return found, lines


def ok(r):
    return bool(r) and r.get("responseStatus") == "OK"


def publish(text, report):
    """מעלה את ההודעה הזמנית: קול מוכן (M0000.wav) ואם אין — M0000.tts."""
    notes = []
    wav = synthesize(text, notes) if GEMINI_KEYS else None
    if wav:
        r, body = upload_file(TEMP_WAV, "M0000.wav", wav)
        if ok(r):
            call("FileAction", action="delete", what=TEMP_TTS)  # שלא יושמע פעמיים
            report.append("✅ ההודעה הזמנית הועלתה בקול מוכן (M0000.wav)")
            report.extend(notes)
            return True
        notes.append("- העלאת הקול נכשלה: " + mask(body[:200]))
    r, body = call("UploadTextFile", what=TEMP_TTS, contents=text)
    if ok(r):
        call("FileAction", action="delete", what=TEMP_WAV)
        report.append("✅ ההודעה הזמנית הועלתה בקול הממוחשב (M0000.tts)" + (" — הקול המוכן לא הצליח" if GEMINI_KEYS else ""))
        report.extend(notes)
        return True
    report.append("⚠️ העלאה נכשלה: " + mask(body[:300]))
    report.extend(notes)
    return False


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
        r1, b1 = call("FileAction", action="delete", what=TEMP_WAV)
        r2, b2 = call("FileAction", action="delete", what=TEMP_TTS)
        report.append("## מחיקת ההודעה הזמנית")
        report.append("✅ נמחקה" if ok(r1) or ok(r2) else "⚠️ נכשל: " + mask((b1 or "")[:200]) + " / " + mask((b2 or "")[:200]))
    else:
        silent = first.startswith("#בלי-צינתוק")
        lines = msg.splitlines()[1:] if silent else msg.splitlines()
        text = " ".join(l.strip() for l in lines if l.strip())
        report.append("## מבזק")
        report.append(f"> {text}")
        if not publish(text, report):
            sys.exit(1)
        if silent:
            report.append("צינתוק: לא נשלח (#בלי-צינתוק)")
        else:
            r, body = call("RunTzintuk", token=TZ_TOKEN or None, phones=f"tzl:{list_id}")
            report.append((f"✅ צינתוק נשלח לרשימה {list_id} (עם {how}) — " if ok(r) else f"⚠️ צינתוק נכשל (עם {how}): ")
                          + mask(body[:300]))

    out = "\n".join(report)
    print(out)
    with open(os.path.join(ALERT, "result.md"), "w", encoding="utf-8") as f:
        f.write(out + "\n")


if __name__ == "__main__":
    main()
