// yemot-news-bridge
//
// גשר בין Telegram Popup ("ערוץ חי") לבין קו טלפון בימות המשיח.
// רץ ב-GitHub Actions ובודק הודעות חדשות כל 20 שניות. כל הודעה נשמרת בקובץ
// משלה, עם מספר קבוע, כל עוד יש מקום בשלוחה (ראו archive.go); סרטונים והודעות
// קוליות — גם הקול שלהם (audio.go):
//
//	שלוחה 1    — כל הערוצים יחד (החדשה נשמעת ראשונה, וממשיכים אחורה)
//	שלוחה 2    — בחירת כתב → 2/1, 2/2, ... שלוחה לכל ערוץ
//	תפריט ראשי — הודעת פתיחה (M1000.tts) שמפרטת את השלוחות
package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	_ "time/tzdata" // שעון ישראל עובד גם אם בשרת אין קבצי אזורי זמן
)

// FeedItem משקף פריט בודד שחוזר מ-/api/messages של Telegram Popup.
type FeedItem struct {
	ID      int    `json:"id"`
	Channel string `json:"channel"`
	TS      int64  `json:"ts"`
	Text    string `json:"text"`
	HTML    string `json:"html"` // לזיהוי מדיה (תמונה/סרטון/קולית/סקר)

	RawText string `json:"-"` // הטקסט המקורי (להקשר בניתוח תמונה — vision.go)

	Flash     bool `json:"-"` // מכיל מילת מבזק
	MediaOnly bool `json:"-"` // אין טקסט — רק מדיה (לא עובר סינון כפילויות)
	OldPromo  bool `json:"-"` // היה נזרק ככלל הישן ("להצטרפות לערוץ..." = פרסומת) — ראו promoSince
}

type Channel struct {
	Name  string `json:"name"`
	Title string `json:"title"`
}

// שמות קריאים לערוצים — גיבוי למקרה שהשרת לא מחזיר שם ערוץ (ההקראות בארכיון
// נשארות לתמיד, אז עדיף שם אמיתי ולא הכינוי באנגלית).
var knownNames = map[string]string{
	"elisha_yered":   "אלישע ירד",
	"hakolhayehudi":  "הקול היהודי",
	"realelchangr":   "אלחנן גרונר",
	"ayeletlash":     "איילת לאש",
	"SamariaUpdates": "עדכוני השומרון",
	"nilchamim":      "נלחמים על החיים",
	"hatzhalhyosh":   "הצלה יהודה ושומרון",
}

// channelsWait: כמה סבבים ממתינים לרשימת הערוצים (ושמותיהם) לפני שמוסיפים
// הודעות לארכיון עם שמות הגיבוי.
const channelsWait = 10

const (
	welcomeGreeting = "ברוכים הבאים לקו עדכוני ארץ ישראל."
	welcomeHead     = "לכל העדכונים, הקישו 1."

	chooseExt   = "2"  // תפריט בחירת כתב
	resumeExt   = "5"  // המשך מהמקום שהפסקתם
	recordExt   = "6"  // הודעה למנהל המערכת
	listExt     = "8"  // צינתוקים וטלזכור
	registerExt = "7"  // הרשמה זמנית של בעל הקו לצינתוקי המנהל (ADMIN_TZINTUK_REGISTER=on)
	maxPerFile  = 1000 // ימות המשיח: קובץ TTS מוגבל לכ-1,300 תווים — משאירים מרווח
	failLimit   = 10   // כמה סבבים כושלים ברצף עד שמכשילים את הריצה (= מייל מ-GitHub)
)

type config struct {
	feedURL, feedKey string
	ext              string // שלוחת "כל העדכונים"
	channelExts      bool
	audio            bool   // להעלות את הקול של סרטונים והודעות קוליות
	audioMax         int    // שניות; קול ארוך מזה — רק התיאור (0 = בלי הגבלה)
	welcome          string // "" = אוטומטי, "off" = כבוי
	voice, rate      string
	publicList       string          // רשימת הצינתוקים הכללית (שלוחה 8/1)
	lineNumber       string          // מספר הקו — יעד החיוג בטלזכור (שלוחה 8/2)
	callback         bool            // שלוחה 8/3: שיחה חוזרת מהמערכת (חוסכת דקות למתקשר)
	adminList        string          // רשימת הצינתוקים של המנהל (הודעה חדשה בשלוחה 6)
	adminRegister    bool            // שלוחה 7 = הרשמה לרשימת המנהל (זמני)
	adminListen      string          // שלוחה נסתרת: השמעת ההודעות שהשאירו בשלוחה 6 (0/148; "off" = כבוי)
	exclude          map[string]bool // ערוצים שהוצאו מהקו (EXCLUDE_CHANNELS; באותיות קטנות)
	podcasts         []podcastSource // שלוחה 3: פודקאסטים (podcast.go)
	podcastExt       string
	podcastKeep      int // כמה פרקים אחרונים נשמרים מכל פודקאסט
	loc              *time.Location
	y                *yemot
	tg               *tgSource     // קריאה ישירה מטלגרם (nil = דרך שרת ערוץ חי) — telegram.go
	vision           *visionClient // ניתוח תמונות עם Gemini (nil = כבוי) — vision.go
	speech           *speaker      // קול מוכן מראש להודעות (nil = כבוי) — voice.go
	music            *menuMusic    // שיר ברקע של התפריט הראשי (nil = בלי) — music.go
	client           *http.Client  // ל-API של ערוץ חי
	feedClient       *http.Client  // זמן המתנה ארוך — Render מתעורר לאט
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

	arch      map[string]*archive // שלוחה → הארכיון שלה (נטען פעם אחת בכל הפעלה)
	chMap     map[string]string   // ערוץ → שלוחת כתב (קבוע — נשמר באינדקס)
	mapped    bool                // chMap נקרא מהשלוחות בהפעלה הזו
	titleSet  map[string]string   // כותרות שלוחות כתב שהועלו
	digitsSet map[string]bool     // file_amount_digits הוגדר
	aw        *audioWorker        // הקול של סרטונים, הודעות קוליות ופרקי פודקאסטים (ברקע)
	pods      map[string]*podcast // שלוחה → הפודקאסט שבה (נטען פעם אחת בכל הפעלה)
	purged    bool                // הודעות של ערוצים שהוצאו מהקו נמחקו (פעם אחת בכל הפעלה)

	podMenuText string // תפריט הפודקאסטים שהועלה
	podActive   bool   // בפעם האחרונה שכל הפודקאסטים נטענו — היה מה לשמוע

	noChannels int // כמה סבבים ממתינים לרשימת הערוצים לפני שמוסיפים לארכיון בלעדיה

	special      map[string]bool      // שלוחות מיוחדות בהפעלה הזו: true=הוגדרה, false=לא של הגשר
	failAt       map[string]time.Time // מתי נכשל ניסיון אחרון להגדיר שלוחה (ניסיון חוזר אחרי setupRetry)
	warned       map[string]bool      // הודעות הסבר שכבר נרשמו בלוג בהפעלה הזו
	chooserText  string               // תפריט בחירת הכתב שהועלה
	listMenuText string

	lastNewest int64     // לוג טריות: ההודעה החדשה ביותר שדווחה
	lastStatus time.Time // לוג טריות: מתי דווח לאחרונה
}

func main() {
	statusLoc, err := time.LoadLocation("Asia/Jerusalem")
	if err != nil {
		statusLoc = time.FixedZone("IL", 3*3600)
	}
	var rep *statusReport
	if envInt("RUN_MINUTES", 0) > 0 {
		// דוח מצב ב-GitHub Issue (status.go): כל שורת לוג נבדקת אם היא בעיה.
		if rep = newStatusReport(statusLoc); rep != nil {
			log.SetOutput(rep)
			lineStatus = rep
		}
	}
	cfg := config{
		feedURL:       envOr("TGPOPUP_URL", "https://telegram-popup.onrender.com/api/messages"),
		feedKey:       strings.TrimSpace(os.Getenv("TGPOPUP_KEY")),
		ext:           envOr("YEMOT_EXT", "1"),
		channelExts:   envOr("CHANNEL_EXTS", "on") != "off",
		audio:         envOr("AUDIO", "on") != "off",
		audioMax:      envInt("AUDIO_MAX_MINUTES", 20) * 60,
		welcome:       strings.TrimSpace(os.Getenv("YEMOT_WELCOME")),
		voice:         strings.TrimSpace(os.Getenv("YEMOT_VOICE")),
		rate:          strings.TrimSpace(os.Getenv("YEMOT_RATE")),
		publicList:    envOr("PUBLIC_TZINTUK_LIST", "800"),
		lineNumber:    strings.TrimSpace(os.Getenv("YEMOT_LINE_NUMBER")),
		callback:      envOr("CALLBACK_ENABLED", "on") == "on",
		adminList:     envOr("ADMIN_TZINTUK_LIST", "606"),
		adminRegister: envOr("ADMIN_TZINTUK_REGISTER", "off") == "on",
		adminListen:   envOr("ADMIN_LISTEN_EXT", "4"),
		exclude:       parseExclude(os.Getenv("EXCLUDE_CHANNELS")),
		podcasts:      parsePodcasts(os.Getenv("PODCASTS")),
		podcastExt:    envOr("PODCAST_EXT", "3"),
		podcastKeep:   envInt("PODCAST_KEEP", 10),
		client:        &http.Client{Timeout: 30 * time.Second},
		feedClient:    &http.Client{Timeout: 90 * time.Second},
	}
	apiKey := cleanKey(os.Getenv("YEMOT_API_KEY"))
	cfg.tg = newTgSource(envOr("CHANNELS", defaultChannels), os.Getenv("SOURCE"))
	switch {
	case cfg.tg != nil && cfg.feedKey != "":
		log.Printf("מקור ההודעות: ישירות מטלגרם (%d ערוצים, כל ערוץ נבדק בערך כל %v). גיבוי: שרת ערוץ חי, אם טלגרם חוסמים.", len(cfg.tg.chans), cfg.tg.every())
	case cfg.tg != nil:
		log.Printf("מקור ההודעות: ישירות מטלגרם (%d ערוצים). אין TGPOPUP_KEY — בלי גיבוי של שרת ערוץ חי.", len(cfg.tg.chans))
	case cfg.feedKey == "":
		log.Fatal("חסר משתנה סביבה TGPOPUP_KEY (SOURCE=server — ההודעות נלקחות משרת ערוץ חי)")
	default:
		log.Println("מקור ההודעות: שרת ערוץ חי (SOURCE=server).")
	}
	if apiKey == "" {
		log.Fatal("חסר משתנה סביבה YEMOT_API_KEY (המפתח הקבוע מעמוד \"מפתחות גישה\" בימות המשיח)")
	}
	cfg.y = &yemot{client: &http.Client{Timeout: 30 * time.Second}, apiKey: apiKey}
	vkeys := speechKeys(os.Getenv) // אותם מפתחות כמו הקול המוכן — כשאחד במכסה, הבא
	if len(vkeys) == 0 {
		vkeys = []string{""}
	}
	cfg.vision = newVisionClient(vkeys[0], os.Getenv("GEMINI_MODEL"), os.Getenv("VISION"), vkeys[1:]...)
	if cfg.vision != nil {
		log.Println("ניתוח תמונות (Gemini): פעיל — הודעות עם תמונה יקבלו תיאור בהקראה.")
	} else {
		log.Println("ניתוח תמונות: כבוי (אין GEMINI_API_KEY ב-Secrets, או VISION=off).")
	}
	cfg.speech = newSpeakerKeys(speechKeys(os.Getenv), os.Getenv("SPEECH_VOICE"), os.Getenv("SPEECH_MENU_VOICE"), os.Getenv("SPEECH"))
	if cfg.speech != nil {
		log.Printf("קול מוכן מראש (Gemini, קול %s, %d מפתחות): פעיל — כל הודעה עולה גם כקובץ שמע, בלי המתנה בהאזנה.", cfg.speech.voice, len(cfg.speech.keys))
	}
	cfg.music = loadMenuMusic(os.Getenv("MENU_MUSIC"), os.Getenv("MENU_MUSIC_INTRO"), os.Getenv("MENU_MUSIC_VOLUME"))
	switch {
	case cfg.music != nil && cfg.speech == nil:
		log.Println("הערה: מוזיקה בתפריט צריכה את הקול המוכן (SPEECH=on ומפתח GEMINI_API_KEY) — בינתיים התפריט בלי מוזיקה.")
		cfg.music = nil
	case cfg.music != nil:
		log.Printf("מוזיקה בתפריט הראשי: %s — %g שניות שיר, ואז התפריט עם השיר ב-%g%% עוצמה.", cfg.music.name, cfg.music.intro, cfg.music.level*100)
	}
	cfg.loc = statusLoc

	diagnoseRoot(cfg.y)

	st := &state{files: map[string][]string{}, known: map[string]bool{}, chExt: map[string]string{}, blocked: map[string]bool{}, special: map[string]bool{}}
	st.ensureMaps()

	// מצב לולאה: RUN_MINUTES > 0 — נשארים פתוחים ובודקים כל INTERVAL_SECONDS.
	// בלי RUN_MINUTES — סבב אחד וסיום (והקול — בתוך הסבב).
	runFor := time.Duration(envInt("RUN_MINUTES", 0)) * time.Minute
	interval := time.Duration(envInt("INTERVAL_SECONDS", 60)) * time.Second
	if cfg.useWorker() && runFor > 0 {
		st.aw.start(&cfg) // ברקע — ההקראות לא מחכות לקול
	}
	if runFor > 0 {
		cfg.speech.start(&cfg) // ברקע — ההודעה עולה מיד כטקסט, והקול מחליף אותה כשהוא מוכן
	}
	if runFor == 0 {
		if err := syncOnce(&cfg, st); err != nil {
			log.Fatalf("%v", err)
		}
		return
	}

	deadline := time.Now().Add(runFor)
	log.Printf("מצב לולאה: בדיקה כל %v, עד %s (שעון ישראל).", interval, deadline.In(cfg.loc).Format("15:04"))
	mySHA := os.Getenv("GITHUB_SHA")
	var lastSHACheck time.Time
	for {
		// הפעלה ישנה לא ממשיכה לרוץ במקביל לחדשה (היא הייתה דורסת את הקבצים
		// בגרסה הישנה): אם יש קוד חדש יותר ב-main — מסיימים.
		if mySHA != "" && time.Since(lastSHACheck) > 2*time.Minute {
			lastSHACheck = time.Now()
			if latest, err := latestSHA(cfg.client); err == nil && latest != "" && latest != mySHA {
				log.Printf("יש גרסה חדשה של הגשר (%.7s, אני %.7s) — מסיים כדי שהיא תרוץ במקומי.", latest, mySHA)
				// סימון לשלב השרשרת ב-Workflow: לא להפעיל עוד הפעלה (החדשה כבר בתור).
				_ = os.WriteFile(".no-chain", []byte(latest), 0o644)
				st.flush(&cfg)
				rep.finish("הוחלפה בגרסה חדשה של הגשר")
				return
			}
		}
		err := syncOnce(&cfg, st)
		if err != nil {
			st.failures++
			log.Printf("שגיאה בסבב (%d ברצף): %v", st.failures, err)
		} else {
			st.failures = 0
		}
		rep.cycle(err, st.failures)
		if err != nil {
			if st.failures >= failLimit {
				rep.finish(fmt.Sprintf("נעצרה בתקלה — %d סבבים כושלים ברצף", st.failures))
				// הכשלת הריצה → GitHub שולח מייל על ריצה שנכשלה.
				log.Fatalf("הגשר נכשל %d פעמים ברצף — עוצר כדי שתישלח התראה. שגיאה אחרונה: %v", st.failures, err)
			}
		}
		if time.Now().Add(interval).After(deadline) {
			log.Println("זמן הריצה הסתיים — ההפעלה הבאה של ה-Workflow תמשיך מכאן.")
			st.flush(&cfg)
			rep.finish("הסתיימה כרגיל (סוף זמן ההפעלה)")
			return
		}
		time.Sleep(interval)
	}
}

// flush — לפני סיום ההפעלה: מה שה-workers (קול סרטונים, קול מוכן) סיימו מאז הסבב
// האחרון נרשם באינדקסים ונשמר. בלי זה, קול שעלה ברגע האחרון לא מסומן, וההפעלה
// הבאה לא יודעת עליו (למשל: קול בניסוח "ביום שישי" נמחק בחצות כאילו הוא ישן).
func (st *state) flush(cfg *config) {
	st.audioTick(cfg)
	st.speechTick(cfg)
	if err := st.saveArchives(cfg, nowFunc().In(cfg.loc)); err != nil {
		log.Printf("הערה: שמירה לפני סיום נכשלה: %v", err)
	}
}

// ensureMaps מאתחל מפות שחסרות (בבדיקות בונים state חלקי).
func (st *state) ensureMaps() {
	if st.files == nil {
		st.files = map[string][]string{}
	}
	if st.known == nil {
		st.known = map[string]bool{}
	}
	if st.chExt == nil {
		st.chExt = map[string]string{}
	}
	if st.blocked == nil {
		st.blocked = map[string]bool{}
	}
	if st.special == nil {
		st.special = map[string]bool{}
	}
	if st.failAt == nil {
		st.failAt = map[string]time.Time{}
	}
	if st.warned == nil {
		st.warned = map[string]bool{}
	}
	if st.arch == nil {
		st.arch = map[string]*archive{}
	}
	if st.chMap == nil {
		st.chMap = map[string]string{}
	}
	if st.titleSet == nil {
		st.titleSet = map[string]string{}
	}
	if st.digitsSet == nil {
		st.digitsSet = map[string]bool{}
	}
	if st.aw == nil {
		st.aw = newAudioWorker()
	}
	if st.pods == nil {
		st.pods = map[string]*podcast{}
	}
}

// useWorker: יש עבודה ל-audioWorker — הקול של סרטונים והודעות קוליות, או פודקאסטים.
func (cfg *config) useWorker() bool { return cfg.audio || len(cfg.podcasts) > 0 }

// parseExclude: "SamariaUpdates, other" → ערוצים שלא נכנסים לקו (נשארים בערוץ חי).
func parseExclude(s string) map[string]bool {
	m := map[string]bool{}
	for _, f := range strings.FieldsFunc(s, func(r rune) bool { return r == ',' || r == ' ' || r == '\n' || r == '\t' }) {
		m[strings.ToLower(strings.TrimPrefix(f, "@"))] = true
	}
	return m
}

// excluded: ערוץ שהוצא מהקו.
func (cfg *config) excluded(channel string) bool { return cfg.exclude[strings.ToLower(channel)] }

// nowFunc — השעה הנוכחית (בדיקות מזיזות אותה כדי לבדוק "אתמול").
var nowFunc = time.Now

// syncOnce: סבב אחד — שליפה, בנייה, והעלאת מה שהשתנה בלבד.
func syncOnce(cfg *config, st *state) error {
	st.ensureMaps()
	items, tgChans, fromServer, err := fetchItems(cfg)
	if errors.Is(err, errTgNotYet) {
		return nil // טלגרם עוד לא ענו בהפעלה הזו — ממתינים לסבב הבא
	}
	if err != nil {
		return err
	}
	if len(cfg.exclude) > 0 { // ערוצים שהוצאו מהקו (EXCLUDE_CHANNELS) — כאילו אינם
		kept := make([]FeedItem, 0, len(items))
		for _, it := range items {
			if !cfg.excluded(it.Channel) {
				kept = append(kept, it)
			}
		}
		items = kept
	}
	chs, err := tgChans, error(nil)
	if fromServer {
		chs, err = fetchChannels(cfg.client, cfg.feedURL, cfg.feedKey)
	}
	if err == nil {
		st.channels = chs[:0:0]
		for _, c := range chs {
			if !cfg.excluded(c.Name) {
				st.channels = append(st.channels, c)
			}
		}
	} else if st.channels == nil {
		log.Printf("לא הצלחתי לשלוף את רשימת הערוצים (ממשיך בלי שלוחות לפי ערוץ): %v", err)
	}
	titles := map[string]string{}
	for _, c := range st.channels {
		if c.Title != "" && !strings.HasPrefix(c.Title, "@") {
			titles[c.Name] = c.Title
		}
	}

	now := nowFunc().In(cfg.loc)
	logFreshness(cfg, st, items, now, fromServer)
	// ניקוי להקראה, מהישנה לחדשה. כפילויות (אותה הודעה בכמה ערוצים) מסוננות
	// בארכיון עצמו, מול מה שכבר נשמר.
	clean := prepareClean(items)
	sort.SliceStable(clean, func(i, j int) bool {
		if clean[i].TS != clean[j].TS {
			return clean[i].TS < clean[j].TS
		}
		return clean[i].ID < clean[j].ID
	})
	budget := rerenderBudget

	// שלוחה 1: כל הערוצים — ארכיון קבוע. תקלה כאן לא עוצרת את שאר הקו (תפריטים
	// וכו'), אבל הסבב נחשב כושל — כך שתקלה מתמשכת מגיעה במייל.
	var cycleErr error
	if st.channels == nil && st.noChannels < channelsWait {
		// בלי רשימת הערוצים אין שמות קריאים לערוצים, וההקראות נשארות בארכיון
		// לתמיד — ממתינים לה כמה סבבים (תקלה זמנית), ורק אז ממשיכים עם שמות הגיבוי.
		st.noChannels++
	} else if err := st.ensureDigits(cfg, cfg.ext); err != nil {
		cycleErr = fmt.Errorf("שלוחה %s לא עוברת לארכיון עד שההגדרה file_amount_digits=5 תיכנס: %w", cfg.ext, err)
	} else if all, err := st.archiveFor(cfg, cfg.ext, "", true, now); err != nil {
		cycleErr = fmt.Errorf("טעינת הארכיון של שלוחה %s: %w%s", cfg.ext, err, aclHint(err, "GetTextFile"))
	} else if err := all.sync(cfg, st, clean, titles, now); err != nil {
		cycleErr = err
	} else {
		all.rerender(cfg, now, &budget)
	}

	// שלוחה 2: תפריט בחירת כתב → 2/1, 2/2, ... (ארכיון קבוע לכל כתב).
	type choice struct {
		n    int
		text string
	}
	var choices []choice
	if cfg.channelExts {
		exts := st.reporterExts(cfg)
		for _, ch := range st.channels {
			ext, ok := exts[ch.Name]
			if !ok || !ensureChannelExt(cfg, st, ch.Name, ext) {
				continue
			}
			name := speakerName(ch.Name, titles)
			a, err := st.archiveFor(cfg, ext, ch.Name, false, now)
			if err != nil {
				log.Printf("הערה: הארכיון של שלוחה %s (%s) לא נטען: %v", ext, name, err)
				continue
			}
			st.ensureTitle(cfg, ext, updatesOf(name)+".")
			var mine []FeedItem
			for _, it := range clean {
				if it.Channel == ch.Name {
					mine = append(mine, it)
				}
			}
			if err := a.sync(cfg, st, mine, titles, now); err != nil {
				log.Printf("הערה: עדכון שלוחה %s (%s) נכשל: %v", ext, name, err)
				continue
			}
			a.rerender(cfg, now, &budget)
			n, _ := strconv.Atoi(strings.TrimPrefix(ext, chooseExt+"/"))
			choices = append(choices, choice{n, fmt.Sprintf("ל%s הקישו %d.", updatesOf(name), n)})
		}
	}
	sort.Slice(choices, func(i, j int) bool { return choices[i].n < choices[j].n })
	var chooser []string
	for _, c := range choices {
		chooser = append(chooser, c.text)
	}
	// ערוצים שהוצאו מהקו: מה שכבר עלה מהם — נמחק.
	st.purgeExcluded(cfg)

	// שלוחה 3: פודקאסטים — פרקים חדשים לתור (podcast.go).
	st.syncPodcasts(cfg, now)

	// הקול של סרטונים, הודעות קוליות ופרקי פודקאסטים: מה שנוסף בסבב יוצא לעבודה
	// (ברקע), ומה שה-worker סיים נרשם. ואז — שמירת האינדקסים שהשתנו.
	st.audioTick(cfg)
	st.speechTick(cfg)
	podReady := st.finishPodcasts(cfg)
	if err := st.saveArchives(cfg, now); err != nil && cycleErr == nil {
		cycleErr = err
	}

	// התפריט הראשי: אילו שלוחות פעילות.
	var menu []string
	if len(chooser) > 0 && setupSpecial(cfg, st, chooseExt, "type=menu\ndigits=1") {
		text := "בחירת כתב. " + strings.Join(chooser, " ")
		if r := []rune(text); len(r) > maxPerFile {
			text = cutAtWord(r[:maxPerFile])
		}
		if text != st.chooserText {
			if err := uploadSpoken(cfg, chooseExt, "M1000.tts", text); err != nil {
				log.Printf("הערה: עדכון תפריט בחירת הכתב נכשל: %v", err)
			} else {
				st.chooserText = text
				log.Printf("תפריט בחירת כתב (שלוחה %s): %s", chooseExt, text)
			}
		}
		menu = append(menu, "לבחירת כתב מסוים, הקישו "+chooseExt+".")
	}
	if podReady {
		menu = append(menu, "לפודקאסטים, הקישו "+cfg.podcastExt+".")
	}
	if setupSpecial(cfg, st, resumeExt, "type=last_play") {
		menu = append(menu, "להמשך ההאזנה מהמקום שהפסקתם, הקישו "+resumeExt+".")
	}
	// הקלטה למנהל: אחרי כל הודעה שנשמרת (גם בניתוק) — צינתוק לרשימת המנהל.
	recordIni := "type=record\nsay_record_number=no\nhangup_insert_file=yes"
	if cfg.adminList != "" {
		recordIni += "\nrecord_end_run_tzintuk=yes\nhangup_send_tzintuk=yes\nlist_tzintuk=" + cfg.adminList
	}
	if setupSpecial(cfg, st, recordExt, recordIni) {
		menu = append(menu, "להשארת הודעה למנהל המערכת, הקישו "+recordExt+".")
	}
	// שלוחה 8: צינתוקים, טלזכור, ושיחה חוזרת.
	//   8/1 — הרשמה/הסרה מרשימת הצינתוקים הכללית (type=tzintuk)
	//   8/2 — טלזכור: תזכורת קבועה לחייג לקו בימים ובשעות שהמאזין בוחר
	//   8/3 — שיחה חוזרת מהמערכת, כדי לחסוך למתקשר בדקות שיחה (type=system_sharing)
	if cfg.publicList != "" || cfg.callback {
		ok := setupSpecial(cfg, st, listExt, "type=menu\ndigits=1")
		var opts []string
		if cfg.publicList != "" {
			if ok && setupSpecial(cfg, st, listExt+"/1", "type=tzintuk\nlist_tzintuk="+cfg.publicList) {
				opts = append(opts, "להרשמה או הסרה מרשימת הצינתוקים, הקישו 1.")
			}
			telezIni := "type=telezchor\ntelezchor_end=hangup"
			if cfg.lineNumber != "" {
				telezIni += "\ntelezchor_target_number=" + cfg.lineNumber
			}
			if ok && setupSpecial(cfg, st, listExt+"/2", telezIni) {
				opts = append(opts, "לתזכורת קבועה לחייג לקו, בימים ובשעות שתבחרו, הקישו 2.")
			}
		}
		if cfg.callback {
			// שיחה חוזרת מהמערכת אל אותו מספר שהתקשר ממנו — כדי שלא ישתמש בדקות שלו.
			// ההגדרות כמו בדוגמה בפורום של ימות המשיח (topic/19356).
			if ok && setupSpecial(cfg, st, listExt+"/3", "type=system_sharing\nsystem_sharing_custom_did=real_did\nsystem_sharing_to_myself=yes") {
				opts = append(opts, "לשיחה חוזרת מהמערכת, כדי לחסוך בדקות השיחה שלכם, הקישו 3.")
			}
		}
		if ok {
			// אין אף אפשרות פעילה (למשל אין הרשאה ליצור שלוחות) — השלוחה לא מוכרזת
			// בתפריט, ומי שמגיע אליה מהזיכרון שומע שהיא לא פעילה, ולא תפריט של אפשרויות מתות.
			text := "שלוחה זו אינה פעילה כרגע."
			if len(opts) > 0 {
				text = "צינתוקים ותזכורות. " + strings.Join(opts, " ")
				menu = append(menu, "לצינתוקים ותזכורות, הקישו "+listExt+".")
			}
			if text != st.listMenuText {
				if err := uploadSpoken(cfg, listExt, "M1000.tts", text); err == nil {
					st.listMenuText = text
				}
			}
		}
	}
	// הרשמה חד-פעמית של בעל הקו לרשימת צינתוקי המנהל — שלוחה 7 זמנית, לא מופיעה בתפריט.
	// האזנה להודעות שהשאירו למנהל (שלוחה 6): שלוחה נסתרת (לא בתפריט). ברירת המחדל
	// 0/148 — מקישים 0, אז 148, ואז את סיסמת הניהול. החדשה קודם.
	// שלוחה 0 היא שלוחת הניהול של ימות המשיח (admin_login). גרסה קודמת של הגשר
	// דרסה אותה (0/148) — מחזירים, כל עוד היא לא משמשת להאזנה.
	if !strings.HasPrefix(cfg.adminListen, "0/") {
		setupSpecial(cfg, st, "0", "type=admin_login")
	}
	if l := cfg.adminListen; l != "" && l != "off" {
		ok := true
		if parent, code, nested := strings.Cut(l, "/"); nested {
			// תפריט נסתר שמחכה לקוד בן כמה ספרות. קוד שגוי — חזרה לתפריט הראשי.
			ok = setupSpecial(cfg, st, parent, "type=menu\ndigits="+strconv.Itoa(len(code))+"\nmenu_error_goto=/")
		}
		if ok {
			// סיסמת הניהול של המערכת — לא נשמרת בקוד. נדרשת בכל כניסה; שגויה — לתפריט הראשי.
			setupSpecial(cfg, st, l, "type=playfile\nfolder_to_play="+recordExt+"\nstart=max\npassword=password_admin\npassword_always_required=yes\npassword_error_goto=/")
		}
	}
	if cfg.adminRegister && cfg.adminList != "" {
		setupSpecial(cfg, st, registerExt, "type=tzintuk\nlist_tzintuk="+cfg.adminList)
	}
	// שלוחות ריקות מהמבנה הקודם (3, 4, 7, ו-8 כשאין רשימה) — מחזירות לתפריט.
	for _, old := range []string{"3", "4", "7", "8"} {
		if old == listExt && (cfg.publicList != "" || cfg.callback) {
			continue
		}
		if old == cfg.podcastExt && len(cfg.podcasts) > 0 {
			continue
		}
		if old == cfg.adminListen || strings.HasPrefix(cfg.adminListen, old+"/") {
			continue
		}
		if old == registerExt && cfg.adminRegister {
			continue
		}
		retireExt(cfg, st, old)
	}

	// קול ומהירות — פעם אחת בכל הפעלה: בשלוחה הראשית, בשלוחת כל העדכונים
	// ובשלוחות הכתבים. משנה רק את השורות voice/rate ב-ext.ini הקיים.
	if !st.voiceSet && (cfg.voice != "" || cfg.rate != "") {
		st.voiceSet = true
		exts := []string{"", cfg.ext}
		for _, e := range st.chExt {
			exts = append(exts, e)
		}
		for _, ext := range exts {
			if err := applyVoice(cfg, ext); err != nil {
				log.Printf("הערה: עדכון קול/מהירות בשלוחה %q נכשל: %v", ext, err)
				if strings.Contains(err.Error(), "ACL") {
					log.Println("הערה: מפתח ה-API לא מורשה ל-GetTextFile — צריך להוסיף /api/GetTextFile לרשימת ההרשאות של המפתח.")
					break
				}
			}
		}
	}

	// הודעת פתיחה בתפריט הראשי — רק כשהשתנתה.
	if cfg.welcome != "off" {
		w := cfg.welcome
		if w == "" {
			// הודעת הפתיחה = ברכה ותפריט בלבד. (מבזקים לא מוקראים כאן —
			// המתקשר צריך לשמוע קודם את התפריט.)
			parts := []string{welcomeGreeting}
			parts = append(parts, welcomeHead)
			w = strings.Join(append(parts, menu...), " ")
		}
		if r := []rune(w); len(r) > maxPerFile {
			w = cutAtWord(r[:maxPerFile])
		}
		if w != st.welcome {
			if err := uploadSpoken(cfg, "", "M1000.tts", w); err != nil {
				log.Printf("הערה: עדכון הודעת הפתיחה נכשל: %v", err)
			} else {
				st.welcome = w
				log.Printf("הודעת פתיחה: %s", w)
				checkRootIsMenu(cfg.y)
			}
		}
	}
	return cycleErr
}

// logFreshness רושם בלוג (פעם ב-10 דקות, ובכל פעם שהחדשה ביותר משתנה) כמה
// טריות ההודעות שמגיעות משרת ערוץ חי, ומצב כל ערוץ בשרת (/api/status) —
// כדי לדעת אם עיכוב נובע מהשרת/טלגרם או מהגשר.
func logFreshness(cfg *config, st *state, items []FeedItem, now time.Time, fromServer bool) {
	if !fromServer && cfg.tg != nil {
		cfg.tg.logStatus(now)
	}
	var newest FeedItem
	for _, it := range items {
		if it.TS > newest.TS {
			newest = it
		}
	}
	if newest.TS == st.lastNewest && now.Sub(st.lastStatus) < 10*time.Minute {
		return
	}
	st.lastNewest, st.lastStatus = newest.TS, now
	if newest.TS > 0 {
		age := now.Sub(time.Unix(newest.TS, 0)).Round(time.Minute)
		log.Printf("טריות: ההודעה החדשה ביותר — %s, %s (לפני %v).", newest.Channel, time.Unix(newest.TS, 0).In(cfg.loc).Format("15:04"), age)
	}
	if !fromServer {
		return // מצב השרת של ערוץ חי לא רלוונטי כשקוראים ישירות מטלגרם
	}
	u, err := url.Parse(cfg.feedURL)
	if err != nil {
		return
	}
	u.Path = "/api/status"
	body, err := getJSON(cfg.client, u.String(), cfg.feedKey)
	if err != nil {
		log.Printf("טריות: לא הצלחתי לקרוא את מצב השרת: %v", err)
		return
	}
	var stat struct {
		Channels []struct {
			Channel string `json:"channel"`
			OK      bool   `json:"ok"`
			Blocked bool   `json:"blocked"`
			Error   string `json:"error"`
			LastOK  int64  `json:"last_ok"`
		} `json:"channels"`
		Banner string `json:"banner"`
	}
	if json.Unmarshal(body, &stat) != nil {
		return
	}
	if stat.Banner != "" {
		log.Printf("טריות: הודעת השרת: %s", stat.Banner)
	}
	for _, c := range stat.Channels {
		last := "אף פעם"
		if c.LastOK > 0 {
			last = "לפני " + now.Sub(time.Unix(c.LastOK, 0)).Round(time.Minute).String()
		}
		flag := "תקין"
		if c.Blocked {
			flag = "חסום זמנית ע\"י טלגרם"
		} else if !c.OK {
			flag = "נכשל"
		}
		log.Printf("טריות: ערוץ %s — %s, נקרא בהצלחה לאחרונה %s %s", c.Channel, flag, last, c.Error)
	}
}

// prepare: ניקוי טקסט להקראה, תיאור מדיה, סינון פרסומות, סימון מבזקים,
// השמטת הודעות בלי תוכן, וסינון כפילויות.
func prepare(items []FeedItem) []FeedItem {
	return dedupe(prepareClean(items))
}

// prepareClean: כמו prepare, בלי סינון כפילויות (הארכיון מסנן מול מה שכבר נשמר).
func prepareClean(items []FeedItem) []FeedItem {
	var out []FeedItem
	for _, it := range items {
		note, onlyMedia, skip := mediaNote(it.HTML)
		if skip {
			continue
		}
		it.RawText = it.Text
		text := cleanForSpeech(it.Text)
		if t, ok := stripPromo(text); ok {
			text = t
			it.OldPromo = wasPromoAd(cleanForSpeech(it.Text))
			it.RawText, _ = stripPromo(it.RawText)
		}
		switch {
		case onlyMedia && note != "":
			text = cleanForSpeech(publishedVerb(note) + " " + note)
			it.MediaOnly = true
		case note != "" && text != "":
			text += ". מצורף להודעה: " + cleanForSpeech(note)
		}
		if text == "" || isAd(text) {
			continue
		}
		it.Text = text
		it.Flash = isFlashText(text)
		out = append(out, it)
	}
	return out
}

// publishedVerb: "פורסם סרטון", "פורסמה הודעה קולית", "פורסמו 3 תמונות".
func publishedVerb(note string) string {
	switch {
	case strings.HasSuffix(note, "תמונות"):
		return "פורסמו"
	case strings.HasPrefix(note, "תמונה"), strings.HasPrefix(note, "הודעה"):
		return "פורסמה"
	}
	return "פורסם"
}

// buildParts בונה את טקסטי ההקראה — אחד לכל הודעה:
// "<שם הערוץ>, <מתי>. <תוכן ההודעה>" (withName=false: בלי שם הערוץ).
// מבזק פעיל (חצי שעה מפרסומו) נשמע ראשון ומתחיל ב"מבזק".
func buildParts(items []FeedItem, titles map[string]string, loc *time.Location, now time.Time, max int, newestFirst, withName bool) []string {
	sorted := append([]FeedItem(nil), items...)
	sort.SliceStable(sorted, func(i, j int) bool {
		fi, fj := flashActive(sorted[i], now), flashActive(sorted[j], now)
		if fi != fj {
			return fi
		}
		return sorted[i].TS > sorted[j].TS
	})
	if len(sorted) > max {
		sorted = sorted[:max]
	}
	var parts []string
	for _, it := range sorted {
		parts = append(parts, spokenItem(it, titles, loc, now, withName, maxPerFile))
	}
	if !newestFirst {
		for i, j := 0, len(parts)-1; i < j; i, j = i+1, j-1 {
			parts[i], parts[j] = parts[j], parts[i]
		}
	}
	return parts
}

// spokenItem: הודעה אחת כטקסט להקראה, עד limit תווים.
func spokenItem(it FeedItem, titles map[string]string, loc *time.Location, now time.Time, withName bool, limit int) string {
	t := time.Unix(it.TS, 0).In(loc)
	head := itemHead(it.Channel, t, whenClass(t, now), titles, withName)
	if flashActive(it, now) {
		head = "מבזק. " + head
	}
	body := it.Text
	const cutNote = " המשך ההודעה לא הוקרא."
	room := limit - len([]rune(head)) - len([]rune(cutNote))
	if r := []rune(body); len(r) > room {
		body = cutAtSentence(r[:room]) + cutNote
	}
	return head + body
}

// ensureChannelExt מכין שלוחת השמעה לערוץ (2/1, 2/2, ...). יוצר אותה אם היא
// לא קיימת; שלוחה קיימת משמשת רק אם היא של הגשר (type=playfile, רק קבצי
// הגשר). שלוחה עם הגדרות או קבצים אחרים — לא נוגעים בה ולא מפרסמים אותה.
func ensureChannelExt(cfg *config, st *state, channel, ext string) bool {
	st.ensureMaps()
	if st.chExt[channel] == ext {
		return true
	}
	if st.blocked[ext] || coolingDown(st, ext) {
		return false
	}
	block := func(format string, args ...any) bool {
		st.blocked[ext] = true
		log.Printf(format, args...)
		return false
	}
	want, _ := setIniValues(playfileIni, [][2]string{{"voice", cfg.voice}, {"rate", cfg.rate}})
	info, err := cfg.y.dir(ext)
	if err != nil {
		return failed(st, ext, "הערה: לא הצלחתי לבדוק את שלוחה %s: %v", ext, err)
	}
	if !info.Exists {
		if err := createExt(cfg, st, ext, want); err != nil {
			return failed(st, ext, "הערה: יצירת שלוחה %s (%s) נכשלה: %v", ext, channel, err)
		}
	} else {
		for _, n := range info.Files {
			if !isBridgeFile(n) && !isSystemFile(n) {
				return block("אזהרה: בשלוחה %s יש קובץ %s שאינו של הגשר — לא נוגע בה.", ext, n)
			}
		}
		ini, exists, err := cfg.y.read(ext, "ext.ini")
		switch {
		case err != nil:
			// אין הרשאה לקרוא הגדרות — לפי ההגדרות בפועל שמחזיר GetIVR2Dir.
			if t := info.Ini["type"]; t != "playfile" && hasName(info.Files, "ext.ini") {
				return block("אזהרה: שלוחה %s מוגדרת type=%s ולא כשלוחת השמעה — לא נוגע בה.", ext, t)
			}
		case exists && !isBridgeIni(ini):
			return block("אזהרה: שלוחה %s כבר קיימת עם הגדרות אחרות — לא נוגע בה. ext.ini: %.150s", ext, ini)
		case exists && strings.TrimSpace(ini) == want:
			st.chExt[channel] = ext
			return true
		}
	}
	// בלי ext.ini משלה, שלוחה יורשת את type=menu של שלוחה 2 — ואז הקשה עליה
	// פשוט משמיעה שוב את תפריט הבחירה. לכן תמיד כותבים type=playfile.
	if err := cfg.y.upload(ext, "ext.ini", want); err != nil {
		return failed(st, ext, "הערה: הגדרת שלוחה %s נכשלה: %v", ext, err)
	}
	log.Printf("שלוחה %s הוגדרה לערוץ %s.", ext, channel)
	st.chExt[channel] = ext
	return true
}

// bridgeMarker — קובץ סימון שהגשר שם בשלוחות שהוא הגדיר, כדי לדעת בהפעלות
// הבאות שהשלוחה שלו (גם כשנוספו בה קבצים, למשל הקלטות בשלוחה 6).
// ימות המשיח לא מראים אותו ברשימת הקבצים (GetIVR2Dir) — קוראים אותו ישירות.
const bridgeMarker = "bridge.txt"

// setupRetry: אחרי כישלון בהגדרת שלוחה, כמה זמן לחכות לפני ניסיון חוזר —
// בתוך אותה ריצה (ריצה נשארת פתוחה שעות, אז תקלה זמנית, כמו הרשאה שעוד לא
// נכנסה לתוקף, לא נשארת תקועה עד ההפעלה הבאה).
var setupRetry = 3 * time.Minute

func coolingDown(st *state, ext string) bool {
	t, ok := st.failAt[ext]
	return ok && time.Since(t) < setupRetry
}

// failed רושם בלוג ושומר את זמן הכישלון (לניסיון חוזר אחרי setupRetry).
func failed(st *state, ext, format string, args ...any) bool {
	log.Printf(format, args...)
	st.failAt[ext] = time.Now()
	return false
}

// createExt יוצר שלוחה שלא קיימת ומוודא שהיא באמת נוצרה — לא סומכים על "OK"
// של ימות המשיח לבד (UploadTextFile מחזיר OK גם כשלא נוצר כלום).
func createExt(cfg *config, st *state, ext, ini string) error {
	if err := cfg.y.createExt(ext, ini); err != nil {
		if strings.Contains(err.Error(), "ACL") && !st.warned["UpdateExtension"] {
			st.warned["UpdateExtension"] = true
			log.Println("חסרה הרשאה: מפתח ה-API לא מורשה ל-UpdateExtension, ולכן הגשר לא יכול ליצור שלוחות חדשות (כמו 2/2 או 8/1). " +
				"צריך להוסיף /api/UpdateExtension לרשימת ההרשאות של המפתח באתר ימות המשיח. עד אז — שלוחות שלא קיימות לא מוכרזות בתפריט.")
		}
		return err
	}
	info, err := cfg.y.dir(ext)
	if err != nil {
		return err
	}
	if !info.Exists {
		return fmt.Errorf("ימות המשיח אישרו את היצירה, אבל השלוחה לא קיימת")
	}
	log.Printf("שלוחה %s נוצרה.", ext)
	return nil
}

// setupSpecial מגדיר שלוחה מיוחדת (תפריט / המשך האזנה / הקלטה / צינתוק...) —
// פעם אחת בכל הפעלה. יוצר אותה אם היא לא קיימת. שלוחה קיימת עם קבצים שאינם
// של הגשר ובלי קובץ הסימון — של המשתמש: לא נוגעים ולא מפרסמים.
// מחזיר true רק כשהשלוחה קיימת בפועל ומוגדרת — רק אז מותר להכריז עליה בתפריט.
func setupSpecial(cfg *config, st *state, ext, ini string) bool {
	st.ensureMaps()
	if done, ok := st.special[ext]; ok {
		return done
	}
	if coolingDown(st, ext) {
		return false
	}
	want, _ := setIniValues(ini, [][2]string{{"voice", cfg.voice}, {"rate", cfg.rate}})
	info, err := cfg.y.dir(ext)
	if err != nil {
		return failed(st, ext, "הערה: לא הצלחתי לבדוק את שלוחה %s: %v", ext, err)
	}
	if !info.Exists {
		if err := createExt(cfg, st, ext, want); err != nil {
			return failed(st, ext, "הערה: יצירת שלוחה %s נכשלה: %v", ext, err)
		}
	} else if foreign := foreignFile(info.Files); foreign != "" && !hasMarker(cfg, st, ext) {
		log.Printf("אזהרה: בשלוחה %s יש קובץ %s שאינו של הגשר — לא נוגע בה ולא מפרסם אותה בתפריט.", ext, foreign)
		st.special[ext] = false
		return false
	}
	if err := cfg.y.upload(ext, "ext.ini", want); err != nil {
		return failed(st, ext, "הערה: הגדרת שלוחה %s נכשלה: %v", ext, err)
	}
	_ = cfg.y.upload(ext, bridgeMarker, "שלוחה זו מנוהלת על ידי הגשר (yemot-news-bridge).")
	if iniValue(want, "type") != "playfile" {
		removeStaleTTS(cfg, ext, info.Files)
	}
	log.Printf("שלוחה %s הוגדרה: %s", ext, strings.ReplaceAll(want, "\n", " | "))
	st.special[ext] = true
	return true
}

// foreignFile: קובץ ראשון שאינו של הגשר (ext.ini, NNN.tts, M1000.tts), או "".
func foreignFile(files []string) string {
	for _, n := range files {
		if !isBridgeFile(n) && !isSystemFile(n) && !strings.EqualFold(n, "M1000.tts") && !strings.EqualFold(n, "M1000.wav") {
			return n
		}
	}
	return ""
}

// hasMarker: האם יש בשלוחה את קובץ הסימון של הגשר.
func hasMarker(cfg *config, st *state, ext string) bool {
	_, exists, err := cfg.y.read(ext, bridgeMarker)
	if err != nil && strings.Contains(err.Error(), "ACL") && !st.warned["GetTextFile"] {
		st.warned["GetTextFile"] = true
		log.Println("חסרה הרשאה: מפתח ה-API לא מורשה ל-GetTextFile, ולכן הגשר לא יכול לזהות שלוחות שלו שנוספו בהן קבצים (למשל הקלטות בשלוחה 6). צריך להוסיף /api/GetTextFile להרשאות המפתח.")
	}
	return err == nil && exists
}

func hasName(list []string, name string) bool {
	for _, n := range list {
		if strings.EqualFold(n, name) {
			return true
		}
	}
	return false
}

// removeStaleTTS מוחק קבצי NNN.tts (הקראות חדשות מהמבנה הקודם) משלוחה שאינה
// שלוחת השמעה — שם הם לא מושמעים, רק מבלבלים בניהול הקבצים באתר.
func removeStaleTTS(cfg *config, ext string, files []string) {
	var paths []string
	for _, n := range files {
		if isBridgeFile(n) && strings.HasSuffix(strings.ToLower(n), ".tts") {
			paths = append(paths, ivrPath(ext, n))
		}
	}
	if len(paths) == 0 {
		return
	}
	if err := cfg.y.remove(paths); err != nil {
		log.Printf("הערה: מחיקת הקראות ישנות משלוחה %s לא הצליחה (לא קריטי): %v", ext, err)
		return
	}
	log.Printf("שלוחה %s: נמחקו %d הקראות ישנות מהמבנה הקודם.", ext, len(paths))
}

// retireExt: שלוחת כתב מהמבנה הקודם — אם היא של הגשר, מפנה אותה חזרה
// לתפריט הראשי, כדי שלא יושמעו בה הודעות ישנות.
func retireExt(cfg *config, st *state, ext string) {
	st.ensureMaps()
	key := "retired:" + ext
	if _, ok := st.special[key]; ok {
		return
	}
	st.special[key] = true
	info, err := cfg.y.dir(ext)
	if err != nil || !info.Exists || len(info.Files) == 0 {
		return
	}
	for _, n := range info.Files {
		if !isBridgeFile(n) && !isSystemFile(n) {
			return // לא של הגשר — לא נוגעים
		}
	}
	if info.Ini["type"] != "go_to_folder" || info.Ini["go_to_folder"] != "/" {
		if err := cfg.y.upload(ext, "ext.ini", "type=go_to_folder\ngo_to_folder=/"); err != nil {
			return
		}
		log.Printf("שלוחה %s (מהמבנה הקודם) מפנה עכשיו לתפריט הראשי.", ext)
	}
	removeStaleTTS(cfg, ext, info.Files)
}

// isSystemFile: קבצים שימות המשיח עצמם כותבים לשלוחה (יומנים, כמו
// record_log.ymgr) — לא תוכן של המשתמש, ולא הופכים שלוחה ל"לא של הגשר".
func isSystemFile(name string) bool {
	n := strings.ToLower(name)
	// rating.ini — דירוגים שמאזינים נותנים לקבצים (ימות המשיח כותבים אותו לבד).
	// בעבר הוא נחשב "לא של הגשר", ושלוחת הפודקאסט נחסמה — והתפריט אמר "אינה פעילה".
	return strings.HasSuffix(n, ".ymgr") || n == "rating.ini"
}

// isBridgeFile: קבצים שהגשר עצמו יוצר בשלוחה — ext.ini, NNN.tts (המבנה הקודם),
// קבצי הארכיון (NNNNN.tts / NNNNN.wav) והאינדקס שלו.
func isBridgeFile(name string) bool {
	n := strings.ToLower(name)
	if n == "ext.ini" || n == archiveIndex || n == podcastIndex || strings.HasSuffix(n, spokenSuffix) {
		return true
	}
	if fileNum(n) >= 0 {
		return true
	}
	if len(n) == 7 && strings.HasSuffix(n, ".tts") {
		for _, c := range n[:3] {
			if c < '0' || c > '9' {
				return false
			}
		}
		return true
	}
	return false
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
		case strings.HasPrefix(t, "voice="), strings.HasPrefix(t, "rate="), strings.HasPrefix(t, "file_amount_digits="), strings.HasPrefix(t, "play_beep="):
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
	if info, err := y.dir(""); err != nil {
		log.Printf("אבחון: לא הצלחתי לקרוא את רשימת הקבצים בשלוחה הראשית: %v", err)
	} else {
		log.Printf("אבחון: קבצים בשלוחה הראשית: %s", strings.Join(info.Files, ", "))
		log.Printf("אבחון: שלוחות בשלוחה הראשית: %s", strings.Join(info.Dirs, ", "))
		for _, n := range info.Files {
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

// aclHint: כשהשגיאה היא הרשאה חסרה במפתח — איזו הרשאה להוסיף.
func aclHint(err error, method string) string {
	if err != nil && strings.Contains(err.Error(), "ACL") {
		return " — צריך להוסיף /api/" + method + " לרשימת ההרשאות של המפתח באתר ימות המשיח"
	}
	return ""
}

// updatesOf: "עדכוני אלישע ירד" — אבל ערוץ שנקרא כבר "עדכוני השומרון" לא
// הופך ל"עדכוני עדכוני השומרון".
func updatesOf(name string) string {
	if strings.HasPrefix(name, "עדכוני ") || strings.HasPrefix(name, "עדכון ") {
		return name
	}
	return "עדכוני " + name
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

// cutAtSentence חותך בסוף משפט שלם (. ! ?) אם יש כזה בחצי השני של הטקסט,
// אחרת בסוף מילה.
func cutAtSentence(r []rune) string {
	for i := len(r) - 1; i > len(r)*2/5; i-- {
		if r[i] == '.' || r[i] == '!' || r[i] == '?' {
			return strings.TrimSpace(string(r[:i+1]))
		}
	}
	return cutAtWord(r) + "."
}

// cutAtWord חותך בסוף מילה שלמה, כדי לא לקטוע באמצע מילה.
func cutAtWord(r []rune) string {
	s := string(r)
	if i := strings.LastIndex(s, " "); i > len(s)/2 {
		s = s[:i]
	}
	return strings.TrimSpace(s)
}

// defaultChannels: הערוצים כשאין CHANNELS ב-bridge.yml (אותם ערוצים ובאותו סדר כמו בערוץ חי).
const defaultChannels = "elisha_yered,hakolhayehudi,realelchangr,ayeletlash,SamariaUpdates,nilchamim,hatzhalhyosh"

// fetchItems: ההודעות לסבב הזה — ישירות מטלגרם, או משרת ערוץ חי (SOURCE=server,
// או כשטלגרם חוסמים את כל הערוצים יותר מ-tgFallback). fromServer=true: מהשרת,
// ואז רשימת הערוצים נלקחת גם היא מהשרת. errTgNotYet: טלגרם עוד לא ענו בהפעלה
// הזו — מדלגים על הסבב (בלי הודעות אין מה לעדכן).
func fetchItems(cfg *config) (items []FeedItem, chans []Channel, fromServer bool, err error) {
	if cfg.tg != nil {
		items, chans, blocked := cfg.tg.poll(time.Now())
		if !blocked {
			if cfg.tg.usingSrv {
				cfg.tg.usingSrv = false
				log.Println("טלגרם עונים שוב — חוזר לקרוא ישירות מטלגרם.")
			}
			if !cfg.tg.anyOK() {
				return nil, nil, false, errTgNotYet
			}
			return items, chans, false, nil
		}
		if cfg.feedKey == "" {
			return nil, nil, false, fmt.Errorf("טלגרם חוסמים את כל הערוצים כבר יותר מ-%v, ואין TGPOPUP_KEY לגיבוי דרך שרת ערוץ חי", tgFallback)
		}
		if !cfg.tg.usingSrv {
			cfg.tg.usingSrv = true
			log.Printf("טלגרם חוסמים את כל הערוצים כבר יותר מ-%v — לוקח את ההודעות משרת ערוץ חי עד שטלגרם יחזרו.", tgFallback)
		}
	}
	for attempt := 1; attempt <= 3; attempt++ {
		if items, err = fetchFeed(cfg.feedClient, cfg.feedURL, cfg.feedKey); err == nil {
			return items, nil, true, nil
		}
		log.Printf("ניסיון %d לשליפה מה-feed נכשל: %v", attempt, err)
		if attempt < 3 {
			time.Sleep(time.Duration(attempt) * 10 * time.Second)
		}
	}
	return nil, nil, true, fmt.Errorf("שגיאה בשליפת ההודעות: %w", err)
}

var errTgNotYet = errors.New("טלגרם עוד לא ענו")

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
	cacheKey := u.String() // בלי המפתח
	q := u.Query()
	q.Set("k", key)
	u.RawQuery = q.Encode()
	req, err := http.NewRequest(http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("כתובת לא תקינה")
	}
	etagMu.Lock()
	prev, havePrev := etagCache[cacheKey]
	etagMu.Unlock()
	if havePrev {
		// חיסכון ברוחב הפס של Render: אם שום דבר לא השתנה, השרת עונה 304 בלי גוף.
		req.Header.Set("If-None-Match", prev.etag)
	}
	resp, err := client.Do(req)
	if err != nil {
		// שגיאת רשת כוללת את הכתובת המלאה — עם המפתח. מסתירים אותו.
		if ue, ok := err.(*url.Error); ok {
			ue.URL = strings.Replace(ue.URL, url.QueryEscape(key), "***", -1)
		}
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotModified && havePrev {
		return prev.body, nil
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("סטטוס %d מ-%s: %.200s", resp.StatusCode, u.Path, string(body))
	}
	if et := resp.Header.Get("ETag"); et != "" {
		etagMu.Lock()
		etagCache[cacheKey] = etagEntry{et, body}
		etagMu.Unlock()
	}
	return body, nil
}

// etagCache: התשובה האחרונה מכל כתובת של ערוץ חי, עם ה-ETag שלה. בבדיקה
// הבאה שולחים את ה-ETag, ואם לא השתנה כלום השרת עונה 304 (כמה מאות בתים
// במקום כל רשימת ההודעות) — חוסך את רוב רוחב הפס של Render.
type etagEntry struct {
	etag string
	body []byte
}

var (
	etagMu    sync.Mutex
	etagCache = map[string]etagEntry{}
)

// latestSHA מחזיר את מזהה הקומיט האחרון ב-main (דרך ה-API של GitHub).
func latestSHA(client *http.Client) (string, error) {
	repo := envOr("GITHUB_REPOSITORY", "hanyna/yemot-news-bridge")
	req, err := http.NewRequest(http.MethodGet, "https://api.github.com/repos/"+repo+"/commits/main", nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Accept", "application/vnd.github.sha")
	if tok := os.Getenv("GITHUB_TOKEN"); tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("סטטוס %d", resp.StatusCode)
	}
	return strings.TrimSpace(string(body)), nil
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
