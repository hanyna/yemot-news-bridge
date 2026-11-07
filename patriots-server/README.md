# הפטריוטים המלא — בשרת ענן (בלי המחשב שלך)

גרסת שרת של הכלי שמוריד את "התוכנית המלאה" של הפטריוטים מיוטיוב ומעלה אותה
לשלוחה 3/3 במקום ההקלטה. רצה על שרת לינוקס חינמי ב-Oracle Cloud (Always Free —
חינם לתמיד, לא תקופת ניסיון), 24/7, בלי קשר למחשב בבית.

מה בחבילה:

| קובץ | תפקיד |
|---|---|
| `install.sh` | התקנה מלאה (ffmpeg, yt-dlp, התוכנה, טיימר יומי, הרצת בדיקה) |
| `update.sh` | עדכון גרסה (רק מחליף את התוכנה, שומר הגדרות) |
| `yemot-patriots-arm64` / `yemot-patriots-amd64` | התוכנה, לשני סוגי המעבדים של Oracle |
| `cookies.txt` | *(אתם מוסיפים)* עוגיות יוטיוב — ראו שלב ג' |

---

## שלב א' — פתיחת חשבון Oracle Cloud (פעם אחת, ~10 דק')

1. נכנסים ל-<https://www.oracle.com/cloud/free/> ולוחצים **Start for free**.
2. ממלאים: מדינה **Israel**, שם, אימייל → **Verify my email** → לוחצים על הקישור במייל.
3. סיסמה, שם לחשבון הענן (למשל `hanyna`), ו-**Home Region**: מומלץ
   **Israel Central (Jerusalem)**. (אם אין בו מקום פנוי למכונה חינמית — ראו הערה בשלב ב'.)
4. כתובת + כרטיס אשראי **לאימות בלבד** (נעשית תפיסה של ~1$ שמוחזרת; החשבון חינמי לא
   עובר לחיוב מעצמו — צריך ללחוץ בפירוש על "Upgrade" בשביל זה).
5. אימות SMS → **Start my free trial**. החשבון מוכן אחרי כמה דקות.

## שלב ב' — יצירת השרת (~5 דק')

1. בקונסול (<https://cloud.oracle.com>) → תפריט ☰ → **Compute** → **Instances** → **Create instance**.
2. **Name**: `patriots`.
3. **Image and shape** → **Edit**:
   - Image: **Canonical Ubuntu 24.04** (או 22.04).
   - Shape: **Ampere → VM.Standard.A1.Flex**, 1 OCPU, 6 GB — מסומן **Always Free-eligible**.
     אם מופיע "Out of capacity" — בוחרים במקום זה **AMD → VM.Standard.E2.1.Micro** (גם חינמי).
4. **Networking**: להשאיר "Create new virtual cloud network", ולוודא ש-**Assign a public IPv4 address** מסומן.
5. **Add SSH keys** → **Generate a key pair for me** → **Save private key** (נשמר קובץ בשם
   `ssh-key-2026-xx-xx.key` בהורדות — **לשמור אותו**, זה המפתח לשרת).
6. **Create**. אחרי כדקה המצב יהיה **RUNNING**. מעתיקים את **Public IP address**.

## שלב ג' — עוגיות יוטיוב (מומלץ מאוד; 3 דק')

יוטיוב חוסם לפעמים בקשות מכתובות של שרתי ענן. עם עוגיות של חשבון מחובר הוא כמעט לא חוסם.

1. בכרום במחשב מתקינים את התוסף **Get cookies.txt LOCALLY**.
2. נכנסים ל-<https://www.youtube.com> כשמחוברים לחשבון גוגל.
   **מומלץ חשבון גוגל משני/ייעודי** — העוגיות נותנות גישה לחשבון, ושומרים אותן על השרת.
3. לוחצים על התוסף → **Export** → נשמר `cookies.txt`. מעתיקים אותו לתיקיית החבילה הזו.

(אפשר לדלג ולהוסיף אחר-כך: `scp` של הקובץ ל-`/opt/yemot-patriots/cookies.txt` ואז `patriots`.)

## שלב ד' — העלאת הקבצים והתקנה (~3 דק')

בווינדוס: פותחים **CMD** (יש בו `ssh` ו-`scp` מובנים). מחליפים `IP` בכתובת מהשלב הקודם
ו-`KEY` בנתיב למפתח שהורד:

```bat
cd %USERPROFILE%\Downloads
scp -i KEY -r patriots-server ubuntu@IP:~/
ssh -i KEY ubuntu@IP
```

בפעם הראשונה ssh ישאל `Are you sure you want to continue connecting?` → כותבים `yes`.
עכשיו אתם **בתוך השרת**. מריצים:

```bash
cd ~/patriots-server
sudo bash install.sh
```

ההתקנה תבקש את **מפתח הקו** (המפתח הקבוע מעמוד "מפתחות גישה" בימות המשיח — מה שמדביקים
לא מוצג על המסך, זה בסדר), תתקין הכל, ותריץ בדיקה אחת מיד — תראו את הלוג המוכר
("הפטריוטים המלא — v13 ...", חיפוש, הורדה, "הוחלפו").

מרגע זה השרת מריץ לבד **כל יום ב-23:40 וב-06:30** (שעון ישראל), ומעדכן את yt-dlp
אוטומטית כל יום ב-22:30. הסטטוס מופיע בדשבורד "מצב הקו" כרגיל (הכלי כותב
`patriots-status.json` לשלוחה 3 → הגשר → הדשבורד).

## פקודות בשרת

| פקודה | מה עושה |
|---|---|
| `patriots` | הרצה עכשיו + הלוג |
| `patriots log` | הלוג של ההרצות האחרונות |
| `patriots live` | לוג חי (יציאה: Ctrl+C) |
| `patriots status` | מתי ההרצה הבאה |
| `sudo nano /opt/yemot-patriots/patriots.env` | שינוי הגדרות (שעות, ימים אחורה, עוגיות) |

להתחבר שוב לשרת בכל פעם: `ssh -i KEY ubuntu@IP`.

## עדכון גרסה

כשמגיע `עדכון-הפטריוטים-שרת-vN.zip`: פורסים, ואז מהמחשב:

```bat
scp -i KEY -r patriots-server-update ubuntu@IP:~/
ssh -i KEY ubuntu@IP "cd ~/patriots-server-update && sudo bash update.sh"
```

## אם משהו לא עובד

- **"Sign in to confirm you're not a bot"** בלוג → צריך/לרענן `cookies.txt` (שלב ג').
- **"אין עדיין podcast.txt"** → הגשר עוד לא יצר את הפרק של היום; ההרצה הבאה תטפל.
- **לא מצליחים להתחבר ב-ssh** → לבדוק שהמכונה RUNNING, שה-IP נכון, ושהמפתח הוא הקובץ
  שהורד ב-"Save private key". אם נטפרי חוסם SSH — לבקש פתיחה של פורט 22 לכתובת השרת.
- **Oracle "Out of host capacity"** בשלב ב' → לנסות שוב אחר-כך, או שם E2.1.Micro.
- **אל תמחקו את המכונה ואל תלחצו Upgrade** — ככה החשבון נשאר חינמי.
