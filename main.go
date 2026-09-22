// yemot-news-bridge
//
// תוכנית קטנה שרצה כ-Cron Job (ב-GitHub Actions, כל 5 דקות): שולפת את
// ההודעות האחרונות מה-API של Telegram Popup ("ערוץ חי"), ובונה מהן טקסט
// אחד שבו כל הודעה נפרדת: קודם מי פרסם (שם הערוץ), אחר כך באיזו שעה,
// ואז מה נאמר. כל הודעה נשלחת לקובץ TTS נפרד בשלוחה בימות המשיח
// (001.tts, 002.tts, ...), כי קובץ TTS אחד מוגבל לכ-1,300 תווים.
package main

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	_ "time/tzdata" // שעון ישראל עובד גם אם בשרת אין קבצי אזורי זמן
	"unicode"
)

// FeedItem משקף פריט בודד שחוזר מ-/api/messages של Telegram Popup.
// רק השדות שבהם אנחנו משתמשים כאן.
type FeedItem struct {
	ID      int    `json:"id"`
	Channel string `json:"channel"`
	TS      int64  `json:"ts"`
	Text    string `json:"text"`
}

type FeedResponse struct {
	Items []FeedItem `json:"items"`
}

// שמות קריאים לערוצים — גיבוי למקרה שהשרת לא מחזיר שם ערוץ.
// אפשר להוסיף כאן ערוצים נוספים: "שם_משתמש": "שם בעברית".
var knownNames = map[string]string{
	"elisha_yered": "אלישע ירד",
}

// כתובת ה-API של ימות המשיח.
var yemotBase = "https://www.call2all.co.il/ym/api/"

// הודעת הפתיחה בתפריט הראשי של הקו.
const defaultWelcome = "ברוכים הבאים לקו עדכוני ארץ ישראל. לעדכונים שוטפים, הקישו 1."

func main() {
	// --- קריאת הגדרות מתוך משתני סביבה ---
	cfg := config{
		feedURL: envOr("TGPOPUP_URL", "https://telegram-popup.onrender.com/api/messages"),
		feedKey: strings.TrimSpace(os.Getenv("TGPOPUP_KEY")),
		apiKey:  cleanKey(os.Getenv("YEMOT_API_KEY")),
		ext:     envOr("YEMOT_EXT", "1"),      // מספר השלוחה
		maxMsgs: envInt("YEMOT_MAX_MSGS", 10), // כמה הודעות אחרונות להקריא (קובץ לכל הודעה)
		newest:  envOr("YEMOT_ORDER", "oldest") == "newest",
	}
	if cfg.feedKey == "" {
		log.Fatal("חסר משתנה סביבה TGPOPUP_KEY")
	}
	if cfg.apiKey == "" {
		log.Fatal("חסר משתנה סביבה YEMOT_API_KEY (המפתח הקבוע מעמוד \"מפתחות גישה\" בימות המשיח)")
	}
	if cfg.maxMsgs > 99 {
		cfg.maxMsgs = 99
	}
	var err error
	cfg.loc, err = time.LoadLocation("Asia/Jerusalem")
	if err != nil {
		cfg.loc = time.FixedZone("IL", 3*3600)
	}
	cfg.client = &http.Client{Timeout: 30 * time.Second}
	// שרת Render בחבילה החינמית נרדם כשאין שימוש, וההתעוררות לוקחת 30–60 שניות.
	cfg.feedClient = &http.Client{Timeout: 90 * time.Second}

	// הודעת פתיחה בתפריט הראשי (קובץ M1000.tts בשלוחה הראשית).
	// YEMOT_WELCOME=off מכבה.
	if welcome := envOr("YEMOT_WELCOME", defaultWelcome); welcome != "off" {
		if err := pushToYemot(cfg.client, cfg.apiKey, "", "M1000.tts", welcome); err != nil {
			log.Printf("הערה: עדכון הודעת הפתיחה נכשל: %v", err)
		} else {
			log.Printf("הודעת פתיחה (M1000.tts בשלוחה הראשית): %s", welcome)
		}
		checkRootIsMenu(cfg.client, cfg.apiKey)
	}

	// מצב לולאה: RUN_MINUTES > 0 — התוכנית נשארת פתוחה ובודקת כל
	// INTERVAL_SECONDS שניות, ומעלה לימות המשיח רק קבצים שהשתנו.
	// בלי RUN_MINUTES — ריצה אחת וסיום (כמו קודם).
	runFor := time.Duration(envInt("RUN_MINUTES", 0)) * time.Minute
	interval := time.Duration(envInt("INTERVAL_SECONDS", 60)) * time.Second

	st := &state{}
	if runFor == 0 {
		if err := syncOnce(&cfg, st); err != nil {
			log.Fatalf("%v", err)
		}
		return
	}

	deadline := time.Now().Add(runFor)
	log.Printf("מצב לולאה: בדיקה כל %v, עד %s (שעון ישראל).", interval, deadline.In(cfg.loc).Format("15:04"))
	for {
		if err := syncOnce(&cfg, st); err != nil {
			log.Printf("שגיאה בסבב (ממשיך לסבב הבא): %v", err)
		}
		if time.Now().Add(interval).After(deadline) {
			log.Println("זמן הריצה הסתיים — ההפעלה הבאה של ה-Workflow תמשיך מכאן.")
			return
		}
		time.Sleep(interval)
	}
}

type config struct {
	feedURL, feedKey, apiKey, ext string
	maxMsgs                       int
	newest                        bool
	loc                           *time.Location
	client, feedClient            *http.Client
}

// state — מה נמצא כרגע בשלוחה (לפי מה שהעלינו בהפעלה הזו), כדי להעלות
// רק קבצים שהשתנו.
type state struct {
	uploaded []string          // תוכן כל קובץ לפי הסדר: [0]=001.tts ...
	known    bool              // האם כבר סונכרנו פעם אחת בהפעלה הזו
	titles   map[string]string // שמות ערוצים אחרונים שנשלפו
}

// syncOnce: שליפה מה-feed, בניית הטקסטים, והעלאת מה שהשתנה בלבד.
func syncOnce(cfg *config, st *state) error {
	var items []FeedItem
	var err error
	for attempt := 1; attempt <= 3; attempt++ {
		items, err = fetchFeed(cfg.feedClient, cfg.feedURL, cfg.feedKey)
		if err == nil {
			break
		}
		log.Printf("ניסיון %d לשליפה מה-feed נכשל: %v", attempt, err)
		if attempt < 3 {
			time.Sleep(time.Duration(attempt) * 10 * time.Second)
		}
	}
	if err != nil {
		return fmt.Errorf("שגיאה בשליפת ההודעות: %w", err)
	}

	// שמות הערוצים. כישלון לא עוצר — נשארים עם הקודמים / השמות הקבועים.
	if t, err := fetchTitles(cfg.client, cfg.feedURL, cfg.feedKey); err == nil {
		st.titles = t
	} else if st.titles == nil {
		log.Printf("לא הצלחתי לשלוף שמות ערוצים (משתמש בשמות הקבועים): %v", err)
	}

	parts := buildScript(items, st.titles, cfg.loc, cfg.maxMsgs, cfg.newest)
	if len(parts) == 0 {
		log.Println("אין הודעות טקסט זמינות — לא נשלח כלום.")
		return nil
	}

	// כל הודעה לקובץ משלה: 001.tts, 002.tts, ... — רק מה שהשתנה.
	changed := 0
	for i, part := range parts {
		if st.known && i < len(st.uploaded) && st.uploaded[i] == part {
			continue
		}
		file := fmt.Sprintf("%03d.tts", i+1)
		if err := pushToYemot(cfg.client, cfg.apiKey, cfg.ext, file, part); err != nil {
			st.known = false // לא בטוחים מה נמצא בשלוחה — בסבב הבא מעלים הכול
			return fmt.Errorf("שגיאה בשליחה לימות המשיח (%s): %w", file, err)
		}
		log.Printf("%s (%d תווים): %.80s", file, len([]rune(part)), part)
		changed++
	}

	// קבצים עודפים מריצה קודמת (כשיש עכשיו פחות הודעות) — מוחקים,
	// כדי שלא יוקראו הודעות ישנות בסוף.
	if !st.known || len(parts) < len(st.uploaded) {
		var stale []string
		for i := len(parts) + 1; i <= cfg.maxMsgs; i++ {
			stale = append(stale, fmt.Sprintf("ivr2:/%s/%03d.tts", cfg.ext, i))
		}
		if len(stale) > 0 {
			if err := deleteYemotFiles(cfg.client, cfg.apiKey, stale); err != nil {
				log.Printf("הערה: מחיקת קבצים ישנים לא הצליחה (לא קריטי): %v", err)
			}
		}
	}

	st.uploaded = parts
	st.known = true
	if changed > 0 {
		log.Printf("עודכנו %d קבצים (סה\"כ %d הודעות בשלוחה).", changed, len(parts))
	}
	return nil
}

// buildScript בונה את טקסטי ההקראה — אחד לכל הודעה:
// "<שם הערוץ>, בשעה <שעה>. <תוכן ההודעה>".
func buildScript(items []FeedItem, titles map[string]string, loc *time.Location, maxMsgs int, newestFirst bool) []string {
	// ימות המשיח: קובץ TTS מוגבל לכ-1,300 תווים. משאירים מרווח ביטחון.
	const maxPerFile = 1000

	// רק הודעות עם טקסט, מהחדשה לישנה.
	var withText []FeedItem
	for _, it := range items {
		it.Text = cleanForSpeech(it.Text)
		if it.Text != "" {
			withText = append(withText, it)
		}
	}
	sort.SliceStable(withText, func(i, j int) bool { return withText[i].TS > withText[j].TS })
	if len(withText) > maxMsgs {
		withText = withText[:maxMsgs]
	}

	var parts []string
	for _, it := range withText {
		head := fmt.Sprintf("%s, %s. ", speakerName(it.Channel, titles), spokenTime(time.Unix(it.TS, 0).In(loc)))
		body := it.Text
		const cutNote = ". סוף ההודעה נחתך."
		room := maxPerFile - len([]rune(head)) - len([]rune(cutNote))
		if r := []rune(body); len(r) > room {
			body = cutAtWord(r[:room]) + cutNote
		}
		parts = append(parts, head+body)
	}

	// ברירת מחדל: לפי סדר השעות — מהמוקדמת למאוחרת.
	if !newestFirst {
		for i, j := 0, len(parts)-1; i < j; i, j = i+1, j-1 {
			parts[i], parts[j] = parts[j], parts[i]
		}
	}
	return parts
}

// spokenTime כותב שעה בצורה שמנוע ההקראה קורא טבעי ("בשעה 20 ו 5 דקות")
// במקום "20:05", שעלול להיקרא כסימנים.
func spokenTime(t time.Time) string {
	if t.Minute() == 0 {
		return fmt.Sprintf("בשעה %d בדיוק", t.Hour())
	}
	return fmt.Sprintf("בשעה %d ו %d דקות", t.Hour(), t.Minute())
}

// cutAtWord חותך בסוף מילה שלמה, כדי לא לקטוע באמצע מילה.
func cutAtWord(r []rune) string {
	s := string(r)
	if i := strings.LastIndex(s, " "); i > len(s)/2 {
		s = s[:i]
	}
	return strings.TrimSpace(s)
}

// speakerName מחזיר שם קריא למי שפרסם את ההודעה.
func speakerName(channel string, titles map[string]string) string {
	if t := cleanForSpeech(titles[channel]); t != "" {
		return t
	}
	if n, ok := knownNames[channel]; ok {
		return n
	}
	return strings.ReplaceAll(channel, "_", " ")
}

var (
	reURL    = regexp.MustCompile(`https?://\S+|t\.me/\S+|www\.\S+|@\w+`)
	reSpaces = regexp.MustCompile(`\s+`)
	reDots   = regexp.MustCompile(`([.!?,])[\s.!?,]*[.,]`)
)

// cleanForSpeech מכין טקסט להקראה: מסיר קישורים ותיוגים, אימוג'ים וסמלים
// שהמנוע מקריא כמילים (כוכבית, סולמית...), והופך ירידות שורה לנקודה —
// קובץ TTS צריך להיות רצף טקסט אחד פשוט.
func cleanForSpeech(s string) string {
	s = reURL.ReplaceAllString(s, " ")
	var b strings.Builder
	for _, r := range s {
		switch {
		case r == '\n' || r == '\r':
			b.WriteString(". ")
		case unicode.IsLetter(r), unicode.IsDigit(r):
			b.WriteRune(r)
		case unicode.IsSpace(r):
			b.WriteRune(' ')
		case strings.ContainsRune(".,!?:;()'\"-–—%₪/", r):
			if r == '–' || r == '—' {
				r = '-'
			}
			b.WriteRune(r)
		default:
			// אימוג'ים, * # _ | ~ ^ ועוד — מוחלפים ברווח.
			b.WriteRune(' ')
		}
	}
	s = reSpaces.ReplaceAllString(b.String(), " ")
	s = strings.ReplaceAll(s, " .", ".")
	s = reDots.ReplaceAllString(s, "$1")
	s = strings.Trim(s, " .,-")
	return s
}

// fetchFeed שולף את רשימת ההודעות מה-API של Telegram Popup.
func fetchFeed(client *http.Client, feedURL, key string) ([]FeedItem, error) {
	body, err := getJSON(client, feedURL, key)
	if err != nil {
		return nil, err
	}
	var parsed FeedResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, fmt.Errorf("JSON לא תקין מה-feed: %w", err)
	}
	return parsed.Items, nil
}

// fetchTitles שולף את שמות התצוגה של הערוצים (/api/channels באותו שרת).
func fetchTitles(client *http.Client, feedURL, key string) (map[string]string, error) {
	u, err := url.Parse(feedURL)
	if err != nil {
		return nil, err
	}
	u.Path = "/api/channels"
	body, err := getJSON(client, u.String(), key)
	if err != nil {
		return nil, err
	}
	var parsed struct {
		Channels []struct {
			Name  string `json:"name"`
			Title string `json:"title"`
		} `json:"channels"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, err
	}
	m := map[string]string{}
	for _, c := range parsed.Channels {
		// "@שם" פירושו שהשרת עוד לא יודע את השם האמיתי — מדלגים.
		if c.Title != "" && !strings.HasPrefix(c.Title, "@") {
			m[c.Name] = c.Title
		}
	}
	return m, nil
}

func getJSON(client *http.Client, rawURL, key string) ([]byte, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil, fmt.Errorf("כתובת לא תקינה: %w", err)
	}
	q := u.Query()
	q.Set("k", key)
	u.RawQuery = q.Encode()

	resp, err := client.Get(u.String())
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("סטטוס %d מ-%s: %.200s", resp.StatusCode, u.Path, string(body))
	}
	return body, nil
}

// pushToYemot מעלה טקסט לקובץ TTS בשלוחה נתונה, דרך ה-API הרשמי
// של ימות המשיח (www.call2all.co.il/ym/api/UploadTextFile), עם מפתח
// API קבוע שנוצר בעמוד "מפתחות גישה" (נשלח בכותרת Authorization).
func pushToYemot(client *http.Client, apiKey, ext, file, text string) error {
	base := yemotBase + "UploadTextFile"

	// שליחה ב-POST (טופס מקודד) במקום GET — כך טקסט ארוך בעברית לא נחתך
	// בגלל אורך הכתובת, והתוכן לא נחשף בלוגים של כתובות.
	form := url.Values{}
	what := "ivr2:/" + file // שלוחה ראשית
	if ext != "" {
		what = "ivr2:/" + ext + "/" + file
	}
	form.Set("what", what)
	form.Set("contents", text)

	req, err := http.NewRequest(http.MethodPost, base, strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded; charset=utf-8")
	req.Header.Set("Authorization", apiKey)

	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	var parsed struct {
		ResponseStatus string `json:"responseStatus"`
		Message        string `json:"message"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return fmt.Errorf("תגובה לא צפויה מימות המשיח: %s", strings.TrimSpace(string(body)))
	}
	if parsed.ResponseStatus != "OK" {
		return fmt.Errorf("שגיאה מימות המשיח (%s): %s", parsed.ResponseStatus, parsed.Message)
	}
	return nil
}

// cleanKey מנקה את המפתח מרווחים בקצוות, מירידות שורה (\r \n) ומתווי
// BOM/רווח בלתי נראים שנכנסים לפעמים כשמדביקים Secret ב-GitHub.
func cleanKey(k string) string {
	k = strings.TrimSpace(k)
	k = strings.NewReplacer("\r", "", "\n", "", "\xef\xbb\xbf", "", "\xe2\x80\x8b", "").Replace(k) // BOM, רווח ברוחב אפס
	return strings.TrimSpace(k)
}

// checkRootIsMenu בודק (רק לצורך הלוג) שהשלוחה הראשית מוגדרת כתפריט —
// אחרת קובץ M1000 לא יושמע. לא משנה שום הגדרה בעצמו.
func checkRootIsMenu(client *http.Client, apiKey string) {
	req, err := http.NewRequest(http.MethodGet, yemotBase+"GetTextFile?what="+url.QueryEscape("ivr2:/ext.ini"), nil)
	if err != nil {
		return
	}
	req.Header.Set("Authorization", apiKey)
	resp, err := client.Do(req)
	if err != nil {
		return
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	var parsed struct {
		ResponseStatus string `json:"responseStatus"`
		Contents       string `json:"contents"`
	}
	if json.Unmarshal(body, &parsed) != nil || parsed.ResponseStatus != "OK" {
		return // אין הרשאה לקריאה או שאין קובץ — לא קריטי
	}
	if !strings.Contains(strings.ReplaceAll(parsed.Contents, " ", ""), "type=menu") {
		log.Printf("אזהרה: השלוחה הראשית אינה מוגדרת type=menu, ולכן הודעת הפתיחה לא תושמע. ext.ini הנוכחי: %.200s", parsed.Contents)
	}
}

// deleteYemotFiles מוחק קבצים במערכת (FileAction?action=delete).
func deleteYemotFiles(client *http.Client, apiKey string, paths []string) error {
	form := url.Values{}
	form.Set("action", "delete")
	for i, p := range paths {
		form.Set(fmt.Sprintf("what%d", i), p)
	}
	req, err := http.NewRequest(http.MethodPost, yemotBase+"FileAction", strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded; charset=utf-8")
	req.Header.Set("Authorization", apiKey)
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	var parsed struct {
		ResponseStatus string `json:"responseStatus"`
		Message        string `json:"message"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return fmt.Errorf("תגובה לא צפויה: %.200s", strings.TrimSpace(string(body)))
	}
	if parsed.ResponseStatus != "OK" {
		return fmt.Errorf("%s: %s", parsed.ResponseStatus, parsed.Message)
	}
	return nil
}

func envOr(key, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return fallback
}

func envInt(key string, fallback int) int {
	if n, err := strconv.Atoi(strings.TrimSpace(os.Getenv(key))); err == nil && n > 0 {
		return n
	}
	return fallback
}
