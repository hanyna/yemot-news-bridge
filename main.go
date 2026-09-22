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

func main() {
	// --- קריאת הגדרות מתוך משתני סביבה ---
	feedURL := envOr("TGPOPUP_URL", "https://telegram-popup.onrender.com/api/messages")
	feedKey := strings.TrimSpace(os.Getenv("TGPOPUP_KEY"))
	if feedKey == "" {
		log.Fatal("חסר משתנה סביבה TGPOPUP_KEY")
	}

	yemotAPIKey := cleanKey(os.Getenv("YEMOT_API_KEY"))
	if yemotAPIKey == "" {
		log.Fatal("חסר משתנה סביבה YEMOT_API_KEY (המפתח הקבוע מעמוד \"מפתחות גישה\" בימות המשיח)")
	}
	yemotExt := envOr("YEMOT_EXT", "1")     // מספר השלוחה
	maxMsgs := envInt("YEMOT_MAX_MSGS", 10) // כמה הודעות אחרונות להקריא (קובץ לכל הודעה)
	if maxMsgs > 99 {
		maxMsgs = 99
	}
	newestFirst := envOr("YEMOT_ORDER", "oldest") == "newest"

	client := &http.Client{Timeout: 30 * time.Second}

	// שרת Render בחבילה החינמית נרדם כשאין שימוש, וההתעוררות לוקחת 30–60 שניות.
	// לכן ל-feed יש זמן המתנה ארוך יותר, ועד 3 ניסיונות.
	feedClient := &http.Client{Timeout: 90 * time.Second}
	var items []FeedItem
	var err error
	for attempt := 1; attempt <= 3; attempt++ {
		items, err = fetchFeed(feedClient, feedURL, feedKey)
		if err == nil {
			break
		}
		log.Printf("ניסיון %d לשליפה מה-feed נכשל: %v", attempt, err)
		if attempt < 3 {
			time.Sleep(time.Duration(attempt) * 10 * time.Second)
		}
	}
	if err != nil {
		log.Fatalf("שגיאה בשליפת ההודעות: %v", err)
	}

	// שמות הערוצים (השרת כבר ער בשלב הזה). כישלון כאן לא עוצר — יש גיבוי.
	titles, err := fetchTitles(client, feedURL, feedKey)
	if err != nil {
		log.Printf("לא הצלחתי לשלוף שמות ערוצים (משתמש בשמות הקבועים): %v", err)
	}

	loc, err := time.LoadLocation("Asia/Jerusalem")
	if err != nil {
		loc = time.FixedZone("IL", 3*3600)
	}

	parts := buildScript(items, titles, loc, maxMsgs, newestFirst)
	if len(parts) == 0 {
		log.Println("אין הודעות טקסט זמינות — לא נשלח כלום.")
		return
	}

	// כל הודעה לקובץ משלה: 001.tts, 002.tts, ...
	for i, part := range parts {
		file := fmt.Sprintf("%03d.tts", i+1)
		if err := pushToYemot(client, yemotAPIKey, yemotExt, file, part); err != nil {
			log.Fatalf("שגיאה בשליחה לימות המשיח (%s): %v", file, err)
		}
		log.Printf("%s (%d תווים): %.80s", file, len([]rune(part)), part)
	}

	// אם הפעם יש פחות הודעות מהמקסימום — מוחקים קבצים ישנים שנשארו מריצה
	// קודמת, כדי שלא יוקראו הודעות ישנות בסוף.
	var stale []string
	for i := len(parts) + 1; i <= maxMsgs; i++ {
		stale = append(stale, fmt.Sprintf("ivr2:/%s/%03d.tts", yemotExt, i))
	}
	if len(stale) > 0 {
		if err := deleteYemotFiles(client, yemotAPIKey, stale); err != nil {
			log.Printf("הערה: מחיקת קבצים ישנים לא הצליחה (לא קריטי): %v", err)
		}
	}

	log.Printf("נשלח בהצלחה: %d הודעות, כל אחת בקובץ נפרד.", len(parts))
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
	base := "https://www.call2all.co.il/ym/api/UploadTextFile"

	// שליחה ב-POST (טופס מקודד) במקום GET — כך טקסט ארוך בעברית לא נחתך
	// בגלל אורך הכתובת, והתוכן לא נחשף בלוגים של כתובות.
	form := url.Values{}
	form.Set("what", "ivr2:/"+ext+"/"+file)
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

// deleteYemotFiles מוחק קבצים במערכת (FileAction?action=delete).
func deleteYemotFiles(client *http.Client, apiKey string, paths []string) error {
	form := url.Values{}
	form.Set("action", "delete")
	for i, p := range paths {
		form.Set(fmt.Sprintf("what%d", i), p)
	}
	req, err := http.NewRequest(http.MethodPost, "https://www.call2all.co.il/ym/api/FileAction", strings.NewReader(form.Encode()))
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
