package main

// קול של סרטונים והודעות קוליות: מורידים, מחלצים את הקול (ffmpeg), ומעלים
// לשלוחה מיד אחרי ההקראה של ההודעה (קובץ b.wav — נשמע אחרי b+1.tts).
//
//   - סרטון (רגיל או עגול): דרך השרת של ערוץ חי (/api/media) — הוא יודע לחדש
//     את הכתובות של טלגרם שפגות אחרי זמן קצר.
//   - הודעה קולית: ישירות מהכתובת שבהודעה.
//   - נשארים עם התיאור בלבד: סרטון ארוך שטלגרם לא נותנים בלי חשבון (בשרת הוא
//     מוצג כתמונה עם כפתור הפעלה), GIF (בלי קול), סרטון ארוך מ-AUDIO_MAX_MINUTES,
//     והודעה ישנה מיממה.
//
// העבודה נעשית ברקע (audioWorker, ב-goroutine נפרד): ההקראה של הודעה עולה
// מיד, והקול מצטרף כשהוא מוכן — הקראות של הודעות חדשות לא מחכות לו. החדשות
// קודם. בין סרטון לסרטון יש הפסקה: השרת של ערוץ חי מביא אותם מטלגרם מאותה
// כתובת שממנה הוא סורק את הערוצים, ועומס עליה עלול לעכב את כל החדשות.

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"html"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	audioTries    = 4                // ניסיונות עד שמוותרים על קובץ
	mediaMaxBytes = 200 << 20        // סרטון גדול מזה — לא מורידים
	aclRetry      = 10 * time.Minute // אין הרשאה ל-UploadFile — בודקים שוב אחרי
	mediaTimeout  = 4 * time.Minute
	audioMaxAge   = 24 * time.Hour // הודעה ישנה מזה — רק התיאור (הייבוא הראשון מביא 48 שעות)
)

// משתנים — כדי שבדיקות יוכלו לקצר אותם.
var (
	// videoGap: הפסקה בין סרטון לסרטון (העומס על השרת של ערוץ חי מול טלגרם).
	videoGap = 30 * time.Second
	// retryBackoff: סרטון שהשרת עוד לא הצליח להביא (טלגרם מסיימים לעבד סרטון
	// חדש תוך כמה דקות, והשרת מנסה שוב בעצמו) — מתי לבקש שוב.
	retryBackoff = []time.Duration{5 * time.Minute, 20 * time.Minute, time.Hour}
)

const uploadACLNote = "חסרה הרשאה: מפתח ה-API לא מורשה ל-UploadFile, ולכן הגשר לא יכול להעלות את הקול של סרטונים והודעות קוליות. " +
	"צריך להוסיף /api/UploadFile לרשימת ההרשאות של המפתח באתר ימות המשיח. ההקראות ממשיכות כרגיל; הקול ממתין ויעלה כשתתווסף ההרשאה."

type audioTarget struct {
	ext  string
	base int
}

type audioJob struct {
	key       string // ערוץ/מזהה
	kind      string // "v" סרטון, "o" הודעה קולית
	src       string // הודעה קולית: כתובת הקובץ
	channel   string
	id        int
	ts        int64
	targets   []audioTarget
	tries     int
	notBefore time.Time
	held      bool // נוסף בסבב הנוכחי — ממתין לסופו, כדי שכל השלוחות של ההודעה (1 ו-2/x) יקבלו אותה הורדה
	running   bool
}

// audioResult: עבודה שהסתיימה — לעדכון האינדקס (בלולאה הראשית).
type audioResult struct {
	key     string
	targets []audioTarget
	audio   int
}

type audioWorker struct {
	mu        sync.Mutex
	queue     []*audioJob
	results   []audioResult
	pausedTil time.Time
	permOK    bool // נבדק שיש הרשאה ל-UploadFile (לפני ההורדה הראשונה)
	lastVideo time.Time
	warned    map[string]bool
	bg        bool // רץ ב-goroutine משלו (בבדיקות — בתוך הסבב, audioTick)
}

func newAudioWorker() *audioWorker { return &audioWorker{warned: map[string]bool{}} }

// warnOnce רושם בלוג הודעת הסבר — פעם אחת בכל הפעלה. (לקרוא בלי w.mu.)
func (w *audioWorker) warnOnce(key, msg string) {
	w.mu.Lock()
	first := !w.warned[key]
	w.warned[key] = true
	w.mu.Unlock()
	if first {
		log.Println(msg)
	}
}

func (w *audioWorker) warnedFor(key string) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.warned[key]
}

// pending: כמה עבודות בתור (כולל אחת שרצה).
func (w *audioWorker) pending() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return len(w.queue)
}

// add מכניס לתור. אותה הודעה בכמה שלוחות (1 ו-2/x) — הורדה אחת, כמה העלאות.
func (w *audioWorker) add(j *audioJob, t audioTarget) {
	w.mu.Lock()
	defer w.mu.Unlock()
	for _, q := range w.queue {
		if q.key != j.key {
			continue
		}
		for _, x := range q.targets {
			if x == t {
				return
			}
		}
		if !q.running {
			q.targets = append(q.targets, t)
			return
		}
	}
	j.targets, j.held = []audioTarget{t}, true
	w.queue = append(w.queue, j)
}

// release: סוף סבב — מה שנוסף בו מוכן לעבודה.
func (w *audioWorker) release() {
	w.mu.Lock()
	defer w.mu.Unlock()
	for _, q := range w.queue {
		q.held = false
	}
}

// next בוחר את העבודה הבאה — החדשה ביותר מבין המוכנות — ומסמן שהיא רצה.
// מחזיר גם עותק, שה-worker עובד עליו בלי נעילה. inline (בדיקות): בלי ההפסקה
// בין סרטונים.
func (w *audioWorker) next(inline bool) (*audioJob, audioJob) {
	w.mu.Lock()
	defer w.mu.Unlock()
	now := time.Now()
	if now.Before(w.pausedTil) {
		return nil, audioJob{}
	}
	var best *audioJob
	for _, j := range w.queue {
		if j.running || j.held || now.Before(j.notBefore) {
			continue
		}
		if !inline && j.kind == "v" && now.Sub(w.lastVideo) < videoGap {
			continue
		}
		if best == nil || j.ts > best.ts {
			best = j
		}
	}
	if best == nil {
		return nil, audioJob{}
	}
	best.running = true
	cp := *best
	cp.targets = append([]audioTarget(nil), best.targets...)
	return best, cp
}

// finish: העבודה הסתיימה (הקול עלה, או שוויתרנו) — התוצאה לאינדקס, והעבודה
// יוצאת מהתור. (עם w.mu.)
func (w *audioWorker) finish(j *audioJob, audio int) {
	w.results = append(w.results, audioResult{key: j.key, targets: append([]audioTarget(nil), j.targets...), audio: audio})
	for i, q := range w.queue {
		if q == j {
			w.queue = append(w.queue[:i], w.queue[i+1:]...)
			break
		}
	}
}

// start מפעיל את ה-worker ברקע, לכל אורך ההפעלה.
func (w *audioWorker) start(cfg *config) {
	w.bg = true
	go func() {
		for {
			if !w.step(cfg, false) {
				time.Sleep(2 * time.Second)
			}
		}
	}()
}

// runReady: כל מה שמוכן עכשיו, ברצף (בבדיקות, במקום ה-goroutine).
func (w *audioWorker) runReady(cfg *config) {
	for w.step(cfg, true) {
	}
}

// step: עבודה אחת. מחזיר false כשאין כרגע מה לעשות.
func (w *audioWorker) step(cfg *config, inline bool) bool {
	j, job := w.next(inline)
	if j == nil {
		return false
	}
	if !haveFFmpeg() {
		w.mu.Lock()
		j.running, w.pausedTil = false, time.Now().Add(time.Hour)
		w.mu.Unlock()
		w.warnOnce("ffmpeg", "הערה: ffmpeg לא מותקן — הקול של סרטונים והודעות קוליות לא יועלה (ההקראות עצמן כן).")
		return false
	}
	w.mu.Lock()
	permOK := w.permOK
	w.mu.Unlock()
	if !permOK {
		// קודם בודקים שמותר להעלות — בלי הרשאה אין טעם להוריד (ולהעמיס על השרת).
		err := cfg.y.uploadProbe()
		var aclErr *aclError
		isACL := errors.As(err, &aclErr)
		w.mu.Lock()
		j.running = false
		switch {
		case isACL:
			w.pausedTil = time.Now().Add(aclRetry)
		case err != nil:
			w.pausedTil = time.Now().Add(time.Minute)
		default:
			w.permOK = true
		}
		w.mu.Unlock()
		if isACL {
			w.warnOnce("UploadFile", uploadACLNote)
		} else if err != nil {
			log.Printf("הערה: בדיקת ההרשאה ל-UploadFile נכשלה — ננסה שוב בעוד דקה: %v", err)
		}
		return err == nil
	}

	err := runAudioJob(cfg, &job)
	now := time.Now()
	var aclErr *aclError
	var perm *permanentError
	var later *retryLaterError
	isACL := errors.As(err, &aclErr)
	note := ""
	w.mu.Lock()
	j.running = false
	if job.kind == "v" {
		w.lastVideo = now
	}
	switch {
	case err == nil:
		w.finish(j, audioDone)
		note = fmt.Sprintf("קול הועלה: %s (%s) ← %s", job.key, kindName(job.kind), targetsText(job.targets))
	case isACL:
		// לא אשמת הקובץ — ההרשאה חסרה. לא סופרים ניסיון; בודקים שוב מאוחר יותר.
		w.permOK, w.pausedTil = false, now.Add(aclRetry)
	case errors.As(err, &perm):
		w.finish(j, audioNo)
		note = fmt.Sprintf("הערה: הקול של %s (%s) לא יועלה: %v", job.key, kindName(job.kind), err)
	default:
		j.tries++
		if j.tries >= audioTries {
			w.finish(j, audioNo)
			note = fmt.Sprintf("הערה: הקול של %s (%s) לא יועלה — %d ניסיונות נכשלו: %v", job.key, kindName(job.kind), j.tries, err)
			break
		}
		wait := time.Duration(j.tries) * 3 * time.Minute
		if errors.As(err, &later) && len(retryBackoff) > 0 {
			wait = retryBackoff[min(j.tries, len(retryBackoff))-1]
		}
		j.notBefore = now.Add(wait)
		note = fmt.Sprintf("הערה: הקול של %s (%s) נכשל (ניסיון %d) — ננסה שוב בעוד %v: %v", job.key, kindName(job.kind), j.tries, wait, err)
	}
	w.mu.Unlock()
	if isACL {
		w.warnOnce("UploadFile", uploadACLNote)
	}
	if note != "" {
		log.Print(note)
	}
	return true
}

var (
	reAudioSrc = regexp.MustCompile(`<audio[^>]*\ssrc="([^"]+)"`)
	reVoiceDur = regexp.MustCompile(`class="voicedur">([^<]*)<`)
)

// audioSource: האם יש בהודעה קול להשמיע, מאיזה סוג, ומאיפה.
func audioSource(h string) (kind, src string, secs int, ok bool) {
	switch {
	case strings.Contains(h, `class="voicebox"`):
		m := reAudioSrc.FindStringSubmatch(h)
		if m == nil {
			return "", "", 0, false
		}
		if d := reVoiceDur.FindStringSubmatch(h); d != nil {
			secs = durationSecs(html.UnescapeString(d[1]))
		}
		return "o", html.UnescapeString(m[1]), secs, true
	case strings.Contains(h, `class="roundwrap"`):
		return "v", "", 0, true
	case strings.Contains(h, `class="vidwrap"`) && !strings.Contains(h, `class="gifvid"`):
		// "vidwrap embedwrap" (סרטון ארוך שטלגרם מציגים רק כתמונה) לא נכנס לכאן:
		// בלי חשבון אי אפשר להוריד אותו, והשרת היה מנסה שוב ושוב במשך שעות.
		if m := reDurBadge.FindStringSubmatch(h); m != nil {
			secs = durationSecs(html.UnescapeString(m[1]))
		}
		return "v", "", secs, true
	}
	return "", "", 0, false
}

// durationSecs: "1:05" → 65, "1:02:03" → 3723.
func durationSecs(d string) int {
	secs := 0
	for _, p := range strings.Split(strings.TrimSpace(d), ":") {
		n, err := strconv.Atoi(p)
		if err != nil {
			return 0
		}
		secs = secs*60 + n
	}
	return secs
}

// queueAudio מכניס לתור את הקול של הודעה. מחזיר את סוג המדיה ואת המצב
// ההתחלתי לאינדקס.
func (st *state) queueAudio(cfg *config, it FeedItem, ext string, base int, now time.Time) (media string, audio int) {
	kind, src, secs, ok := audioSource(it.HTML)
	if !ok {
		return "", audioNone
	}
	switch {
	case cfg.audioMax > 0 && secs > cfg.audioMax,
		now.Sub(time.Unix(it.TS, 0)) > audioMaxAge,
		kind == "v" && it.ID <= 0:
		return kind, audioNo
	}
	st.aw.add(&audioJob{key: itemKey(it), kind: kind, src: src, channel: it.Channel, id: it.ID, ts: it.TS},
		audioTarget{ext: ext, base: base})
	return kind, audioPending
}

// queueVideoByKey: סרטון שממתין מהפעלה קודמת וכבר לא ברשימה שהשרת מחזיר —
// מספיקים הערוץ והמזהה (השרת מוצא את הקובץ לבד). false: מפתח לא תקין.
func (st *state) queueVideoByKey(key string, ts int64, ext string, base int) bool {
	i := strings.LastIndex(key, "/")
	if i <= 0 {
		return false
	}
	id, err := strconv.Atoi(key[i+1:])
	if err != nil || id <= 0 {
		return false
	}
	st.aw.add(&audioJob{key: key, kind: "v", channel: key[:i], id: id, ts: ts}, audioTarget{ext: ext, base: base})
	return true
}

// audioTick — בסוף כל סבב, בלולאה הראשית: משחרר לעבודה את מה שנוסף בסבב,
// ומעדכן באינדקסים את מה שה-worker סיים.
func (st *state) audioTick(cfg *config) {
	if !cfg.useWorker() {
		return
	}
	st.aw.release()
	if !st.aw.bg {
		st.aw.runReady(cfg)
	}
	st.aw.mu.Lock()
	res := st.aw.results
	st.aw.results = nil
	st.aw.mu.Unlock()
	for _, r := range res {
		for _, t := range r.targets {
			if p, ok := st.pods[t.ext]; ok {
				p.onAudio(r.key, r.audio) // פרק של פודקאסט (podcast.go)
				continue
			}
			a, ok := st.arch[t.ext]
			var e *archEntry
			if ok {
				e = a.entries[r.key]
			}
			if e == nil || e.base != t.base {
				// ההודעה כבר לא בשלוחה (ערוץ שהוצא מהקו) — קול שעלה בינתיים נמחק.
				if r.audio == audioDone {
					if err := cfg.y.remove([]string{ivrPath(t.ext, audioFile(t.base))}); err != nil {
						log.Printf("הערה: מחיקת %s משלוחה %s נכשלה: %v", audioFile(t.base), t.ext, err)
					}
				}
				continue
			}
			if e.audio != r.audio {
				e.audio, a.dirty = r.audio, true
			}
			if r.audio == audioDone {
				a.addFile(audioFile(t.base)) // עוד קובץ בשלוחה — למגבלת הקבצים
				a.trim(cfg)
			}
		}
	}
}

func kindName(k string) string {
	switch k {
	case "o":
		return "הודעה קולית"
	case "p":
		return "פרק פודקאסט"
	}
	return "סרטון"
}

func targetsText(ts []audioTarget) string {
	var s []string
	for _, t := range ts {
		s = append(s, t.ext+"/"+audioFile(t.base))
	}
	return strings.Join(s, ", ")
}

// permanentError: אין טעם לנסות שוב (הקובץ כבר לא זמין, אין בו קול, גדול מדי).
type permanentError struct{ err error }

func (e *permanentError) Error() string { return e.err.Error() }

// retryLaterError: כדאי לנסות שוב, אבל רק אחרי זמן (retryBackoff).
type retryLaterError struct{ err error }

func (e *retryLaterError) Error() string { return e.err.Error() }

// aclError: למפתח של ימות המשיח אין הרשאה לפעולה.
type aclError struct{ err error }

func (e *aclError) Error() string { return e.err.Error() }

func runAudioJob(cfg *config, j *audioJob) error {
	dir, err := os.MkdirTemp("", "yemot-audio-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	in, out := filepath.Join(dir, "in"), filepath.Join(dir, "out.mp3")
	if err := downloadMedia(cfg, j, in); err != nil {
		return err
	}
	if err := transcode(in, out, cfg.audioMax, j.kind == "p"); err != nil {
		if s := err.Error(); strings.Contains(s, "matches no streams") || strings.Contains(s, "does not contain any stream") {
			return &permanentError{fmt.Errorf("אין קול בקובץ")}
		}
		return err
	}
	data, err := os.ReadFile(out)
	if err != nil {
		return err
	}
	if len(data) == 0 {
		return &permanentError{fmt.Errorf("הקול יצא ריק")}
	}
	for _, t := range j.targets {
		if err := cfg.y.uploadFile(t.ext, audioFile(t.base), "audio.mp3", data, true); err != nil {
			if strings.Contains(err.Error(), "ACL") {
				return &aclError{err}
			}
			return err
		}
	}
	return nil
}

// mediaClient: בלי מגבלת זמן כללית — לכל הורדה יש מגבלה משלה (mediaTimeout / podcastTimeout).
var mediaClient = &http.Client{}

// downloadMedia מוריד את הסרטון (דרך השרת של ערוץ חי), את ההודעה הקולית, או
// פרק של פודקאסט.
func downloadMedia(cfg *config, j *audioJob, path string) error {
	target, maxBytes, timeout := j.src, int64(mediaMaxBytes), mediaTimeout
	if j.kind == "p" {
		maxBytes, timeout = podcastMaxBytes, podcastTimeout
	}
	if j.kind == "v" {
		u, err := url.Parse(cfg.feedURL)
		if err != nil {
			return err
		}
		q := url.Values{}
		q.Set("channel", j.channel)
		q.Set("id", strconv.Itoa(j.id))
		q.Set("k", cfg.feedKey)
		u.Path, u.RawQuery = "/api/media", q.Encode()
		target = u.String()
	}
	if target == "" {
		return &permanentError{fmt.Errorf("אין כתובת להורדה")}
	}
	hide := func(s string) string { // שגיאות רשת כוללות את הכתובת המלאה — עם המפתח
		if cfg.feedKey == "" {
			return s
		}
		return strings.ReplaceAll(strings.ReplaceAll(s, url.QueryEscape(cfg.feedKey), "***"), cfg.feedKey, "***")
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return errors.New(hide(err.Error()))
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0 Safari/537.36")
	resp, err := mediaClient.Do(req)
	if err != nil {
		return errors.New(hide(err.Error()))
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusGone:
		switch j.kind {
		case "v": // השרת לא הצליח (עדיין) להביא את הסרטון מטלגרם — לרוב זמני.
			return &retryLaterError{fmt.Errorf("השרת לא הביא את הסרטון (סטטוס %d)", resp.StatusCode)}
		case "p": // פרק שעוד לא עלה לשרת של הפודקאסט, או תקלה אצלם
			return &retryLaterError{fmt.Errorf("הפרק לא זמין כרגע (סטטוס %d)", resp.StatusCode)}
		}
		// הודעה קולית: הכתובת של טלגרם פגה, ואין דרך לחדש אותה.
		return &permanentError{fmt.Errorf("הקובץ כבר לא זמין (סטטוס %d)", resp.StatusCode)}
	case resp.StatusCode >= 300:
		return fmt.Errorf("סטטוס %d בהורדה", resp.StatusCode)
	case resp.ContentLength > maxBytes:
		return &permanentError{fmt.Errorf("הקובץ גדול מדי (%d MB)", resp.ContentLength>>20)}
	}
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	n, err := io.Copy(f, io.LimitReader(resp.Body, maxBytes+1))
	if err != nil {
		return fmt.Errorf("הורדה נקטעה: %s", hide(err.Error()))
	}
	if n > maxBytes {
		return &permanentError{fmt.Errorf("הקובץ גדול מדי")}
	}
	if n == 0 {
		return fmt.Errorf("הורד קובץ ריק")
	}
	return nil
}

// haveFFmpeg — משתנה, כדי שבדיקות לא יהיו תלויות בהתקנה.
var haveFFmpeg = sync.OnceValue(func() bool {
	_, err := exec.LookPath("ffmpeg")
	return err == nil
})

// transcode מחלץ את הקול ל-MP3 מונו. ימות המשיח ממירים אותו לפורמט של טלפון
// בזמן ההעלאה (convertAudio=1). סרטון / הודעה קולית: עוצמה אחידה (loudnorm).
// פרק של פודקאסט (ארוך, וכבר מעובד): בלי loudnorm — מהיר — ובקצב נמוך יותר,
// כדי שגם פרק של שעתיים יעבור את מגבלת ההעלאה של ימות. משתנה — לבדיקות.
var transcode = func(in, out string, maxSecs int, podcast bool) error {
	timeout := 3 * time.Minute
	args := []string{"-hide_banner", "-loglevel", "error", "-nostdin", "-y", "-i", in, "-vn", "-map", "0:a:0", "-ac", "1"}
	if podcast {
		timeout = 15 * time.Minute
		args = append(args, "-ar", "16000", "-b:a", "32k", "-t", strconv.Itoa(podcastMaxSecs))
	} else {
		args = append(args, "-ar", "22050", "-af", "loudnorm=I=-16:TP=-1.5:LRA=11", "-b:a", "48k")
		if maxSecs > 0 {
			args = append(args, "-t", strconv.Itoa(maxSecs))
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	args = append(args, out)
	var outBuf bytes.Buffer
	cmd := exec.CommandContext(ctx, "ffmpeg", args...)
	cmd.Stdout, cmd.Stderr = &outBuf, &outBuf
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("ffmpeg: %v: %.300s", err, strings.TrimSpace(outBuf.String()))
	}
	return nil
}
