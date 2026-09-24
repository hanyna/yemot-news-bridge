package main

// ניתוח תמונות עם Gemini (Google AI Studio): הודעה עם תמונה מקבלת בהקראה
// תיאור קצר של מה רואים בה, ואת הטקסט שכתוב עליה (אם יש):
//
//	"אלישע ירד, בשעה 8 בערב. פורסמה תמונה. בתמונה: רכבי צבא בכניסה ליישוב. כתוב בתמונה: ..."
//
// המפתח — ב-GitHub Secrets בשם GEMINI_API_KEY (לא בקוד!). בלי מפתח, או עם
// VISION=off, הכול עובד כמו קודם ("פורסמה תמונה").
//
// מתי מנתחים: רק כשהודעה חדשה נכנסת לשלוחה (archive.add), ורק תמונות רגילות
// (לא סרטונים, לא סטיקרים ולא תמונה של קישור). כל הודעה מנותחת פעם אחת —
// שלוחה 1 ושלוחת הכתב מקבלות אותו תיאור. תקלה ב-Gemini לא עוצרת כלום: ההודעה
// עולה בלי התיאור.

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"log"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"
)

const (
	visionMaxImages = 4                // באלבום — כמה תמונות ראשונות שולחים
	visionImageMax  = 8 << 20          // תמונה גדולה מזה — לא שולחים
	visionMaxAge    = 6 * time.Hour    // הודעה ישנה מזה (ייבוא ראשון) — בלי ניתוח
	visionPause     = 10 * time.Minute // אחרי "חרגת מהמכסה" / מפתח לא תקין — הפסקה
	visionTries     = 2                // ניסיונות לכל הודעה
	visionTextMax   = 400              // אורך מקסימלי לטקסט שבתמונה (תווים)
)

// visionModels: המודל הראשון שעובד נשמר לכל ההפעלה. GEMINI_MODEL מוסיף מודל בראש הרשימה.
var visionModels = []string{"gemini-flash-latest", "gemini-3.5-flash", "gemini-2.5-flash"}

// visionEndpoints: מפתחות של AI Studio עובדים מול הכתובת הראשונה; מפתחות
// "AQ." מסוג Vertex express — מול השנייה. מנסים לפי הסדר ונשארים עם מה שעבד.
var visionEndpoints = []string{
	"https://generativelanguage.googleapis.com/v1beta/models/%s:generateContent",
	"https://aiplatform.googleapis.com/v1/publishers/google/models/%s:generateContent",
}

// visionGap: הפסקה מינימלית בין בקשות (המכסה החינמית מוגבלת בבקשות לדקה).
var visionGap = 4 * time.Second

const visionPrompt = `אתה מתאר תמונה שפורסמה בערוץ חדשות, עבור מאזינים בקו טלפון שלא רואים אותה. ההקראה נעשית בקול ממוחשב בעברית.
החזר JSON בלבד, בדיוק במבנה: {"description": "...", "text": "..."}

description — משפט אחד, לכל היותר שניים, בעברית פשוטה וענינית: מה רואים בתמונה (אירוע, מקום, כלי רכב, שלט וכו'). בלי פתיחות כמו "בתמונה נראה" או "זוהי תמונה של". בלי השערות, בלי פרשנות ובלי תיאור מראה חיצוני של אנשים. אם זו תמונה של טקסט בלבד (צילום מסך של הודעה, מסמך, מודעה) — כתוב רק מה זה, למשל "צילום מסך של הודעה".
text — הטקסט העיקרי שכתוב בתמונה, בעברית, כמו שהוא. טקסט בשפה אחרת — תרגם לעברית. בלי סימני מים, שמות ערוצים, לוגואים, קישורים ומספרי טלפון. אם אין טקסט משמעותי — מחרוזת ריקה. עד 400 תווים.
אם יש כמה תמונות — תאר את כולן יחד במשפט אחד או שניים.`

var (
	rePhotoImg = regexp.MustCompile(`<img[^>]*\ssrc="([^"]+)"`)
)

type visionClient struct {
	key      string
	client   *http.Client
	models   []string
	model    int // המודל שעובד (אינדקס ב-models)
	endpoint int // הכתובת שעובדת (אינדקס ב-visionEndpoints)
	found    bool

	mu        sync.Mutex
	cache     map[string]string // itemKey → התוספת להקראה ("" = אין / נכשל סופית)
	tries     map[string]int
	pausedTil time.Time
	last      time.Time
	warned    map[string]bool
}

// newVisionClient: nil כשאין מפתח או ש-VISION=off.
func newVisionClient(key, model, mode string) *visionClient {
	key = cleanKey(key)
	if key == "" || strings.EqualFold(strings.TrimSpace(mode), "off") {
		return nil
	}
	models := append([]string(nil), visionModels...)
	if m := strings.TrimSpace(model); m != "" {
		models = append([]string{m}, models...)
	}
	return &visionClient{
		key:    key,
		client: &http.Client{Timeout: 60 * time.Second},
		models: models,
		cache:  map[string]string{},
		tries:  map[string]int{},
		warned: map[string]bool{},
	}
}

func (v *visionClient) warnOnce(key, msg string) {
	if !v.warned[key] {
		v.warned[key] = true
		log.Println(msg)
	}
}

// photoURLs: כתובות התמונות שבהודעה — רק תמונה רגילה או אלבום.
// סרטון (גם התמונה המקדימה שלו), סטיקר ותמונה של קישור — לא.
func photoURLs(h, feedURL string) []string {
	i := strings.Index(h, `<div class="photo">`)
	if i < 0 {
		return nil
	}
	block := h[i:]
	if j := strings.Index(block, `</div></div>`); j >= 0 {
		block = block[:j]
	} else if j := strings.Index(block, `</div>`); j >= 0 {
		block = block[:j]
	}
	if strings.Contains(block, "vidwrap") || strings.Contains(block, "roundwrap") || strings.Contains(block, "<video") {
		return nil
	}
	var out []string
	for _, m := range rePhotoImg.FindAllStringSubmatch(block, visionMaxImages) {
		if u := absURL(html.UnescapeString(m[1]), feedURL); u != "" {
			out = append(out, u)
		}
	}
	return out
}

// absURL: כתובת יחסית (/api/...) — יחסית לשרת של ערוץ חי.
func absURL(src, base string) string {
	u, err := url.Parse(src)
	if err != nil {
		return ""
	}
	if u.IsAbs() {
		if u.Scheme != "http" && u.Scheme != "https" {
			return ""
		}
		return u.String()
	}
	b, err := url.Parse(base)
	if err != nil || !b.IsAbs() {
		return ""
	}
	return b.ResolveReference(u).String()
}

// describePhoto: התוספת להקראה של הודעה עם תמונה ("בתמונה: ... כתוב בתמונה: ..."),
// או "" (אין תמונה / אין מפתח / תקלה). כל הודעה מנותחת פעם אחת.
func (st *state) describePhoto(cfg *config, it FeedItem, now time.Time) string {
	v := cfg.vision
	if v == nil || it.HTML == "" {
		return ""
	}
	key := itemKey(it)
	v.mu.Lock()
	defer v.mu.Unlock()
	if d, ok := v.cache[key]; ok {
		return d
	}
	urls := photoURLs(it.HTML, cfg.feedURL)
	if len(urls) == 0 {
		return ""
	}
	if now.Sub(time.Unix(it.TS, 0)) > visionMaxAge || now.Before(v.pausedTil) {
		return "" // לא נשמר במטמון — בשלוחה אחרת אולי כבר יהיה אפשר
	}
	if wait := visionGap - time.Since(v.last); wait > 0 {
		time.Sleep(wait)
	}
	v.last = time.Now()
	desc, text, err := v.analyze(urls, cleanForSpeech(it.RawText))
	if err != nil {
		v.tries[key]++
		var pe *visionPauseErr
		if errors.As(err, &pe) {
			v.pausedTil = time.Now().Add(visionPause)
			v.warnOnce("pause:"+pe.why, "ניתוח תמונות: "+pe.why+" — ממשיך בלי תיאור, ומנסה שוב בעוד "+visionPause.String()+".")
			return ""
		}
		log.Printf("ניתוח תמונה %s נכשל (%d/%d): %v", key, v.tries[key], visionTries, err)
		if v.tries[key] >= visionTries {
			v.cache[key] = ""
		}
		return ""
	}
	out := spokenPhoto(desc, text, len(urls) > 1)
	v.cache[key] = out
	if out != "" {
		log.Printf("ניתוח תמונה %s: %.120s", key, out)
	}
	return out
}

// spokenPhoto: "בתמונה: <תיאור>. כתוב בתמונה: <טקסט>." (אלבום: "בתמונות").
func spokenPhoto(desc, text string, many bool) string {
	word := "בתמונה"
	if many {
		word = "בתמונות"
	}
	desc = strings.TrimRight(cleanForSpeech(desc), ". ")
	if r := []rune(text); len(r) > visionTextMax {
		text = cutAtWord(r[:visionTextMax])
	}
	text = strings.TrimRight(cleanForSpeech(text), ". ")
	var parts []string
	if desc != "" {
		parts = append(parts, word+": "+desc+".")
	}
	if text != "" {
		parts = append(parts, "כתוב "+word+": "+text+".")
	}
	return strings.Join(parts, " ")
}

// withPhoto מוסיף את התיאור לטקסט ההקראה של ההודעה.
func withPhoto(text, photo string) string {
	if photo == "" {
		return text
	}
	text = strings.TrimSpace(text)
	if text == "" {
		return photo
	}
	if !strings.HasSuffix(text, ".") && !strings.HasSuffix(text, "!") && !strings.HasSuffix(text, "?") {
		text += "."
	}
	return text + " " + photo
}

// visionPauseErr: תקלה שלא תיפתר בהודעה הבאה (מכסה / מפתח) — מפסיקים לזמן מה.
type visionPauseErr struct{ why string }

func (e *visionPauseErr) Error() string { return e.why }

type geminiPart struct {
	Text       string            `json:"text,omitempty"`
	InlineData *geminiInlineData `json:"inlineData,omitempty"`
}

type geminiInlineData struct {
	MimeType string `json:"mimeType"`
	Data     string `json:"data"`
}

// analyze שולח את התמונות ל-Gemini ומחזיר תיאור וטקסט.
func (v *visionClient) analyze(urls []string, msgText string) (desc, text string, err error) {
	parts := []geminiPart{{Text: visionPrompt}}
	if msgText != "" {
		parts = append(parts, geminiPart{Text: "טקסט ההודעה שהתמונה צורפה אליה (להקשר בלבד — אל תחזור עליו; אם בתמונה כתוב אותו דבר, השאר text ריק):\n" + msgText})
	}
	n := 0
	for _, u := range urls {
		data, mime, err := v.fetchImage(u)
		if err != nil {
			log.Printf("ניתוח תמונה: הורדת %s נכשלה: %v", shortURL(u), err)
			continue
		}
		parts = append(parts, geminiPart{InlineData: &geminiInlineData{MimeType: mime, Data: base64.StdEncoding.EncodeToString(data)}})
		n++
	}
	if n == 0 {
		return "", "", errors.New("לא הצלחתי להוריד אף תמונה")
	}
	body, _ := json.Marshal(map[string]any{
		"contents": []any{map[string]any{"role": "user", "parts": parts}},
		"generationConfig": map[string]any{
			"temperature":      0.2,
			"maxOutputTokens":  4096,
			"responseMimeType": "application/json",
		},
	})
	out, err := v.generate(body)
	if err != nil {
		return "", "", err
	}
	return parseVisionJSON(out)
}

// generate: מוצא בפעם הראשונה כתובת ומודל שעובדים עם המפתח, ונשאר איתם.
func (v *visionClient) generate(body []byte) (string, error) {
	if v.found {
		out, _, err := v.call(v.endpoint, v.model, body)
		return out, err
	}
	var lastErr error
endpoints:
	for e := range visionEndpoints {
		for m := range v.models {
			out, status, err := v.call(e, m, body)
			if err == nil {
				v.endpoint, v.model, v.found = e, m, true
				log.Printf("ניתוח תמונות: עובד עם %s (%s).", v.models[m], hostOf(visionEndpoints[e]))
				return out, nil
			}
			lastErr = err
			switch {
			case status == http.StatusNotFound, status == http.StatusBadRequest:
				continue // המודל לא קיים / לא זמין למפתח הזה — המודל הבא
			case status == http.StatusUnauthorized, status == http.StatusForbidden:
				continue endpoints // המפתח לא מתאים לכתובת הזו — הכתובת הבאה
			case status == http.StatusTooManyRequests:
				return "", &visionPauseErr{"חריגה מהמכסה של Gemini"}
			default:
				return "", err // תקלה זמנית (רשת / שרת) — ננסה שוב בהודעה הבאה
			}
		}
	}
	return "", &visionPauseErr{fmt.Sprintf("המפתח GEMINI_API_KEY לא התקבל (בדקו שהוא הועתק נכון ל-Secrets): %v", lastErr)}
}

// call: בקשה אחת ל-Gemini. מחזיר את הטקסט שבתשובה, או את קוד השגיאה.
func (v *visionClient) call(endpoint, model int, body []byte) (string, int, error) {
	u := fmt.Sprintf(visionEndpoints[endpoint], url.PathEscape(v.models[model]))
	req, err := http.NewRequest(http.MethodPost, u, bytes.NewReader(body))
	if err != nil {
		return "", 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-goog-api-key", v.key)
	resp, err := v.client.Do(req)
	if err != nil {
		return "", 0, errors.New(strings.ReplaceAll(err.Error(), v.key, "***"))
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
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
		if len(msg) > 200 {
			msg = msg[:200]
		}
		return "", resp.StatusCode, fmt.Errorf("%s: HTTP %d: %s", v.models[model], resp.StatusCode, strings.ReplaceAll(msg, v.key, "***"))
	}
	var r struct {
		Candidates []struct {
			Content struct {
				Parts []struct {
					Text    string `json:"text"`
					Thought bool   `json:"thought"`
				} `json:"parts"`
			} `json:"content"`
			FinishReason string `json:"finishReason"`
		} `json:"candidates"`
		PromptFeedback struct {
			BlockReason string `json:"blockReason"`
		} `json:"promptFeedback"`
	}
	if err := json.Unmarshal(raw, &r); err != nil {
		return "", resp.StatusCode, fmt.Errorf("תשובה לא תקינה: %v", err)
	}
	if len(r.Candidates) == 0 {
		// נחסם ע"י מסנני הבטיחות של Google — לא ננסה שוב
		return `{"description":"","text":""}`, resp.StatusCode, nil
	}
	var sb strings.Builder
	for _, p := range r.Candidates[0].Content.Parts {
		if !p.Thought {
			sb.WriteString(p.Text)
		}
	}
	if sb.Len() == 0 {
		return `{"description":"","text":""}`, resp.StatusCode, nil
	}
	return sb.String(), resp.StatusCode, nil
}

// parseVisionJSON: {"description": "...", "text": "..."} — גם כשהמודל עוטף ב-```json.
func parseVisionJSON(s string) (string, string, error) {
	s = strings.TrimSpace(s)
	if i, j := strings.Index(s, "{"), strings.LastIndex(s, "}"); i >= 0 && j > i {
		s = s[i : j+1]
	}
	var r struct {
		Description string `json:"description"`
		Text        string `json:"text"`
	}
	if err := json.Unmarshal([]byte(s), &r); err != nil {
		return "", "", fmt.Errorf("התשובה של Gemini לא ב-JSON: %.100s", s)
	}
	return strings.TrimSpace(r.Description), strings.TrimSpace(r.Text), nil
}

func (v *visionClient) fetchImage(u string) ([]byte, string, error) {
	resp, err := v.client.Get(u)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, "", fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, visionImageMax+1))
	if err != nil {
		return nil, "", err
	}
	if len(data) > visionImageMax {
		return nil, "", errors.New("התמונה גדולה מדי")
	}
	mime := http.DetectContentType(data)
	if !strings.HasPrefix(mime, "image/") {
		return nil, "", fmt.Errorf("לא תמונה (%s)", mime)
	}
	return data, mime, nil
}

func shortURL(u string) string {
	if p, err := url.Parse(u); err == nil {
		return p.Host + p.Path
	}
	return u
}

func hostOf(u string) string {
	if p, err := url.Parse(strings.SplitN(u, "%", 2)[0]); err == nil {
		return p.Host
	}
	return u
}
