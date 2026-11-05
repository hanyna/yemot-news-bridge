package main

// מצב "patriots": כלי שרץ על המחשב של בעל הקו (ולא על GitHub), ומעלה לשלוחת
// הפודקאסט את התוכנית המלאה המוכנה מיוטיוב — במקום ההקלטה מהשידור שהעלה הגשר.
// יוטיוב ואתר הצפייה של ערוץ 14 חוסמים את השרתים של GitHub, אבל לא מחשב ביתי
// בישראל; לכן ההורדה המוכנה נעשית כאן.
//
// איך זה משתלב בלי להתנגש בגשר: הגשר ממשיך להקליט מהשידור ולנהל את השלוחה
// (מספור, סבב, תפריט). הכלי הזה רק *מחליף את הקול* של פרק שהגשר כבר סיים
// (מצב 3 ב-podcast.txt) בגרסה המוכנה, ומעדכן את ההקראה לאורך האמיתי. פרק
// שהגשר עוד לא סיים — מדלגים עליו, וננסה בהרצה הבאה. יום שאין לו סרטון מוכן
// ביוטיוב (בעיקר חמישי) — נשארת ההקלטה מהשידור.
//
// מריצים: yemot-patriots.exe   (המשתנים נטענים מקובץ patriots.env ליד ה-exe
// דרך המתקין). משתני סביבה:
//   YEMOT_API_KEY   מפתח קבוע של הקו (חובה)
//   PATRIOTS_PLAYLIST  מזהה רשימת ההשמעה ביוטיוב (ברירת מחדל: של הפטריוטים)
//   PATRIOTS_EXT    שלוחת הפודקאסט (ברירת מחדל 3/3)
//   PATRIOTS_START  שעת תחילת השידור, לחישוב התאריך (ברירת מחדל 21:00)
//   PATRIOTS_DAYS_BACK  כמה ימים אחורה לבדוק (ברירת מחדל 2 — חלון החזרה)
//   FFMPEG_DIR      תיקיית ffmpeg.exe / ffprobe.exe / yt-dlp.exe (אם לא ב-PATH)
//   YTDLP           שם/נתיב yt-dlp (ברירת מחדל yt-dlp)
//   YT_COOKIES_BROWSER  דפדפן לעוגיות (chrome/edge/firefox) אם יוטיוב מבקש אימות
//   PATRIOTS_STATE_DIR  איפה נשמר סימון "כבר הועלה" (ברירת מחדל: ליד ה-exe)

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const patriotsDefaultPlaylist = "PL8E54R76rowCcCOn_HQf36o1RF3_oD3z4"

// patriotsMain — נקודת הכניסה של תת-הפקודה (מ-main.go).
func patriotsMain() {
	loadEnvFile(filepath.Join(exeDir(), "patriots.env")) // הגדרות ליד ה-exe
	keepSystemAwake()                                    // שלא יחזור לשינה באמצע (windows; no-op בשאר)

	loc, err := time.LoadLocation("Asia/Jerusalem")
	if err != nil {
		loc = time.FixedZone("IL", 3*3600)
	}
	apiKey := cleanKey(os.Getenv("YEMOT_API_KEY"))
	if apiKey == "" {
		log.Fatal("חסר YEMOT_API_KEY — המפתח הקבוע מעמוד \"מפתחות גישה\" בימות המשיח (בקובץ patriots.env).")
	}
	playlist := envOr("PATRIOTS_PLAYLIST", patriotsDefaultPlaylist)
	ext := envOr("PATRIOTS_EXT", "3/3")
	startH, startM := 21, 0
	if h, m, ok := strings.Cut(envOr("PATRIOTS_START", "21:00"), ":"); ok {
		startH, _ = strconv.Atoi(h)
		startM, _ = strconv.Atoi(m)
	}
	daysBack := envInt("PATRIOTS_DAYS_BACK", 3)
	ytdlp := envOr("YTDLP", "yt-dlp")
	stateDir := envOr("PATRIOTS_STATE_DIR", filepath.Join(exeDir(), "state"))

	// נטפרי/רימון מיירטים את ה-TLS, ולכן yt-dlp וגם קריאות הרשת של Go נכשלות
	// באימות התעודה. מוריד השירים פותר את זה עם --no-check-certificate; כאן
	// עושים אותו דבר. אפשר לכבות עם PATRIOTS_INSECURE_TLS=off.
	patriotsInsecure = envOr("PATRIOTS_INSECURE_TLS", "on") != "off"

	// משתמשים ב-yt-dlp וב-ffmpeg שכבר מותקנים עם מוריד השירים (אותם כלים,
	// כבר עובדים דרך נטפרי) — אם לא הוגדרו במפורש ולא נמצאים ב-PATH.
	ffdir := strings.TrimSpace(os.Getenv("FFMPEG_DIR"))
	if yt, ff := findMoridTools(); yt != "" || ff != "" {
		if ytdlp == "yt-dlp" && yt != "" {
			ytdlp = yt
		}
		if ffdir == "" && ff != "" {
			ffdir = ff
		}
	}
	if ffdir != "" {
		// כך ש-ffmpeg/ffprobe יימצאו גם כשאינם ב-PATH.
		os.Setenv("PATH", ffdir+string(os.PathListSeparator)+os.Getenv("PATH"))
	}
	_ = os.MkdirAll(stateDir, 0o755)

	client := &http.Client{Timeout: 30 * time.Second, Transport: patriotsTransport()}
	if patriotsInsecure {
		feedHTTP.Transport = patriotsTransport() // קריאת עמוד היוטיוב (c14Get)
	}
	y := &yemot{client: client, apiKey: apiKey}

	// קוראים קודם את podcast.txt של הגשר: משם שולפים את מזהה רשימת ההשמעה
	// ואת הפרקים שהגשר כבר יצר — כך המזהים תמיד תואמים לגשר.
	txt, exists, err := y.read(ext, podcastIndex)
	if err != nil {
		log.Fatalf("קריאת %s בשלוחה %s נכשלה: %v", podcastIndex, ext, err)
	}
	if !exists {
		log.Fatalf("אין עדיין %s בשלוחה %s — הגשר צריך קודם ליצור את הפרק (ההקלטה). ננסה שוב בהרצה הבאה.", podcastIndex, ext)
	}
	feed, _, _, eps := parsePodcastIndex(txt)
	if !isC14(feed) {
		log.Fatalf("שלוחה %s אינה פודקאסט של ערוץ 14 (feed=%s).", ext, feed)
	}
	if src, err := parseC14(feed); err == nil && src.playlist != "" {
		playlist = src.playlist
	}

	// מאיפה הסרטונים. מה שלמדנו ב-6.11: התוכנית המלאה של 5.11 כבר הייתה ביוטיוב,
	// אבל (א) רשימת ההשמעה של הפטריוטים נעצרה ב-2.11 — ערוץ 14 מכניסים אליה
	// באיחור של ימים; (ב) בטאב "סרטונים" של הערוץ הכי חדשה הייתה 14.9 — כי
	// התוכנית משודרת בשידור חי, וההקלטה נכנסת לטאב "שידורים חיים" (streams).
	// לכן קוראים את שני הטאבים של הערוץ (streams + videos), ואת הרשימה — ומאחדים.
	// ובנוסף, לכל יום בחלון שעדיין לא נמצא: חיפוש ממוקד ביוטיוב לפי הכותרת
	// המדויקת ("הפטריוטים 5.11.2026 התוכנית המלאה") — מוצא את התוכנית בכל מקום
	// שהיא, בלי להסתמך על איך ערוץ 14 מסדרים אותה.
	channelBase := envOr("PATRIOTS_CHANNEL", "https://www.youtube.com/@c14news")
	channelBase = strings.TrimSuffix(strings.TrimSuffix(strings.TrimRight(channelBase, "/"), "/videos"), "/streams")
	channelName := envOr("PATRIOTS_CHANNEL_NAME", "C14") // שם הערוץ כפי שיוטיוב מציג — לסינון תוצאות חיפוש
	showName := envOr("PATRIOTS_SHOW", "הפטריוטים")     // בערוץ יש גם תוכניות אחרות עם "התוכנית המלאה"

	var vids []c14Video
	seenID := map[string]bool{}
	addAll := func(list []c14Video) (n int) {
		for _, v := range list {
			if !seenID[v.id] {
				seenID[v.id] = true
				vids = append(vids, v)
				n++
			}
		}
		return n
	}
	var parts []string
	for _, tab := range []string{"/streams", "/videos"} {
		l, e := ytdlpList(ytdlp, channelBase+tab, 500) // ~שבוע של העלאות בערוץ עמוס
		if e != nil {
			log.Printf("הערה: %s של הערוץ לא נקרא: %v", tab, e)
			continue
		}
		parts = append(parts, fmt.Sprintf("%s=%d", strings.TrimPrefix(tab, "/"), addAll(l)))
	}
	if l, e := ytdlpPlaylist(ytdlp, playlist); e == nil {
		parts = append(parts, fmt.Sprintf("playlist=%d", addAll(l)))
	} else {
		log.Printf("הערה: רשימת ההשמעה דרך yt-dlp לא נקראה: %v", e)
	}
	if len(vids) == 0 {
		// הכול נכשל — ניסיון אחרון דרך עמוד היוטיוב של הרשימה.
		if _, v2, e2 := c14PageVideos(playlist); e2 == nil {
			parts = append(parts, fmt.Sprintf("page=%d", addAll(v2)))
		} else {
			log.Fatalf("קריאת הסרטונים מיוטיוב נכשלה: %v", e2)
		}
	}
	// רק התוכנית שלנו (לפי השם בכותרת); הערוץ מכיל גם תוכניות אחרות.
	kept := vids[:0]
	for _, v := range vids {
		if strings.Contains(v.title, showName) {
			kept = append(kept, v)
		}
	}
	log.Printf("מקורות: %s — סה\"כ %d סרטונים, מהם %d של %s.", strings.Join(parts, ", "), len(vids), len(kept), showName)
	vids = kept

	now := time.Now().In(loc)

	// חיפוש ממוקד לימים בחלון שאין להם עדיין תוכנית מלאה ברשימה.
	have := map[string]bool{}
	for _, v := range vids {
		if c14IsFull(v.title) {
			if m := reC14Date.FindStringSubmatch(v.title); m != nil {
				have[normDate(m)] = true
			}
		}
	}
	for i := 0; i <= daysBack; i++ {
		d := now.AddDate(0, 0, -i)
		if d.Weekday() == time.Friday {
			continue
		}
		start := time.Date(d.Year(), d.Month(), d.Day(), startH, startM, 0, 0, loc)
		if now.Before(start.Add(90 * time.Minute)) {
			continue // השידור עוד לא הסתיים — אין מה לחפש
		}
		key := fmt.Sprintf("%d.%d.%d", d.Day(), int(d.Month()), d.Year())
		if have[key] {
			continue
		}
		found := ytdlpSearchDay(ytdlp, showName, channelName, d)
		if len(found) > 0 {
			log.Printf("חיפוש ממוקד %s: נמצא — %.80s", key, found[0].title)
			addAll(found) // מצטרף ל-vids (עם סינון כפילויות)
			have[key] = true
		} else {
			log.Printf("חיפוש ממוקד %s: לא נמצא ביוטיוב (עוד לא עלה).", key)
		}
	}
	// לוג עזר: כמה "תוכנית מלאה" נמצאו ומה התאריך האחרון — כדי שאפשר יהיה לראות
	// מיד אם הבעיה בקריאת היוטיוב או בהתאמה לקו.
	var fullDates []string
	for _, v := range vids {
		if c14IsFull(v.title) {
			if m := reC14Date.FindStringSubmatch(v.title); m != nil {
				fullDates = append(fullDates, fmt.Sprintf("%s.%s", m[1], m[2]))
			}
		}
	}
	if len(fullDates) > 3 {
		fullDates = fullDates[:3]
	}
	log.Printf("נמצאו תוכניות מלאות ביוטיוב (אחרונות): %s", strings.Join(fullDates, ", "))

	// התאריך האמיתי הכי חדש מבין כל התוכניות המלאות ברשימה (אבחון: כך רואים
	// אם בכלל יש תוכנית מהימים האחרונים, או שהבעיה בחלון הזמן/בזיהוי).
	var newest time.Time
	nFull := 0
	for _, v := range vids {
		if !c14IsFull(v.title) {
			continue
		}
		if m := reC14Date.FindStringSubmatch(v.title); m != nil {
			d, _ := strconv.Atoi(m[1])
			mo, _ := strconv.Atoi(m[2])
			yr, _ := strconv.Atoi(m[3])
			if mo >= 1 && mo <= 12 && d >= 1 && d <= 31 {
				nFull++
				if t := time.Date(yr, time.Month(mo), d, 0, 0, 0, 0, loc); t.After(newest) {
					newest = t
				}
			}
		}
	}
	if !newest.IsZero() {
		log.Printf("התוכנית המלאה הכי חדשה ברשימה: %s (מתוך %d תוכניות מלאות). היום: %s.",
			newest.Format("02.01.2006"), nFull, now.Format("02.01.2006"))
	}

	done, skipped, failed := 0, 0, 0
	lastReplaced := ""
	seen := map[string]bool{}
	for _, v := range vids {
		if !c14IsFull(v.title) {
			continue
		}
		m := reC14Date.FindStringSubmatch(v.title)
		if m == nil {
			continue
		}
		d, _ := strconv.Atoi(m[1])
		mo, _ := strconv.Atoi(m[2])
		yr, _ := strconv.Atoi(m[3])
		if mo < 1 || mo > 12 || d < 1 || d > 31 {
			continue
		}
		start := time.Date(yr, time.Month(mo), d, startH, startM, 0, 0, loc)
		day := start.Format("2006-01-02")
		if seen[day] || start.Weekday() == time.Friday {
			continue // יום אחד לכל תאריך; בשישי אין תוכנית
		}
		seen[day] = true
		if now.Sub(start) > time.Duration(daysBack)*24*time.Hour || now.Before(start) {
			continue // ישן מדי, או עדיין לא שודר
		}
		ep := eps[c14EpisodeID(playlist, day)]
		if ep == nil {
			log.Printf("%s: הגשר עוד לא יצר את הפרק — נדלג, ננסה שוב בהרצה הבאה.", day)
			skipped++
			continue
		}
		if ep.state != podDone {
			log.Printf("%s: הגשר עוד לא סיים את ההקלטה (מצב %d) — נדלג.", day, ep.state)
			skipped++
			continue
		}
		marker := filepath.Join(stateDir, "done-"+shortPlaylist(playlist)+"-"+day+".txt")
		if _, err := os.Stat(marker); err == nil {
			continue // כבר הוחלף בגרסה המוכנה
		}
		showName := c14ShowName(v.title)
		if err := patriotsUpgrade(y, ext, ytdlp, v.id, ep.base, showName, start, loc); err != nil {
			log.Printf("%s: ההחלפה בגרסה המוכנה נכשלה (נשארת ההקלטה): %v", day, err)
			failed++
			continue
		}
		_ = os.WriteFile(marker, []byte(time.Now().Format(time.RFC3339)+"\n"+v.id+"\n"), 0o644)
		log.Printf("%s: הפרק הוחלף בגרסה המוכנה מיוטיוב (שלוחה %s, קובץ %s).", day, ext, audioFile(ep.base))
		done++
		lastReplaced = day
	}
	log.Printf("סיום: %d הוחלפו, %d דילוגים, %d נכשלו.", done, skipped, failed)

	// שומרים סטטוס על הקו כדי שהדאשבורד (דרך הגשר) יראה שהכלי רץ ומתי הוחלף לאחרונה.
	writePatriotsStatus(y, parentExt(ext), done, skipped, failed, lastReplaced, fullDates)
}

// parentExt: שלוחת האב ("3/3" → "3"); בלי "/" מחזיר כמו שהוא.
func parentExt(ext string) string {
	if i := strings.LastIndex(ext, "/"); i > 0 {
		return ext[:i]
	}
	return ext
}

// writePatriotsStatus מעלה patriots-status.json לשלוחה — הגשר קורא אותו ומצרף
// לדיווח המצב ב-GitHub, כדי שיופיע בדאשבורד. כישלון כאן לא נורא (רק הדיווח).
func writePatriotsStatus(y *yemot, ext string, done, skipped, failed int, lastReplaced string, found []string) {
	b, _ := json.Marshal(map[string]any{
		"last_run": time.Now().UTC().Format(time.RFC3339),
		"last_replaced": lastReplaced, "done": done, "skipped": skipped, "failed": failed,
		"found": found,
	})
	if err := y.upload(ext, "patriots-status.json", string(b)); err != nil {
		log.Printf("הערה: שמירת סטטוס הפטריוטים לשלוחה %s נכשלה (לא קריטי): %v", ext, err)
	}
}

// patriotsUpgrade מוריד את הסרטון מיוטיוב, מנרמל, מחליף את הקול בשלוחה ומעדכן
// את ההקראה לאורך האמיתי של הגרסה המוכנה.
func patriotsUpgrade(y *yemot, ext, ytdlp, videoID string, base int, showName string, start time.Time, loc *time.Location) error {
	dir, err := os.MkdirTemp("", "patriots")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	src := filepath.Join(dir, "src.mp3")
	if err := ytdlpAudio(ytdlp, videoID, src); err != nil {
		return err
	}
	out := filepath.Join(dir, "out.mp3")
	if err := transcode(src, out, 0, true); err != nil { // loudnorm + 16k mono, כמו בגשר
		return fmt.Errorf("עיבוד הקול (ffmpeg): %w", err)
	}
	data, err := os.ReadFile(out)
	if err != nil {
		return err
	}
	if len(data) == 0 {
		return fmt.Errorf("הקול יצא ריק")
	}
	if err := y.uploadFile(ext, audioFile(base), "audio.mp3", data, true); err != nil {
		return fmt.Errorf("העלאה לשלוחה: %w", err)
	}
	secs := ffprobeSeconds(out)
	if err := y.upload(ext, introFile(base), patriotsIntro(showName, start, secs)); err != nil {
		return fmt.Errorf("עדכון ההקראה: %w", err)
	}
	return nil
}

// patriotsIntro: "<שם>. פורסם ב 8 באוקטובר, באורך שעה ו 33 דקות." (כמו podcastIntro).
func patriotsIntro(showName string, start time.Time, secs int) string {
	s := cleanForSpeech(showName)
	if s == "" {
		s = "הפטריוטים"
	}
	s += fmt.Sprintf(". פורסם ב %d %s", start.Day(), months[start.Month()])
	if secs >= 60 {
		mnt := (secs + 30) / 60
		if d := spokenDuration(fmt.Sprintf("%d:%02d:00", mnt/60, mnt%60)); d != "" {
			s += ", באורך " + d
		}
	}
	s += "."
	if r := []rune(s); len(r) > maxPerFile {
		s = cutAtWord(r[:maxPerFile]) + "."
	}
	return s
}

// patriotsInsecure: לדלג על אימות תעודת TLS (נטפרי/רימון). נקבע ב-patriotsMain.
var patriotsInsecure bool

// patriotsTransport: מעביר HTTP שמדלג על אימות תעודה כשצריך (נטפרי).
func patriotsTransport() http.RoundTripper {
	if !patriotsInsecure {
		return nil
	}
	return &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}
}

// utf8Env: מכריח את yt-dlp (פייתון) להוציא UTF-8 ל-pipe. בלי זה, בווינדוס
// פייתון פולט ל-pipe בקידוד החלון (cp1255) ולא ב-UTF-8, הכותרות בעברית מגיעות
// משובשות ל-exe, וההתאמה ל"התוכנית המלאה" נכשלת (0 נמצאו למרות שהתוכניות שם).
func utf8Env() []string {
	return append(os.Environ(), "PYTHONIOENCODING=utf-8", "PYTHONUTF8=1")
}

// findMoridTools מאתר את yt-dlp.exe ו-ffmpeg.exe שמוריד השירים כבר התקין
// (ברירת מחדל ב-%LOCALAPPDATA%\MoridHashirim). מחזיר נתיב yt-dlp ותיקיית ffmpeg.
func findMoridTools() (ytdlp, ffdir string) {
	roots := []string{}
	if la := os.Getenv("LOCALAPPDATA"); la != "" {
		roots = append(roots, filepath.Join(la, "MoridHashirim"))
	}
	if ad := os.Getenv("APPDATA"); ad != "" {
		roots = append(roots, filepath.Join(ad, "MoridHashirim"))
	}
	for _, root := range roots {
		if root == "" {
			continue
		}
		_ = filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return nil
			}
			switch strings.ToLower(d.Name()) {
			case "yt-dlp.exe":
				if ytdlp == "" {
					ytdlp = p
				}
			case "ffmpeg.exe":
				if ffdir == "" {
					ffdir = filepath.Dir(p)
				}
			}
			return nil
		})
		if ytdlp != "" && ffdir != "" {
			break
		}
	}
	return ytdlp, ffdir
}

// normDate: תאריך מהכותרת כ-"5.11.2026" (בלי אפסים מובילים), להשוואה אחידה.
func normDate(m []string) string {
	d, _ := strconv.Atoi(m[1])
	mo, _ := strconv.Atoi(m[2])
	y, _ := strconv.Atoi(m[3])
	return fmt.Sprintf("%d.%d.%d", d, mo, y)
}

// ytdlpSearchDay מחפש ביוטיוב את התוכנית המלאה של יום מסוים לפי הכותרת —
// "הפטריוטים 5.11.2026 התוכנית המלאה" — ומחזיר רק תוצאות של הערוץ הנכון,
// עם אותו תאריך בכותרת. כך מוצאים את התוכנית בכל מקום שערוץ 14 שמו אותה.
func ytdlpSearchDay(ytdlp, show, channelName string, d time.Time) []c14Video {
	want := fmt.Sprintf("%d.%d.%d", d.Day(), int(d.Month()), d.Year())
	q := fmt.Sprintf("%s %s התוכנית המלאה", show, want)
	args := []string{"--flat-playlist", "--no-warnings", "--no-progress",
		"--print", "%(id)s|%(channel)s|%(title)s"}
	if patriotsInsecure {
		args = append(args, "--no-check-certificate")
	}
	if b := strings.TrimSpace(os.Getenv("YT_COOKIES_BROWSER")); b != "" {
		args = append(args, "--cookies-from-browser", b)
	}
	args = append(args, "ytsearch10:"+q)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, ytdlp, args...)
	cmd.Env = utf8Env()
	var out strings.Builder
	cmd.Stdout, cmd.Stderr = &out, &out
	if err := cmd.Run(); err != nil {
		log.Printf("הערה: חיפוש ביוטיוב (%s) נכשל: %v: %.200s", want, err, strings.TrimSpace(out.String()))
		return nil
	}
	return filterSearchLines(out.String(), show, channelName, want)
}

// filterSearchLines מסנן שורות "id|channel|title" מחיפוש: רק תוכנית מלאה של
// התוכנית שלנו, מהערוץ הנכון, עם התאריך המבוקש בכותרת.
func filterSearchLines(out, show, channelName, want string) []c14Video {
	var res []c14Video
	for _, line := range strings.Split(out, "\n") {
		f := strings.SplitN(strings.TrimSpace(line), "|", 3) // הכותרת עצמה מכילה '|'
		if len(f) != 3 || len(f[0]) != 11 {
			continue
		}
		ch, _ := decodeTitle(f[1])
		title, _ := decodeTitle(f[2])
		if !c14IsFull(title) || !strings.Contains(title, show) {
			continue
		}
		m := reC14Date.FindStringSubmatch(title)
		if m == nil || normDate(m) != want {
			continue
		}
		if channelName != "" && !strings.Contains(strings.ToUpper(ch), strings.ToUpper(channelName)) {
			continue // העתקה של ערוץ אחר
		}
		res = append(res, c14Video{id: f[0], title: title})
	}
	return res
}

// ytdlpPlaylist מביא את כל הסרטונים (מזהה + כותרת) של רשימת השמעה ביוטיוב.
func ytdlpPlaylist(ytdlp, playlist string) ([]c14Video, error) {
	return ytdlpList(ytdlp, "https://www.youtube.com/playlist?list="+playlist, 0)
}

// ytdlpList מביא סרטונים (מזהה + כותרת) מכתובת ביוטיוב — רשימת השמעה או עמוד
// הסרטונים של ערוץ — דרך yt-dlp (אמין יותר מגירוד העמוד). limit > 0 = רק
// ה-N הראשונים (בערוץ: החדשים ביותר) — מהיר, ומספיק לחלון של כמה ימים.
func ytdlpList(ytdlp, url string, limit int) ([]c14Video, error) {
	args := []string{"--flat-playlist", "--no-warnings", "--no-progress",
		"--print", "%(id)s|%(title)s"}
	if limit > 0 {
		args = append(args, "--playlist-end", strconv.Itoa(limit))
	}
	if patriotsInsecure {
		args = append(args, "--no-check-certificate")
	}
	if b := strings.TrimSpace(os.Getenv("YT_COOKIES_BROWSER")); b != "" {
		args = append(args, "--cookies-from-browser", b)
	}
	args = append(args, url)

	// קריאת 2600+ סרטונים על חיבור מסונן יכולה לקחת זמן — נותנים 6 דקות, ועד 3
	// ניסיונות. כישלון מחזיר גם stdout וגם stderr, כדי שהשגיאה האמיתית תירשם בלוג.
	var out string
	var lastErr error
	for try := 1; try <= 3; try++ {
		ctx, cancel := context.WithTimeout(context.Background(), 6*time.Minute)
		cmd := exec.CommandContext(ctx, ytdlp, args...)
		cmd.Env = utf8Env() // כותרות עברית ב-UTF-8, לא בקידוד החלון
		var outBuf, errBuf strings.Builder
		cmd.Stdout, cmd.Stderr = &outBuf, &errBuf
		err := cmd.Run()
		cancel()
		if err == nil && strings.TrimSpace(outBuf.String()) != "" {
			out = outBuf.String()
			lastErr = nil
			break
		}
		tail := strings.TrimSpace(errBuf.String())
		if tail == "" {
			tail = strings.TrimSpace(outBuf.String()) // לפעמים yt-dlp כותב ל-stdout
		}
		if err == nil {
			err = fmt.Errorf("yt-dlp לא החזיר רשימה")
		}
		lastErr = fmt.Errorf("ניסיון %d: %v: %.300s", try, err, tail)
		log.Printf("הערה: yt-dlp נכשל בקריאת הרשימה — %v", lastErr)
		if try < 3 {
			time.Sleep(10 * time.Second)
		}
	}
	if lastErr != nil {
		return nil, lastErr
	}
	var vids []c14Video
	enc := map[string]int{}
	for _, line := range strings.Split(out, "\n") {
		id, title, ok := strings.Cut(strings.TrimSpace(line), "|")
		if !ok || len(id) != 11 {
			continue
		}
		// הכותרת עלולה להגיע בקידוד החלון (cp1255/cp862) ולא UTF-8 — מפענחים.
		dec, how := decodeTitle(title)
		enc[how]++
		vids = append(vids, c14Video{id: id, title: dec})
	}
	log.Printf("קידוד הכותרות מ-yt-dlp: %v", enc)
	return vids, nil
}

// ytdlpAudio מוריד את הקול של סרטון יוטיוב לקובץ MP3 (דרך yt-dlp + ffmpeg).
func ytdlpAudio(ytdlp, videoID, out string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
	defer cancel()
	args := []string{
		"--no-playlist", "--no-progress", "--no-warnings",
		"-f", "bestaudio/best",
		"-x", "--audio-format", "mp3", "--audio-quality", "0",
		"-o", strings.TrimSuffix(out, ".mp3") + ".%(ext)s",
	}
	if patriotsInsecure {
		args = append(args, "--no-check-certificate") // נטפרי/רימון
	}
	if b := strings.TrimSpace(os.Getenv("YT_COOKIES_BROWSER")); b != "" {
		args = append(args, "--cookies-from-browser", b)
	}
	args = append(args, "https://www.youtube.com/watch?v="+videoID)
	cmd := exec.CommandContext(ctx, ytdlp, args...)
	cmd.Env = utf8Env()
	outBuf := &strings.Builder{}
	cmd.Stdout, cmd.Stderr = outBuf, outBuf
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(outBuf.String())
		if strings.Contains(msg, "Sign in to confirm") || strings.Contains(msg, "bot") {
			return fmt.Errorf("יוטיוב ביקש אימות — הגדירו YT_COOKIES_BROWSER (למשל chrome): %v: %.300s", err, msg)
		}
		return fmt.Errorf("yt-dlp: %v: %.400s", err, msg)
	}
	if _, err := os.Stat(out); err != nil {
		return fmt.Errorf("yt-dlp לא יצר קובץ קול: %.300s", strings.TrimSpace(outBuf.String()))
	}
	return nil
}

// ffprobeSeconds — אורך קובץ הקול בשניות (0 אם לא הצליח).
func ffprobeSeconds(path string) int {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "ffprobe", "-v", "error", "-show_entries", "format=duration", "-of", "default=nw=1:nk=1", path)
	b, err := cmd.Output()
	if err != nil {
		return 0
	}
	f, _ := strconv.ParseFloat(strings.TrimSpace(string(b)), 64)
	return int(f)
}

// loadEnvFile טוען קובץ הגדרות פשוט (KEY=VALUE בכל שורה; # הערה) לתוך משתני
// הסביבה — רק מה שעוד לא הוגדר, כך שאפשר גם לדרוס דרך הסביבה. חסר — לא נורא.
func loadEnvFile(path string) {
	b, err := os.ReadFile(path)
	if err != nil {
		return
	}
	for _, line := range strings.Split(strings.ReplaceAll(string(b), "\r\n", "\n"), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		k, v = strings.TrimSpace(k), strings.TrimSpace(v)
		v = strings.Trim(v, `"'`)
		if k != "" {
			if _, set := os.LookupEnv(k); !set {
				os.Setenv(k, v)
			}
		}
	}
}

func exeDir() string {
	p, err := os.Executable()
	if err != nil {
		return "."
	}
	return filepath.Dir(p)
}

func shortPlaylist(p string) string {
	if len(p) > 8 {
		return p[len(p)-8:]
	}
	return p
}
