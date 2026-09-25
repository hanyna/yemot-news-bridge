package main

// קול מוכן מראש: כל הודעה בשלוחה עולה גם כקובץ שמע (NNNNN.wav, באותו מספר
// של ההקראה NNNNN.tts). ימות המשיח משמיעים קובץ wav במקום קובץ tts באותו שם
// — ככה המאזין לא מחכה להמרת הטקסט לדיבור בזמן ההאזנה (זה מה שגרם להמתנה בין
// הודעות ולגמגום בתחילת השמעה).
//
// הקול נוצר ב-Gemini (אותו מפתח GEMINI_API_KEY של ניתוח התמונות), ברקע —
// ההודעה עצמה עולה מיד כטקסט, והקול מחליף אותה כשהוא מוכן (שניות ספורות).
// כל תקלה (מכסה, עומס, אין ffmpeg) — ההודעה פשוט נשארת כטקסט, כמו קודם.
//
// כשניסוח הזמן בהודעה משתנה ("היום" ← "אתמול"), הקול הישן נמחק מיד (כדי שלא
// יושמע ניסוח לא נכון) ונוצר מחדש בעדיפות נמוכה, אחרי הודעות חדשות.

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
	"strings"
	"sync"
	"time"
)

// speechModels: לפי הסדר. לכל מודל מכסה חינמית משלו — כשאחד מגיע למכסה, עוברים לבא.
var speechModels = []string{"gemini-3.8-flash-tts", "gemini-3.8-flash-lite-tts", "gemini-3.1-flash-tts-preview", "gemini-2.5-flash-preview-tts"}

var speechEndpoint = "https://generativelanguage.googleapis.com/v1beta/models/%s:generateContent"

const (
	speechTries      = 3                 // ניסיונות לכל קובץ
	speechQuotaPause = time.Hour         // מודל שהגיע למכסה — לא מנסים אותו לפני כן
	speechBusyPause  = 2 * time.Minute   // כל המודלים עמוסים — הפסקה קצרה
	speechRequeueAge = 12 * time.Hour    // אחרי הפעלה מחדש: הודעות מהשעות האחרונות בלי קול — חוזרות לתור
	speechRequeueMax = 40                // כמה לכל היותר בכל שלוחה
	speechTimeout    = 150 * time.Second // בקשה אחת ל-Gemini (הודעה ארוכה לוקחת עד דקה וחצי)
)

// speechFile: קובץ הקול של ההקראה (אותו מספר כמו introFile, בסיומת wav).
func speechFile(base int) string { return fmt.Sprintf("%05d.wav", base+1) }

type speechJob struct {
	ext       string
	base      int
	text      string
	low       bool // עדכון ניסוח זמן — אחרי הודעות חדשות
	held      bool // נוסף בסבב הנוכחי — יוצא לעבודה בסוף הסבב (speechTick)
	running   bool
	tries     int
	notBefore time.Time
}

type speechResult struct {
	ext  string
	base int
	ok   bool
}

type speaker struct {
	key, voice string
	client     *http.Client

	mu         sync.Mutex
	jobs       map[string]*speechJob // ext/base → העבודה (הטקסט האחרון)
	results    []speechResult
	modelPause map[int]time.Time
	pausedTil  time.Time
	bg         bool
	warned     map[string]bool
}

// newSpeaker: nil כשאין מפתח או ש-SPEECH=off.
func newSpeaker(key, voice, mode string) *speaker {
	key = cleanKey(key)
	if key == "" || strings.EqualFold(strings.TrimSpace(mode), "off") {
		return nil
	}
	if voice = strings.TrimSpace(voice); voice == "" {
		voice = "Charon"
	}
	return &speaker{key: key, voice: voice, client: &http.Client{Timeout: speechTimeout},
		jobs: map[string]*speechJob{}, modelPause: map[int]time.Time{}, warned: map[string]bool{}}
}

func jobKey(ext string, base int) string { return fmt.Sprintf("%s/%d", ext, base) }

// add מכניס (או מעדכן) עבודה. טקסט חדש לאותו קובץ מחליף את הישן.
func (s *speaker) add(ext string, base int, text string, low bool) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	k := jobKey(ext, base)
	if j, ok := s.jobs[k]; ok {
		// אם היא רצה עכשיו — ה-worker רואה שהטקסט השתנה, לא מעלה, ומנסה שוב עם החדש.
		j.text, j.tries, j.notBefore = text, 0, time.Time{}
		j.low = j.low && low
		return
	}
	s.jobs[k] = &speechJob{ext: ext, base: base, text: text, low: low, held: true}
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

// next: העבודה הבאה — חדשות קודם (low=false), ובתוכן המספר הגבוה (החדש) קודם.
func (s *speaker) next() (*speechJob, speechJob) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	if now.Before(s.pausedTil) {
		return nil, speechJob{}
	}
	var best *speechJob
	for _, j := range s.jobs {
		if j.running || j.held || now.Before(j.notBefore) {
			continue
		}
		if best == nil || (best.low && !j.low) || (best.low == j.low && j.base > best.base) {
			best = j
		}
	}
	if best == nil {
		return nil, speechJob{}
	}
	best.running = true
	return best, *best
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
	data, model, err := s.synthesize(job.text)
	if err == nil {
		data, err = prepareSpeech(data)
	}
	s.mu.Lock()
	changed := j.text != job.text // הטקסט עודכן בזמן שעבדנו — הקול כבר לא מתאים
	s.mu.Unlock()
	if err == nil && !changed {
		err = cfg.y.uploadFile(job.ext, speechFile(job.base), "speech.wav", data, true)
	}
	var busy *speechBusyErr
	s.mu.Lock()
	defer s.mu.Unlock()
	j.running = false
	switch {
	case changed:
		// נשאר בתור עם הטקסט החדש
	case err == nil:
		delete(s.jobs, jobKey(job.ext, job.base))
		s.results = append(s.results, speechResult{job.ext, job.base, true})
		log.Printf("קול מוכן: שלוחה %s / %s (%s, %v).", job.ext, speechFile(job.base), model, time.Since(start).Round(100*time.Millisecond))
	case errors.As(err, &busy):
		// מכסה / עומס בכל המודלים — לא אשמת הקובץ; לא סופרים ניסיון
		s.pausedTil = time.Now().Add(busy.wait)
		if !s.warned["busy"] {
			s.warned["busy"] = true
			log.Printf("הערה: יצירת קול מושהית ל-%v (%v). בינתיים ההודעות מושמעות כטקסט.", busy.wait, err)
		}
	default:
		j.tries++
		if j.tries >= speechTries {
			delete(s.jobs, jobKey(job.ext, job.base))
			s.results = append(s.results, speechResult{job.ext, job.base, false})
			log.Printf("הערה: הקול של %s בשלוחה %s לא נוצר (נשאר טקסט): %v", speechFile(job.base), job.ext, err)
		} else {
			j.notBefore = time.Now().Add(time.Duration(j.tries) * time.Minute)
		}
	}
	return true
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
func (s *speaker) synthesize(text string) ([]byte, string, error) {
	body, _ := json.Marshal(map[string]any{
		// בלי הוראות לפני הטקסט — המודל מקריא אותן בקול.
		"contents": []any{map[string]any{"parts": []any{map[string]any{"text": text}}}},
		"generationConfig": map[string]any{
			"responseModalities": []string{"AUDIO"},
			"speechConfig":       map[string]any{"voiceConfig": map[string]any{"prebuiltVoiceConfig": map[string]any{"voiceName": s.voice}}},
		},
	})
	var lastErr error
	allQuota := true
	for m, model := range speechModels {
		s.mu.Lock()
		paused := time.Now().Before(s.modelPause[m])
		s.mu.Unlock()
		if paused {
			continue
		}
		data, status, err := s.call(model, body)
		if err == nil {
			return data, model, nil
		}
		lastErr = err
		switch {
		case status == http.StatusTooManyRequests:
			s.mu.Lock()
			s.modelPause[m] = time.Now().Add(speechQuotaPause)
			s.mu.Unlock()
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
	if allQuota {
		wait = 15 * time.Minute
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
		return nil, resp.StatusCode, fmt.Errorf("%s: HTTP %d: %s", model, resp.StatusCode, msg)
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
		if !ok || !r.ok {
			continue
		}
		if !a.hasBase(r.base) {
			// ההודעה כבר לא בשלוחה (נמחקה בינתיים) — גם הקול שעלה
			_ = cfg.y.remove([]string{ivrPath(r.ext, speechFile(r.base))})
			continue
		}
		a.addFile(speechFile(r.base))
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
// שעדיין בלי קול חוזרות לתור. פעם אחת לכל שלוחה בכל הפעלה.
func (a *archive) requeueSpeech(cfg *config, now time.Time) {
	if cfg.speech == nil || a.spoken {
		return
	}
	a.spoken = true
	n := 0
	for _, e := range a.entries {
		if n >= speechRequeueMax {
			break
		}
		if e.base < 0 || now.Sub(time.Unix(e.ts, 0)) > speechRequeueAge || a.hasFile(speechFile(e.base)) || !a.hasFile(introFile(e.base)) {
			continue
		}
		text, exists, err := cfg.y.read(a.ext, introFile(e.base))
		if err != nil || !exists {
			continue
		}
		cfg.speech.add(a.ext, e.base, text, true)
		n++
	}
	if n > 0 {
		log.Printf("שלוחה %s: %d הודעות אחרונות בלי קול מוכן — נכנסו לתור.", a.ext, n)
	}
}
