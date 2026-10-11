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
	keepSystemAwake() // שלא יחזור לשינה באמצע (windows; no-op בשאר)

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
	daysBack := envInt("PATRIOTS_DAYS_BACK", 2)
	ytdlp := envOr("YTDLP", "yt-dlp")
	stateDir := envOr("PATRIOTS_STATE_DIR", filepath.Join(exeDir(), "state"))

	if dir := strings.TrimSpace(os.Getenv("FFMPEG_DIR")); dir != "" {
		// כך ש-ffmpeg/ffprobe/yt-dlp יימצאו גם כשאינם ב-PATH.
		os.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	}
	_ = os.MkdirAll(stateDir, 0o755)

	y := &yemot{client: &http.Client{Timeout: 30 * time.Second}, apiKey: apiKey}

	name, vids, err := c14PageVideos(playlist)
	if err != nil {
		log.Fatalf("קריאת רשימת ההשמעה ביוטיוב נכשלה: %v", err)
	}
	log.Printf("רשימת ההשמעה %q: %d סרטונים.", name, len(vids))

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

	now := time.Now().In(loc)
	done, skipped, failed := 0, 0, 0
	seen := map[string]bool{}
	for _, v := range vids {
		if !strings.Contains(v.title, c14Full) {
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
		showName := v.title
		if i := strings.IndexByte(showName, '|'); i > 0 {
			showName = strings.TrimSpace(showName[:i])
		}
		if err := patriotsUpgrade(y, ext, ytdlp, v.id, ep.base, showName, start, loc); err != nil {
			log.Printf("%s: ההחלפה בגרסה המוכנה נכשלה (נשארת ההקלטה): %v", day, err)
			failed++
			continue
		}
		_ = os.WriteFile(marker, []byte(time.Now().Format(time.RFC3339)+"\n"+v.id+"\n"), 0o644)
		log.Printf("%s: הפרק הוחלף בגרסה המוכנה מיוטיוב (שלוחה %s, קובץ %s).", day, ext, audioFile(ep.base))
		done++
	}
	log.Printf("סיום: %d הוחלפו, %d דילוגים, %d נכשלו.", done, skipped, failed)
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
	if b := strings.TrimSpace(os.Getenv("YT_COOKIES_BROWSER")); b != "" {
		args = append(args, "--cookies-from-browser", b)
	}
	args = append(args, "https://www.youtube.com/watch?v="+videoID)
	cmd := exec.CommandContext(ctx, ytdlp, args...)
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
