#!/usr/bin/env python3
"""העברת שלוחה למספר טלפון אחר, עם קוד גישה — רץ ב-GitHub Actions (route-ext.yml).

קורא את routing/config.txt:
  state=on   — השלוחה מוגדרת כהעברה למספר שב-phone, נעולה בקוד גישה.
               kind=phone — העברה למספר טלפון רגיל (type=routing; עולה יחידות לפי דקות).
               kind=yemot — העברה למערכת אחרת של ימות המשיח (type=routing_yemot; בלי יחידות).
               kind=nitoviya — העברה במודול ניתוביה ליעד קבוע (type=nitoviya + nitoviya_dial_to;
                               בלי יחידות מאז 29.7.2025, ייתכנו פרסומות לפני השיחה;
                               רק ליעד רגיל — נייח או נייד).
               kind=1800 — מודול ההפניה למספרי 1800/1801 (type=routing_1800; בלי יחידות).
  state=off  — השלוחה חוזרת להפנות לתפריט הראשי.

קוד הגישה לא נכתב בריפו (הריפו ציבורי) ולא מודפס לעולם: הסקריפט מגריל קוד,
שומר אותו רק בתוך ext.ini במערכת ימות המשיח, ושולח אותו ב-SMS לבעל הקו
(SMS_TO, כמו בהתראת הפטריוטים). אם ה-SMS נכשל — ההגדרה הקודמת של השלוחה
מוחזרת, כדי שלא תישאר שלוחה נעולה בקוד שאף אחד לא יודע.

קוד חדש: מעלים את code_version ב-routing/config.txt (1 → 2) — יוגרל קוד חדש
ויישלח ב-SMS. שינוי המספר בלבד (phone) משאיר את הקוד הקיים.

קוד שבעל הקו בוחר בעצמו: הרצה ידנית של route-ext.yml (Run workflow) עם השדה
code. הקוד נקרא ישר מקובץ האירוע של ההרצה (לא ממשתנה סביבה), כדי שלא יודפס
בלוג, ולא נשלח SMS — בעל הקו יודע אותו.

השלוחה היא גם השלוחה של קו ג'מיני (gemini_line.py): כשקו ג'מיני פעיל בה
(type=api) הסקריפט לא נוגע בה. ההגדרה השמורה שלו (gemini-off.ini) לא נמחקת.
"""
import json
import os
import secrets
import sys
import urllib.error
import urllib.parse
import urllib.request

BASE = os.environ.get("YEMOT_API_BASE", "").strip() or "https://www.call2all.co.il/ym/api/"
KEY = os.environ.get("YEMOT_API_KEY", "").strip()
SMS_TOKEN = os.environ.get("YEMOT_SMS_TOKEN", "").strip()
SMS_TO = os.environ.get("SMS_TO", "").strip()
SMS_FROM = os.environ.get("SMS_FROM", "").strip()
ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
CONFIG = os.environ.get("ROUTE_CONFIG", "").strip() or os.path.join(ROOT, "routing", "config.txt")
OFF_INI = "type=go_to_folder\ngo_to_folder=/"
KINDS = {  # kind → (type של השלוחה, המפתח של מספר היעד)
    "phone": ("routing", "routing_to_phone"),
    "yemot": ("routing_yemot", "routing_yemot_number"),
    "nitoviya": ("nitoviya", "nitoviya_dial_to"),
    "1800": ("routing_1800", "routing_1800"),
}
ROUTE_TYPES = tuple(t for t, _ in KINDS.values())

_hide = [KEY, SMS_TOKEN, SMS_TO]  # ערכים שאסור שיופיעו בלוג (הקוד מתווסף כשהוא מוגרל)


def mask(s):
    for sec in _hide:
        if sec:
            s = s.replace(sec, "***")
    return s


def post(method, form, auth=True):
    req = urllib.request.Request(BASE + method, data=urllib.parse.urlencode(form).encode(), method="POST")
    req.add_header("Content-Type", "application/x-www-form-urlencoded; charset=utf-8")
    if auth:
        req.add_header("Authorization", KEY)
    try:
        with urllib.request.urlopen(req, timeout=60) as r:  # noqa: S310
            body = r.read().decode("utf-8", "replace")
    except urllib.error.HTTPError as e:
        body = e.read().decode("utf-8", "replace")
    except Exception as e:  # noqa: BLE001
        return None, f"שגיאת רשת ב-{method}: {mask(str(e))}"
    try:
        return json.loads(body), ""
    except ValueError:
        return None, f"תגובה לא צפויה מימות המשיח ב-{method}"


def call(method, **form):
    r, err = post(method, form)
    if r is None:
        sys.exit(err)
    return r


def read(path):
    """תוכן קובץ טקסט, או "" כשאינו קיים."""
    r = call("GetTextFile", what=path)
    if r.get("responseStatus") != "OK":
        return ""
    return (r.get("contents") or "").replace("\r\n", "\n").strip()


def write(path, text):
    """כותב קובץ ומוודא שנשמר. מחזיר "" כשהצליח, אחרת תיאור התקלה (בלי תוכן הקובץ)."""
    name = path.rsplit("/", 1)[-1]
    r, err = post("UploadTextFile", {"what": path, "contents": text})
    if r is None:
        return err
    if r.get("responseStatus") != "OK":
        return f"העלאה נכשלה ({name}): {mask(str(r.get('responseStatus')))}"
    if read(path) != text.strip():
        return f"הקובץ {name} לא נשמר כמו שנשלח."
    return ""


def values(ini):
    out = {}
    for line in ini.splitlines():
        line = line.strip()
        if line.startswith("#") or "=" not in line:
            continue
        k, v = line.split("=", 1)
        out[k.strip().lower()] = v.strip()
    return out


def send_sms(message):
    if not SMS_TO:
        return False, "אין SMS_TO (מספר יעד) ב-Secrets"
    if not (KEY or SMS_TOKEN):
        return False, "אין מפתח לשליחת SMS"
    form = {"phones": SMS_TO, "message": message}
    if SMS_FROM:
        form["from"] = SMS_FROM
    if SMS_TOKEN:
        form["token"] = SMS_TOKEN
    r, err = post("SendSms", form, auth=not SMS_TOKEN)
    if r is None:
        return False, err
    status = str(r.get("responseStatus", ""))
    if status.upper() != "OK":
        return False, mask(f"{status}: {r.get('message', '')}")
    return True, ""


def chosen_code():
    """קוד שבעל הקו בחר — רק בהרצה ידנית (workflow_dispatch, שדה code). "" כשאין."""
    path = os.environ.get("GITHUB_EVENT_PATH", "")
    if os.environ.get("GITHUB_EVENT_NAME") != "workflow_dispatch" or not path:
        return ""
    try:
        with open(path, encoding="utf-8") as f:
            inputs = json.load(f).get("inputs") or {}
    except (OSError, ValueError):
        return ""
    code = str(inputs.get("code") or "").strip()
    if code:
        _hide.append(code)
        print("::add-mask::" + code)  # GitHub מסתיר את הערך בכל הלוג מכאן והלאה
    return code


def new_code():
    """קוד גישה בן 4 ספרות (בלי 0 בהתחלה)."""
    return str(secrets.randbelow(9000) + 1000)


def main():
    if not KEY:
        sys.exit("חסר YEMOT_API_KEY")
    with open(CONFIG, encoding="utf-8") as f:
        cfg = values(f.read())
    ext = cfg.get("ext", "7").strip("/ ") or "7"
    want = cfg.get("state", "").lower()
    phone = cfg.get("phone", "").replace("-", "").replace(" ", "")
    version = cfg.get("code_version", "1")
    kind = cfg.get("kind", "phone").lower()
    if kind not in KINDS:
        sys.exit("routing/config.txt: kind צריך להיות phone, yemot, nitoviya או 1800")
    rtype, rkey = KINDS[kind]
    if want not in ("on", "off"):
        sys.exit("routing/config.txt: state צריך להיות on או off")
    if not ext.replace("/", "").isdigit():
        sys.exit("routing/config.txt: ext צריך להיות מספר שלוחה")
    if want == "on" and not (phone.isdigit() and 7 <= len(phone) <= 15):
        sys.exit("routing/config.txt: phone צריך להיות מספר טלפון (ספרות בלבד)")
    picked = chosen_code()
    if picked and not (picked.isdigit() and 4 <= len(picked) <= 10):
        sys.exit("קוד הגישה שנבחר צריך להיות 4 עד 10 ספרות — לא שיניתי כלום.")

    ini_path = f"ivr2:/{ext}/ext.ini"
    state_path = f"ivr2:/{ext}/routing-state.ini"
    cur = read(ini_path)
    cv = values(cur)
    cur_type = cv.get("type", "(אין)")
    print(f"שלוחה {ext}: מצב נוכחי type={cur_type}; מבוקש: {want}")
    if not cur:
        sys.exit(f"שלוחה {ext} לא קיימת או בלי הגדרות — לא נוגע בה.")
    if cur_type == "api":
        sys.exit(f"שלוחה {ext} פעילה כרגע כקו ג'מיני — לא נוגע בה. כבו אותו קודם (gemini/state.txt = off).")

    if want == "off":
        if cur_type not in ROUTE_TYPES:
            print("השלוחה לא מוגדרת כהעברה — אין מה לשנות.")
            return
        err = write(ini_path, OFF_INI)
        if err:
            sys.exit(err)
        print(f"שלוחה {ext}: ההעברה כבויה, השלוחה מפנה לתפריט הראשי.")
        return

    saved = values(read(state_path))
    old_code = cv.get("password", "") if cur_type in ROUTE_TYPES else ""
    if picked:
        keep_code = old_code == picked
    else:
        keep_code = bool(old_code) and old_code.isdigit() and saved.get("code_version") == version
    if keep_code and cur_type == rtype and cv.get(rkey) == phone:
        print(f"שלוחה {ext} כבר מעבירה למספר המבוקש עם קוד הגישה המבוקש — אין מה לשנות.")
        return
    code = picked or (old_code if keep_code else new_code())
    _hide.append(code)

    ini = "\n".join([
        "type=" + rtype,
        rkey + "=" + phone,
        "password=" + code,
        "password_always_required=yes",
        "password_error_goto=/",
    ])
    err = write(ini_path, ini)
    if err:
        sys.exit(err)
    print(f"שלוחה {ext} מוגדרת כהעברה ל-{phone} (type={rtype}), נעולה בקוד גישה.")

    if picked:
        print("קוד הגישה הוחלף לקוד שבעל הקו בחר — לא נשלח SMS.")
    elif keep_code:
        print("קוד הגישה נשאר כמו שהיה (רק יעד ההעברה השתנה) — לא נשלח SMS.")
    else:
        ok, why = send_sms(f"קו עדכוני ארץ ישראל: קוד הגישה לשלוחה {ext} הוא {code}")
        if not ok:
            print("שליחת ה-SMS עם הקוד נכשלה:", why, file=sys.stderr)
            back = write(ini_path, cur)
            if back:
                sys.exit("...וגם החזרת ההגדרה הקודמת נכשלה: " + back)
            sys.exit(f"שלוחה {ext} הוחזרה להגדרה הקודמת (הקוד לא הגיע לבעל הקו).")
        print("קוד הגישה נשלח ב-SMS לבעל הקו.")

    err = write(state_path, f"phone={phone}\ncode_version={version}")
    if err:
        print("הערה: קובץ המצב לא נשמר —", err, "(ההעברה עצמה פעילה).")


if __name__ == "__main__":
    main()
