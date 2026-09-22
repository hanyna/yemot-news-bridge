// yemot-news-bridge
//
// גשר בין Telegram Popup ("ערוץ חי") לבין קו טלפון בימות המשיח.
// רץ ב-GitHub Actions ובודק הודעות חדשות כל דקה. כל הודעה נשלחת לקובץ TTS
// נפרד (001.tts, 002.tts, ...), כי קובץ TTS אחד מוגבל לכ-1,300 תווים:
//
//	שלוחה 1      — כל הערוצים יחד (החדשה נשמעת ראשונה)
//	שלוחות 2..9  — שלוחה לכל ערוץ ("לעדכוני אלישע ירד הקישו 2")
//	תפריט ראשי   — הודעת פתיחה (M1000.tts) שמפרטת את השלוחות
package main

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"
	_ "time/tzdata" // שעון ישראל עובד גם אם בשרת אין קבצי אזורי זמן
)

// FeedItem משקף פריט בודד שחוזר מ-/api/messages של Telegram Popup.
type FeedItem struct {
	ID      int    `json:"id"`
	Channel string `json:"channel"`
	TS      int64  `json:"ts"`
	Text    string `json:"text"`
}

type Channel struct {
	Name  string `json:"name"`
	Title string `json:"title"`
}

// שמות קריאים לערוצים — גיבוי למקרה שהשרת לא מחזיר שם ערוץ.
var knownNames = map[string]string{
	"elisha_yered": "אלישע ירד",
}

const (
	welcomeHead = "ברוכים הבאים לקו עדכוני ארץ ישראל. לעדכונים שוטפים, הקישו 1."
	maxPerFile  = 1000 // ימות המשיח: קובץ TTS מוגבל לכ-1,300 תווים — משאירים מרווח
	firstChExt  = 2    // שלוחת הערוץ הראשון
	lastChExt   = 9    // שלוחת הערוץ האחרון האפשרי
	failLimit   = 10   // כמה סבבים כושלים ברצף עד שמכשילים את הריצה (= מייל מ-GitHub)
)

type config struct {
	feedURL, feedKey string
	ext              string // שלוחת "כל העדכונים"
	maxMsgs, perChan int
	newestFirst      bool
	channelExts      bool
	welcome          string // "" = אוטומטי, "off" = כבוי
	voice, rate      string
	loc              *time.Location
	y                *yemot
	client           *http.Client // ל-API של ערוץ חי
	feedClient       *http.Client // זמן המתנה ארוך — Render מתעורר לאט
}

type state struct {
	files    map[string][]string // לכל שלוחה: תוכן הקבצים שהועלו (001, 002, ...)
	known    map[string]bool     // האם הקבצים בשלוחה ידועים לנו (אחרי סנכרון מוצלח)
	channels []Channel           // רשימת הערוצים האחרונה
	chExt    map[string]string   // ערוץ → שלוחה שהוכנה עבורו
	blocked  map[string]bool     // שלוחות תפוסות ע"י הגדרה אחרת — לא נוגעים
	welcome  string              // הודעת הפתיחה שהועלתה
	voiceSet bool                // קול/מהירות עודכנו בשלוחה הראשית ובשלוחה 1
	failures int
}

func main() {
	cfg := config{
		feedURL:     envOr("TGPOPUP_URL", "https://telegram-popup.onrender.com/api/messages"),
		feedKey:     strings.TrimSpace(os.Getenv("TGPOPUP_KEY")),
		ext:         envOr("YEMOT_EXT", "1"),
		maxMsgs:     envInt("YEMOT_MAX_MSGS", 10),
		perChan:     envInt("YEMOT_PER_CHANNEL", 5),
		// ימות המשיח משמיע את הקבצים בשלוחה מהמספר הגבוה לנמוך. לכן ברירת
		// המחדל: 001 = הישנה, המספר הגבוה = החדשה — והמאזין שומע את החדשה ראשונה.
		// YEMOT_ORDER=newest הופך (001 = החדשה).
		newestFirst: envOr("YEMOT_ORDER", "oldest") == "newest",
		channelExts: envOr("CHANNEL_EXTS", "on") != "off",
		welcome:     strings.TrimSpace(os.Getenv("YEMOT_WELCOME")),
		voice:       strings.TrimSpace(os.Getenv("YEMOT_VOICE")),
		rate:        strings.TrimSpace(os.Getenv("YEMOT_RATE")),
		client:      &http.Client{Timeout: 30 * time.Second},
		feedClient:  &http.Client{Timeout: 90 * time.Second},
	}
	apiKey := cleanKey(os.Getenv("YEMOT_API_KEY"))
	if cfg.feedKey == "" {
		log.Fatal("חסר משתנה סביבה TGPOPUP_KEY")
	}
	if apiKey == "" {
		log.Fatal("חסר משתנה סביבה YEMOT_API_KEY (המפתח הקבוע מעמוד \"מפתחות גישה\" בימות המשיח)")
	}
	cfg.y = &yemot{client: &http.Client{Timeout: 30 * time.Second}, apiKey: apiKey}
	if cfg.maxMsgs > 99 {
		cfg.maxMsgs = 99
	}
	var err error
	if cfg.loc, err = time.LoadLocation("Asia/Jerusalem"); err != nil {
		cfg.loc = time.FixedZone("IL", 3*3600)
	}

	diagnoseRoot(cfg.y)

	st := &state{files: map[string][]string{}, known: map[string]bool{}, chExt: map[string]string{}, blocked: map[string]bool{}}

	// מצב לולאה: RUN_MINUTES > 0 — נשארים פתוחים ובודקים כל INTERVAL_SECONDS.
	// בלי RUN_MINUTES — סבב אחד וסיום.
	runFor := time.Duration(envInt("RUN_MINUTES", 0)) * time.Minute
	interval := time.Duration(envInt("INTERVAL_SECONDS", 60)) * time.Second
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
			st.failures++
			log.Printf("שגיאה בסבב (%d ברצף): %v", st.failures, err)
			if st.failures >= failLimit {
				// הכשלת הריצה → GitHub שולח מייל על ריצה שנכשלה.
				log.Fatalf("הגשר נכשל %d פעמים ברצף — עוצר כדי שתישלח התראה. שגיאה אחרונה: %v", st.failures, err)
			}
		} else {
			st.failures = 0
		}
		if time.Now().Add(interval).After(deadline) {
			log.Println("זמן הריצה הסתיים — ההפעלה הבאה של ה-Workflow תמשיך מכאן.")
			return
		}
		time.Sleep(interval)
	}
}

// syncOnce: סבב אחד — שליפה, בנייה, והעלאת מה שהשתנה בלבד.
func syncOnce(cfg *config, st *state) error {
	var items []FeedItem
	var err error
	for attempt := 1; attempt <= 3; attempt++ {
		if items, err = fetchFeed(cfg.feedClient, cfg.feedURL, cfg.feedKey); err == nil {
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
	if chs, err := fetchChannels(cfg.client, cfg.feedURL, cfg.feedKey); err == nil {
		st.channels = chs
	} else if st.channels == nil {
		log.Printf("לא הצלחתי לשלוף את רשימת הערוצים (ממשיך בלי שלוחות לפי ערוץ): %v", err)
	}
	titles := map[string]string{}
	for _, c := range st.channels {
		if c.Title != "" && !strings.HasPrefix(c.Title, "@") {
			titles[c.Name] = c.Title
		}
	}

	now := time.Now().In(cfg.loc)
	items = prepare(items)

	// קול ומהירות — פעם אחת בכל הפעלה, בשלוחה הראשית ובשלוחת כל העדכונים.
	if !st.voiceSet && (cfg.voice != "" || cfg.rate != "") {
		st.voiceSet = true
		for _, ext := range []string{"", cfg.ext} {
			if err := applyVoice(cfg, ext); err != nil {
				log.Printf("הערה: עדכון קול/מהירות בשלוחה %q נכשל: %v", ext, err)
			}
		}
	}

	// שלוחה 1: כל הערוצים.
	all := buildParts(items, titles, cfg.loc, now, cfg.maxMsgs, cfg.newestFirst, true)
	if len(all) == 0 {
		all = []string{"אין כרגע עדכונים."}
	}
	if err := syncExt(cfg, st, cfg.ext, all, cfg.maxMsgs); err != nil {
		return err
	}

	// שלוחה לכל ערוץ.
	var menu []string
	if cfg.channelExts {
		for i, ch := range st.channels {
			extNum := firstChExt + i
			if extNum > lastChExt {
				break
			}
			ext := strconv.Itoa(extNum)
			if ext == cfg.ext || !ensureChannelExt(cfg, st, ch.Name, ext) {
				continue
			}
			name := speakerName(ch.Name, titles)
			var mine []FeedItem
			for _, it := range items {
				if it.Channel == ch.Name {
					mine = append(mine, it)
				}
			}
			parts := buildParts(mine, titles, cfg.loc, now, cfg.perChan, cfg.newestFirst, false)
			if len(parts) == 0 {
				parts = []string{"אין כרגע עדכונים חדשים מ" + name + "."}
			} else {
				// הכותרת נכנסת להודעה שנשמעת ראשונה — החדשה ביותר.
				first := len(parts) - 1
				if cfg.newestFirst {
					first = 0
				}
				parts[first] = "עדכוני " + name + ". " + parts[first]
			}
			if err := syncExt(cfg, st, ext, parts, cfg.perChan); err != nil {
				log.Printf("הערה: עדכון שלוחה %s (%s) נכשל: %v", ext, name, err)
				continue
			}
			menu = append(menu, fmt.Sprintf("לעדכוני %s הקישו %s.", name, ext))
		}
	}

	// הודעת פתיחה בתפריט הראשי — רק כשהשתנתה.
	if cfg.welcome != "off" {
		w := cfg.welcome
		if w == "" {
			w = strings.Join(append([]string{welcomeHead}, menu...), " ")
		}
		if r := []rune(w); len(r) > maxPerFile {
			w = cutAtWord(r[:maxPerFile])
		}
		if w != st.welcome {
			if err := cfg.y.upload("", "M1000.tts", w); err != nil {
				log.Printf("הערה: עדכון הודעת הפתיחה נכשל: %v", err)
			} else {
				st.welcome = w
				log.Printf("הודעת פתיחה: %s", w)
				checkRootIsMenu(cfg.y)
			}
		}
	}
	return nil
}

// prepare: ניקוי טקסט להקראה, השמטת הודעות בלי טקסט, וסינון כפילויות.
func prepare(items []FeedItem) []FeedItem {
	var out []FeedItem
	for _, it := range items {
		it.Text = cleanForSpeech(it.Text)
		if it.Text != "" {
			out = append(out, it)
		}
	}
	return dedupe(out)
}

// buildParts בונה את טקסטי ההקראה — אחד לכל הודעה:
// "<שם הערוץ>, <מתי>. <תוכן ההודעה>" (withName=false: בלי שם הערוץ).
func buildParts(items []FeedItem, titles map[string]string, loc *time.Location, now time.Time, max int, newestFirst, withName bool) []string {
	sorted := append([]FeedItem(nil), items...)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].TS > sorted[j].TS })
	if len(sorted) > max {
		sorted = sorted[:max]
	}
	var parts []string
	for _, it := range sorted {
		when := spokenWhen(time.Unix(it.TS, 0).In(loc), now)
		head := when + ". "
		if withName {
			head = speakerName(it.Channel, titles) + ", " + head
		}
		body := it.Text
		const cutNote = ". סוף ההודעה נחתך."
		room := maxPerFile - len([]rune(head)) - len([]rune(cutNote))
		if r := []rune(body); len(r) > room {
			body = cutAtWord(r[:room]) + cutNote
		}
		parts = append(parts, head+body)
	}
	if !newestFirst {
		for i, j := 0, len(parts)-1; i < j; i, j = i+1, j-1 {
			parts[i], parts[j] = parts[j], parts[i]
		}
	}
	return parts
}

// syncExt מעלה לשלוחה רק קבצים שהשתנו, ומוחק קבצים עודפים מריצה קודמת.
func syncExt(cfg *config, st *state, ext string, parts []string, maxFiles int) error {
	prev, known := st.files[ext], st.known[ext]
	changed := 0
	for i, part := range parts {
		if known && i < len(prev) && prev[i] == part {
			continue
		}
		file := fmt.Sprintf("%03d.tts", i+1)
		if err := cfg.y.upload(ext, file, part); err != nil {
			st.known[ext] = false // לא בטוחים מה יש בשלוחה — בסבב הבא מעלים הכול
			return fmt.Errorf("שליחה לשלוחה %s (%s): %w", ext, file, err)
		}
		log.Printf("שלוחה %s / %s (%d תווים): %.80s", ext, file, len([]rune(part)), part)
		changed++
	}
	if !known || len(parts) < len(prev) {
		var stale []string
		for i := len(parts) + 1; i <= maxFiles; i++ {
			stale = append(stale, ivrPath(ext, fmt.Sprintf("%03d.tts", i)))
		}
		if err := cfg.y.remove(stale); err != nil {
			log.Printf("הערה: מחיקת קבצים ישנים בשלוחה %s לא הצליחה (לא קריטי): %v", ext, err)
		}
	}
	st.files[ext] = parts
	st.known[ext] = true
	if changed > 0 {
		log.Printf("שלוחה %s: עודכנו %d קבצים.", ext, changed)
	}
	return nil
}

// ensureChannelExt מכין שלוחת השמעה לערוץ. יוצר אותה רק אם היא לא קיימת,
// או אם היא כבר שלוחה שהגשר יצר (type=playfile בלבד). שלוחה עם הגדרות
// אחרות — לא נוגעים בה ולא מפרסמים אותה בתפריט.
func ensureChannelExt(cfg *config, st *state, channel, ext string) bool {
	if st.chExt[channel] == ext {
		return true
	}
	if st.blocked[ext] {
		return false
	}
	ini, exists, err := cfg.y.read(ext, "ext.ini")
	if err != nil {
		st.blocked[ext] = true
		log.Printf("הערה: לא הצלחתי לבדוק את שלוחה %s, ולכן לא יוצר בה שלוחת ערוץ (אם המפתח לא מורשה ל-GetTextFile — צריך להוסיף לו הרשאה): %v", ext, err)
		return false
	}
	if exists && !isBridgeIni(ini) {
		st.blocked[ext] = true
		log.Printf("אזהרה: שלוחה %s כבר קיימת עם הגדרות אחרות — לא נוגע בה. ext.ini: %.150s", ext, ini)
		return false
	}
	want, _ := setIniValues("type=playfile", [][2]string{{"voice", cfg.voice}, {"rate", cfg.rate}})
	if !exists || strings.TrimSpace(ini) != want {
		if err := cfg.y.upload(ext, "ext.ini", want); err != nil {
			log.Printf("הערה: יצירת שלוחה %s נכשלה: %v", ext, err)
			return false
		}
		log.Printf("שלוחה %s הוגדרה לערוץ %s.", ext, channel)
	}
	st.chExt[channel] = ext
	return true
}

// isBridgeIni: ext.ini שנראה כמו מה שהגשר יוצר — type=playfile ואולי voice/rate.
func isBridgeIni(ini string) bool {
	sawType := false
	for _, l := range strings.Split(strings.ReplaceAll(ini, "\r\n", "\n"), "\n") {
		t := strings.TrimSpace(l)
		switch {
		case t == "":
		case t == "type=playfile":
			sawType = true
		case strings.HasPrefix(t, "voice="), strings.HasPrefix(t, "rate="):
		default:
			return false
		}
	}
	return sawType
}

// applyVoice מעדכן voice/rate בקובץ ext.ini קיים, בלי לגעת בשאר ההגדרות.
func applyVoice(cfg *config, ext string) error {
	ini, exists, err := cfg.y.read(ext, "ext.ini")
	if err != nil {
		return err
	}
	if !exists {
		return fmt.Errorf("אין ext.ini בשלוחה — לא יוצר אחד חדש")
	}
	updated, changed := setIniValues(ini, [][2]string{{"voice", cfg.voice}, {"rate", cfg.rate}})
	if !changed {
		return nil
	}
	if err := cfg.y.upload(ext, "ext.ini", updated); err != nil {
		return err
	}
	log.Printf("קול/מהירות עודכנו בשלוחה %q.", ext)
	return nil
}

// checkRootIsMenu בודק (רק לצורך הלוג) שהשלוחה הראשית מוגדרת כתפריט —
// אחרת הודעת הפתיחה והמקשים לא יעבדו. לא משנה שום הגדרה בעצמו.
func checkRootIsMenu(y *yemot) {
	ini, exists, err := y.read("", "ext.ini")
	if err != nil || !exists {
		return
	}
	if iniValue(ini, "type") != "menu" {
		log.Printf("אזהרה: השלוחה הראשית אינה מוגדרת type=menu, ולכן הודעת הפתיחה לא תושמע. ext.ini: %.200s", ini)
	}
}

// diagnoseRoot רושם בלוג מה יש בשלוחה הראשית — כדי לאבחן בעיות בתפריט.
func diagnoseRoot(y *yemot) {
	if names, err := y.listDir(""); err != nil {
		log.Printf("אבחון: לא הצלחתי לקרוא את רשימת הקבצים בשלוחה הראשית: %v", err)
	} else {
		log.Printf("אבחון: קבצים בשלוחה הראשית: %s", strings.Join(names, ", "))
		for _, n := range names {
			l := strings.ToLower(n)
			if strings.HasPrefix(l, "m1000.") && l != "m1000.tts" {
				log.Printf("אבחון: יש בשלוחה הראשית קובץ %s — הוא קודם להקראה של M1000.tts. צריך למחוק אותו כדי שהודעת הפתיחה החדשה תושמע.", n)
			}
		}
	}
	ini, exists, err := y.read("", "ext.ini")
	switch {
	case err != nil:
		log.Printf("אבחון: לא הצלחתי לקרוא את ext.ini של השלוחה הראשית: %v", err)
	case !exists:
		log.Println("אבחון: אין ext.ini בשלוחה הראשית.")
	default:
		log.Printf("אבחון: ext.ini של השלוחה הראשית: %s", strings.ReplaceAll(strings.TrimSpace(ini), "\n", " | "))
		if iniValue(ini, "type") != "menu" {
			log.Println("אבחון: השלוחה הראשית אינה type=menu — לכן הודעת הפתיחה (M1000) לא מושמעת.")
		}
		if iniValue(ini, "say_menu_voice") == "yes" || iniValue(ini, "menu_voice") != "" {
			log.Println("אבחון: בשלוחה הראשית מוגדר menu_voice — המערכת מקריאה אותו במקום הקובץ M1000.tts.")
		}
	}
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

// cutAtWord חותך בסוף מילה שלמה, כדי לא לקטוע באמצע מילה.
func cutAtWord(r []rune) string {
	s := string(r)
	if i := strings.LastIndex(s, " "); i > len(s)/2 {
		s = s[:i]
	}
	return strings.TrimSpace(s)
}

// fetchFeed שולף את רשימת ההודעות מה-API של Telegram Popup.
func fetchFeed(client *http.Client, feedURL, key string) ([]FeedItem, error) {
	body, err := getJSON(client, feedURL, key)
	if err != nil {
		return nil, err
	}
	var parsed struct {
		Items []FeedItem `json:"items"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, fmt.Errorf("JSON לא תקין מה-feed: %w", err)
	}
	return parsed.Items, nil
}

// fetchChannels שולף את רשימת הערוצים ושמותיהם (/api/channels), לפי הסדר באתר.
func fetchChannels(client *http.Client, feedURL, key string) ([]Channel, error) {
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
		Channels []Channel `json:"channels"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, err
	}
	return parsed.Channels, nil
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

// cleanKey מנקה את המפתח מרווחים בקצוות, מירידות שורה ומתווים בלתי נראים
// (BOM, רווח ברוחב אפס) שנכנסים לפעמים כשמדביקים Secret ב-GitHub.
func cleanKey(k string) string {
	k = strings.TrimSpace(k)
	k = strings.NewReplacer("\r", "", "\n", "", "\xef\xbb\xbf", "", "\xe2\x80\x8b", "").Replace(k)
	return strings.TrimSpace(k)
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
