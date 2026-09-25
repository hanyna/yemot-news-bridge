package main

// קול מוכן מראש: כל הודעה חדשה עולה גם כקובץ שמע (NNNNN.wav, באותו מספר של
// ההקראה NNNNN.tts). ימות המשיח משמיעים קובץ wav במקום קובץ tts באותו שם — ככה
// המאזין לא מחכה להמרת הטקסט לדיבור בזמן ההאזנה (זה מה שגרם להמתנה בין הודעות
// ולגמגום בתחילת השמעה).
//
// הקול נוצר ב-Gemini (אותו מפתח GEMINI_API_KEY של ניתוח התמונות), ברקע —
// ההודעה עצמה עולה מיד כטקסט, והקול מחליף אותה כשהוא מוכן (שניות ספורות).
// כל תקלה (מכסה, עומס, אין ffmpeg) — ההודעה פשוט נשארת כטקסט, כמו קודם.
//
// חיסכון במכסה: בחינם Google נותנים רק 10 קבצי קול ביום לכל מודל. לכן:
//   - קובץ קול אחד לכל הודעה — אותו קובץ עולה לשלוחה 1 ולשלוחת הכתב (עם שם הכתב).
//   - בקול, הזמן נאמר כיום בשבוע ("ביום שישי בשעה 8 בבוקר") ולא "היום"/"אתמול" —
//     ככה הקול לא צריך להיווצר מחדש בכל חצות. אחרי שבוע (כשהניסוח עובר לתאריך)
//     הקול נמחק, וההודעה הישנה מושמעת כטקסט.
//   - הודעות ישנות לא מקבלות קול בדיעבד; רק הודעות חדשות (ואחרי הפעלה מחדש —
//     הודעות מהשעות האחרונות שעוד לא קיבלו).
//   - הודעה ארוכה: הטקסט נחתך (מגבלה של ימות המשיח), אבל הקול מקריא אותה במלואה.

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

// speechModels: לפי הסדר. לכל מודל מכסה חינמית משלו — כשאחד מגיע למכסה, עוברים לבא.
var speechModels = []string{"gemini-3.8-flash-tts", "gemini-3.8-flash-lite-tts", "gemini-3.1-flash-tts-preview", "gemini-2.5-flash-preview-tts"}

var speechEndpoint = "https://generativelanguage.googleapis.com/v1beta/models/%s:generateContent"

const (
	speechTries      = 3                // ניסיונות לכל קובץ
	speechDayPause   = time.Hour        // מודל שהגיע למכסה היומית — לא מנסים אותו לפני כן
	speechBusyPause  = 30 * time.Second // כל המודלים עמוסים — הפסקה קצרה
	speechRequeueAge = 6 * time.Hour    // אחרי הפעלה מחדש: הודעות מהשעות האחרונות בלי קול — חוזרות לתור
	speechRequeueMax = 20               // כמה לכל היותר בכל שלוחה
	speechTimeout    = 5 * time.Minute  // בקשה אחת (הודעה ארוכה במלואה לוקחת כמה דקות)
)

// speechFile: קובץ הקול של ההקראה (אותו מספר כמו introFile, בסיומת wav).
func speechFile(base int) string { return fmt.Sprintf("%05d.wav", base+1) }

// fullFile: טקסט מלא של הודעה ארוכה — מגרסה קודמת (כבר לא נכתב; נמחק עם ההודעה).
// לא NNNNN.txt — את השם הזה ימות המשיח תופסים לבד: בכל העלאת קובץ קול הם
// כותבים לידו קובץ פרטים באותו שם (API-DID-...title=...).
func fullFile(base int) string { return fmt.Sprintf("%05d%s", base+1, fullSuffix) }

const fullSuffix = "-full.txt"

// speechMaxChars: עד כמה תווים הקול מקריא (הודעה ארוכה מזה — נחתכת גם בקול).
const speechMaxChars = 4000

// audioItem: טקסט הקול של הודעה — הזמן כיום בשבוע, כדי שלא יתיישן בחצות.
func audioItem(it FeedItem, titles map[string]string, loc *time.Location, withName bool) string {
	t := time.Unix(it.TS, 0).In(loc)
	head := itemHead(it.Channel, t, whenThisWeek, titles, withName)
	body := it.Text
	const cutNote = " המשך ההודעה לא הוקרא."
	room := speechMaxChars - len([]rune(head)) - len([]rune(cutNote))
	if r := []rune(body); len(r) > room {
		body = cutAtSentence(r[:room]) + cutNote
	}
	return head + body
}

// speechTarget: לאן קובץ הקול עולה.
type speechTarget struct {
	ext, file string
	base      int // הודעה בארכיון; -1: תפריט / כותרת
}

type speechJob struct {
	key       string
	targets   []speechTarget
	done      map[speechTarget]bool
	menu      bool // תפריט או כותרת — לפני הכול (זה הדבר הראשון שהמאזין שומע)
	named     bool // הטקסט כולל את שם הכתב (משלוחה 1)
	voice     string
	text      string
	ts        int64
	data      []byte // הקול שכבר נוצר (אם נשארו יעדים שעוד לא עלה אליהם)
	held      bool   // נוסף בסבב הנוכחי — יוצא לעבודה בסוף הסבב (speechTick)
	running   bool
	tries     int
	notBefore time.Time
}

type speechResult struct {
	ext, key string
	base     int
}

type speaker struct {
	key, voice string
	menuVoice  string // קול התפריטים והכותרות (SPEECH_MENU_VOICE)
	client     *http.Client

	mu         sync.Mutex
	jobs       map[string]*speechJob
	results    []speechResult
	modelPause map[int]time.Time
	pausedTil  time.Time
	bg         bool
	warned     map[string]bool
}

// newSpeaker: nil כשאין מפתח או ש-SPEECH=off.
func newSpeaker(key, voice, mode string) *speaker {
	return newSpeakerVoices(key, voice, "", mode)
}

// newSpeakerVoices: כמו newSpeaker, עם קול נפרד לתפריטים (ריק = Puck).
func newSpeakerVoices(key, voice, menuVoice, mode string) *speaker {
	key = cleanKey(key)
	if key == "" || strings.EqualFold(strings.TrimSpace(mode), "off") {
		return nil
	}
	if voice = strings.TrimSpace(voice); voice == "" {
		voice = "Charon"
	}
	if menuVoice = strings.TrimSpace(menuVoice); menuVoice == "" {
		menuVoice = "Puck"
	}
	return &speaker{key: key, voice: voice, menuVoice: menuVoice, client: &http.Client{Timeout: speechTimeout},
		jobs: map[string]*speechJob{}, modelPause: map[int]time.Time{}, warned: map[string]bool{}}
}

// add: קול להודעה בשלוחה. אותה הודעה בכמה שלוחות (1 ושלוחת הכתב) — קובץ קול
// אחד, שעולה לכולן. הטקסט עם שם הכתב (withName, משלוחה 1) עדיף.
func (s *speaker) add(key string, ts int64, ext string, base int, text string, withName bool) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	t := speechTarget{ext, speechFile(base), base}
	k := "m|" + key
	if j, ok := s.jobs[k]; ok {
		if !j.has(t) {
			j.targets = append(j.targets, t)
		}
		if withName && !j.named && !j.running && j.data == nil {
			j.text, j.named = text, true
		}
		return
	}
	s.jobs[k] = &speechJob{key: key, targets: []speechTarget{t}, done: map[speechTarget]bool{}, named: withName,
		voice: s.voice, text: text, ts: ts, held: true}
}

// addMenu: קול לתפריט / כותרת (M1000.wav, 99999.wav) — בעדיפות ראשונה, בקול התפריטים.
// טקסט חדש לאותו קובץ מחליף את הישן.
func (s *speaker) addMenu(ext, file, text string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	k := "f|" + ext + "|" + file
	if j, ok := s.jobs[k]; ok {
		// אם היא רצה עכשיו — ה-worker רואה שהטקסט השתנה, לא מעלה, ומנסה שוב עם החדש.
		j.text, j.data, j.tries, j.notBefore = text, nil, 0, time.Time{}
		j.done = map[speechTarget]bool{}
		return
	}
	s.jobs[k] = &speechJob{key: k, targets: []speechTarget{{ext, file, -1}}, done: map[speechTarget]bool{}, menu: true,
		voice: s.menuVoice, text: text}
}

func (j *speechJob) has(t speechTarget) bool {
	for _, x := range j.targets {
		if x == t {
			return true
		}
	}
	return false
}

func (s *speaker) warnOnce(key, msg string) {
	s.mu.Lock()
	first := !s.warned[key]
	s.warned[key] = true
	s.mu.Unlock()
	if first {
		log.Println(msg)
	}
}

// next: העבודה הבאה — תפריטים קודם, ואז ההודעה החדשה ביותר.
func (s *speaker) next() (*speechJob, speechJob) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	var best *speechJob
	for _, j := range s.jobs {
		if j.running || j.held || now.Before(j.notBefore) {
			continue
		}
		if j.data == nil && now.Before(s.pausedTil) {
			continue // צריך ליצור קול — ויצירת הקול מושהית (מכסה)
		}
		if best == nil || (j.menu && !best.menu) || (j.menu == best.menu && j.ts > best.ts) {
			best = j
		}
	}
	if best == nil {
		return nil, speechJob{}
	}
	best.running = true
	cp := *best
	cp.targets = append([]speechTarget(nil), best.targets...)
	return best, cp
}

// step: עבודה אחת. false כשאין כרגע מה לעשות.
func (s *speaker) step(cfg *config) bool {
	j, job := s.next()
	if j == nil {
		return false
	}
	if !haveFFmpeg() {
		s.mu.Lock()
		j.running, s.pausedTil = false, time.Now().Add(time.Hour)
		s.mu.Unlock()
		s.warnOnce("ffmpeg", "הערה: ffmpeg לא מותקן — ההודעות נשארות כטקסט (בלי קול מוכן מראש).")
		return false
	}
	start := time.Now()
	data, model, err := job.data, "", error(nil)
	if data == nil {
		data, model, err = s.synthesize(job.text, job.voice)
		if err == nil {
			data, err = prepareSpeech(data)
		}
	}
	s.mu.Lock()
	changed := j.text != job.text // הטקסט עודכן בזמן שעבדנו — הקול כבר לא מתאים
	s.mu.Unlock()
	var uploaded []speechTarget
	if err == nil && !changed {
		for _, t := range job.targets {
			if job.done[t] {
				continue
			}
			if e := cfg.y.uploadFile(t.ext, t.file, "speech.wav", data, true); e != nil {
				err = e
				continue
			}
			uploaded = append(uploaded, t)
		}
	}
	var busy *speechBusyErr
	s.mu.Lock()
	defer s.mu.Unlock()
	j.running = false
	for _, t := range uploaded {
		j.done[t] = true
		s.results = append(s.results, speechResult{t.ext, job.key, t.base})
	}
	if len(uploaded) > 0 {
		log.Printf("קול מוכן: %s → %d שלוחות (%s, %s, %v).", job.key, len(uploaded), model, job.voice, time.Since(start).Round(100*time.Millisecond))
	}
	allDone := true
	for _, t := range j.targets {
		if !j.done[t] {
			allDone = false
		}
	}
	switch {
	case changed:
		// נשאר בתור עם הטקסט החדש
	case err == nil && allDone:
		delete(s.jobs, s.jobKeyOf(j))
	case err == nil:
		j.data = data // נוספה שלוחה בזמן שעבדנו — הקול כבר מוכן, רק להעלות
	case errors.As(err, &busy):
		// מכסה / עומס בכל המודלים — לא אשמת הקובץ; לא סופרים ניסיון
		s.pausedTil = time.Now().Add(busy.wait)
		if !s.warned["busy"] {
			s.warned["busy"] = true
			log.Printf("הערה: יצירת קול מושהית ל-%v (%v). בינתיים ההודעות מושמעות כטקסט.", busy.wait, err)
		}
	default:
		if data != nil && len(uploaded) > 0 {
			j.data = data
		}
		j.tries++
		if j.tries >= speechTries {
			delete(s.jobs, s.jobKeyOf(j))
			log.Printf("הערה: הקול של %s לא עלה (נשאר טקסט): %v", job.key, err)
		} else {
			j.notBefore = time.Now().Add(time.Duration(j.tries) * time.Minute)
		}
	}
	return true
}

// jobKeyOf (עם s.mu): המפתח של העבודה במפה.
func (s *speaker) jobKeyOf(j *speechJob) string {
	for k, x := range s.jobs {
		if x == j {
			return k
		}
	}
	return ""
}

// start: ברקע, לכל אורך ההפעלה.
func (s *speaker) start(cfg *config) {
	if s == nil {
		return
	}
	s.bg = true
	go func() {
		for {
			if !s.step(cfg) {
				time.Sleep(2 * time.Second)
			}
		}
	}()
}

type speechBusyErr struct {
	wait time.Duration
	why  string
}

func (e *speechBusyErr) Error() string { return e.why }

// synthesize: טקסט → WAV (כפי ש-Gemini מחזיר). מנסה את המודלים לפי הסדר.
func (s *speaker) synthesize(text, voice string) ([]byte, string, error) {
	if voice == "" {
		voice = s.voice
	}
	body, _ := json.Marshal(map[string]any{
		// בלי הוראות לפני הטקסט — המודל מקריא אותן בקול.
		"contents": []any{map[string]any{"parts": []any{map[string]any{"text": text}}}},
		"generationConfig": map[string]any{
			"responseModalities": []string{"AUDIO"},
			"speechConfig":       map[string]any{"voiceConfig": map[string]any{"prebuiltVoiceConfig": map[string]any{"voiceName": voice}}},
		},
	})
	var lastErr error
	var soonest time.Time // מתי המודל הראשון שבמכסה משתחרר
	allQuota := true
	for m, model := range speechModels {
		s.mu.Lock()
		paused := time.Now().Before(s.modelPause[m])
		s.mu.Unlock()
		if paused {
			s.mu.Lock()
			if until := s.modelPause[m]; soonest.IsZero() || until.Before(soonest) {
				soonest = until
			}
			s.mu.Unlock()
			continue
		}
		data, status, err := s.call(model, body)
		if err == nil {
			return data, model, nil
		}
		lastErr = err
		switch {
		case status == http.StatusTooManyRequests:
			// מגבלה לדקה — ממתינים כמה ש-Google מבקשים (בד"כ פחות מדקה); מכסה יומית — שעה.
			wait := time.Minute
			var q *quotaErr
			if errors.As(err, &q) && q.wait > 0 {
				wait = q.wait
			}
			until := time.Now().Add(wait)
			s.mu.Lock()
			s.modelPause[m] = until
			s.mu.Unlock()
			if soonest.IsZero() || until.Before(soonest) {
				soonest = until
			}
		case status == 0, status == http.StatusNotFound, status == http.StatusBadRequest, overloaded(status):
			allQuota = false // המודל איטי / עמוס / לא זמין — המודל הבא
		default:
			return nil, "", err // תקלת מפתח וכו' — ננסה שוב אחר כך
		}
	}
	if lastErr == nil {
		lastErr = errors.New("כל המודלים הגיעו למכסה")
	}
	wait := speechBusyPause
	if allQuota && !soonest.IsZero() {
		wait = time.Until(soonest) + time.Second
	}
	if wait < 5*time.Second {
		wait = 5 * time.Second
	}
	return nil, "", &speechBusyErr{wait, lastErr.Error()}
}

func (s *speaker) call(model string, body []byte) ([]byte, int, error) {
	u := fmt.Sprintf(speechEndpoint, url.PathEscape(model))
	req, err := http.NewRequest(http.MethodPost, u, bytes.NewReader(body))
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-goog-api-key", s.key)
	resp, err := s.client.Do(req)
	if err != nil {
		return nil, 0, errors.New(strings.ReplaceAll(err.Error(), s.key, "***"))
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	if resp.StatusCode != http.StatusOK {
		msg := string(raw)
		var e struct {
			Error struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		if json.Unmarshal(raw, &e) == nil && e.Error.Message != "" {
			msg = e.Error.Message
		}
		if len(msg) > 160 {
			msg = msg[:160]
		}
		err := fmt.Errorf("%s: HTTP %d: %s", model, resp.StatusCode, msg)
		if resp.StatusCode == http.StatusTooManyRequests {
			return nil, resp.StatusCode, &quotaErr{err, retryAfter(raw)}
		}
		return nil, resp.StatusCode, err
	}
	var r struct {
		Candidates []struct {
			Content struct {
				Parts []struct {
					InlineData struct {
						MimeType string `json:"mimeType"`
						Data     string `json:"data"`
					} `json:"inlineData"`
				} `json:"parts"`
			} `json:"content"`
		} `json:"candidates"`
	}
	if err := json.Unmarshal(raw, &r); err != nil {
		return nil, resp.StatusCode, fmt.Errorf("%s: תשובה לא תקינה: %v", model, err)
	}
	for _, c := range r.Candidates {
		for _, p := range c.Content.Parts {
			if p.InlineData.Data == "" {
				continue
			}
			data, err := base64.StdEncoding.DecodeString(p.InlineData.Data)
			if err != nil {
				return nil, resp.StatusCode, err
			}
			return asWav(data, p.InlineData.MimeType), resp.StatusCode, nil
		}
	}
	return nil, http.StatusBadRequest, fmt.Errorf("%s: בלי קול בתשובה", model)
}

// asWav: Gemini מחזיר WAV, או PCM גולמי (audio/L16; rate=24000) — שעוטפים בכותרת WAV.
func asWav(data []byte, mime string) []byte {
	if len(data) >= 12 && string(data[:4]) == "RIFF" && string(data[8:12]) == "WAVE" {
		return data
	}
	rate := 24000
	if i := strings.Index(strings.ToLower(mime), "rate="); i >= 0 {
		fmt.Sscanf(mime[i+5:], "%d", &rate)
	}
	var b bytes.Buffer
	w := func(v any) { binary.Write(&b, binary.LittleEndian, v) }
	b.WriteString("RIFF")
	w(uint32(36 + len(data)))
	b.WriteString("WAVEfmt ")
	w(uint32(16))
	w(uint16(1))
	w(uint16(1))
	w(uint32(rate))
	w(uint32(rate * 2))
	w(uint16(2))
	w(uint16(16))
	b.WriteString("data")
	w(uint32(len(data)))
	b.Write(data)
	return b.Bytes()
}

// prepareSpeech: מוריד שקט בהתחלה ובסוף (שלא יהיו הפסקות מיותרות בין הודעות),
// ומכין WAV של טלפון (8000 הרץ, מונו). משתנה — לבדיקות.
var prepareSpeech = func(wav []byte) ([]byte, error) {
	dir, err := os.MkdirTemp("", "speech")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	in, out := filepath.Join(dir, "in.wav"), filepath.Join(dir, "out.wav")
	if err := os.WriteFile(in, wav, 0o644); err != nil {
		return nil, err
	}
	trim := "silenceremove=start_periods=1:start_threshold=-45dB:start_silence=0.1"
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	var buf bytes.Buffer
	cmd := exec.CommandContext(ctx, "ffmpeg", "-hide_banner", "-loglevel", "error", "-nostdin", "-y", "-i", in,
		"-af", trim+",areverse,"+trim+",areverse", "-ar", "8000", "-ac", "1", "-sample_fmt", "s16", out)
	cmd.Stdout, cmd.Stderr = &buf, &buf
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("ffmpeg: %v: %.200s", err, strings.TrimSpace(buf.String()))
	}
	data, err := os.ReadFile(out)
	if err != nil {
		return nil, err
	}
	if len(data) < 8000 { // פחות מחצי שנייה — משהו השתבש
		return nil, errors.New("קובץ קול ריק")
	}
	return data, nil
}

// speechTick — בסוף כל סבב: משחרר לעבודה את מה שנוסף, ורושם בארכיונים את מה שהסתיים.
func (st *state) speechTick(cfg *config) {
	s := cfg.speech
	if s == nil {
		return
	}
	s.mu.Lock()
	for _, j := range s.jobs {
		j.held = false
	}
	s.mu.Unlock()
	if !s.bg {
		for s.step(cfg) {
		}
	}
	s.mu.Lock()
	res := s.results
	s.results = nil
	s.mu.Unlock()
	for _, r := range res {
		a, ok := st.arch[r.ext]
		if !ok || r.base < 0 {
			continue
		}
		e := a.entries[r.key]
		if e == nil || e.base != r.base {
			// ההודעה כבר לא בשלוחה (נמחקה בינתיים) — גם הקול שעלה
			_ = cfg.y.remove([]string{ivrPath(r.ext, speechFile(r.base))})
			continue
		}
		a.addFile(speechFile(r.base))
		if !e.voiced {
			e.voiced, a.dirty = true, true
		}
	}
}

// hasBase: האם יש בשלוחה הודעה במספר הזה.
func (a *archive) hasBase(base int) bool {
	for _, e := range a.entries {
		if e.base == base {
			return true
		}
	}
	return false
}

// hasFile: האם הקובץ ברשימת קבצי השלוחה.
func (a *archive) hasFile(name string) bool {
	for _, n := range a.files {
		if strings.EqualFold(n, name) {
			return true
		}
	}
	return false
}

// dropFile מוחק קובץ מהשלוחה ומהרשימה.
func (a *archive) dropFile(cfg *config, name string) error {
	if err := cfg.y.remove([]string{ivrPath(a.ext, name)}); err != nil {
		return err
	}
	kept := a.files[:0]
	for _, n := range a.files {
		if !strings.EqualFold(n, name) {
			kept = append(kept, n)
		}
	}
	a.files = kept
	return nil
}

// requeueSpeech: אחרי הפעלה מחדש (התור נשמר רק בזיכרון) — הודעות מהשעות האחרונות
// שעדיין בלי קול חוזרות לתור (הטקסט מההודעה שבשרת). פעם אחת לכל שלוחה בכל הפעלה.
func (a *archive) requeueSpeech(cfg *config, items []FeedItem, titles map[string]string, now time.Time) {
	if cfg.speech == nil || a.spoken {
		return
	}
	a.spoken = true
	n := 0
	for _, it := range items {
		e := a.entries[itemKey(it)]
		if e == nil || e.base < 0 || n >= speechRequeueMax || now.Sub(time.Unix(e.ts, 0)) > speechRequeueAge ||
			a.hasFile(speechFile(e.base)) || !a.hasFile(introFile(e.base)) {
			continue
		}
		cfg.speech.add(e.key, e.ts, a.ext, e.base, audioItem(it, titles, cfg.loc, a.withName), a.withName)
		n++
	}
	if n > 0 {
		log.Printf("שלוחה %s: %d הודעות מהשעות האחרונות בלי קול מוכן — נכנסו לתור.", a.ext, n)
	}
}

// uploadSpoken: מעלה קובץ טקסט של תפריט / כותרת (M1000.tts, 99999.tts), ומחליף
// גם את קובץ הקול שלו. קול ישן אומר את הטקסט הישן — נמחק מיד (עד שהחדש מוכן
// מושמע הטקסט). טקסט שלא השתנה, ויש לו כבר קול — לא נוגעים.
func uploadSpoken(cfg *config, ext, name, text string) error {
	if cfg.speech == nil {
		return cfg.y.upload(ext, name, text)
	}
	wav := strings.TrimSuffix(name, ".tts") + ".wav"
	hasWav := false
	if info, err := cfg.y.dir(ext); err == nil {
		hasWav = hasName(info.Files, wav)
	}
	if hasWav {
		if old, _, err := cfg.y.read(ext, name); err == nil && old == text {
			return nil // בדיוק מה שכבר בשלוחה, עם קול
		}
	}
	if err := cfg.y.upload(ext, name, text); err != nil {
		return err
	}
	if hasWav {
		if err := cfg.y.remove([]string{ivrPath(ext, wav)}); err != nil {
			log.Printf("הערה: מחיקת הקול הישן %s בשלוחה %q נכשלה: %v", wav, ext, err)
		}
	}
	cfg.speech.addMenu(ext, wav, text)
	return nil
}

// quotaErr: ‏429 מ-Google, עם כמה זמן לחכות.
type quotaErr struct {
	err  error
	wait time.Duration
}

func (e *quotaErr) Error() string { return e.err.Error() }

var (
	reRetryDelay = regexp.MustCompile(`"retryDelay":\s*"([\d.]+)s"`)
	reRetryIn    = regexp.MustCompile(`retry in ([\d.]+)\s*s`)
)

// retryAfter: כמה לחכות לפי התשובה של Google. מכסה יומית (PerDay) — שעה.
func retryAfter(raw []byte) time.Duration {
	s := string(raw)
	if strings.Contains(s, "PerDay") {
		return speechDayPause
	}
	for _, re := range []*regexp.Regexp{reRetryDelay, reRetryIn} {
		if m := re.FindStringSubmatch(s); m != nil {
			var sec float64
			if _, err := fmt.Sscanf(m[1], "%g", &sec); err == nil && sec > 0 {
				return time.Duration(sec*float64(time.Second)) + time.Second
			}
		}
	}
	return time.Minute
}
