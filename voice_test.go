package main

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeTTS: מדמה את Gemini TTS. מחזיר PCM שהוא הטקסט עצמו (כדי לבדוק מה הוקרא).
type fakeTTS struct {
	mu     sync.Mutex
	status map[string]int  // מודל → סטטוס
	quota  map[string]bool // מפתחות שהגיעו למכסה
	calls  []string
	texts  []string
}

func (g *fakeTTS) handler(w http.ResponseWriter, r *http.Request) {
	g.mu.Lock()
	defer g.mu.Unlock()
	model := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/"), ":generateContent")
	g.calls = append(g.calls, model)
	if g.quota[r.Header.Get("x-goog-api-key")] {
		w.WriteHeader(429)
		io.WriteString(w, `{"error":{"code":429,"details":[{"violations":[{"quotaId":"GenerateRequestsPerDayPerProjectPerModel-FreeTier"}]}]}}`)
		return
	}
	if st := g.status[model]; st != 0 {
		w.WriteHeader(st)
		io.WriteString(w, `{"error":{"message":"nope"}}`)
		return
	}
	var req struct {
		Contents []struct {
			Parts []struct {
				Text string `json:"text"`
			} `json:"parts"`
		} `json:"contents"`
	}
	raw, _ := io.ReadAll(r.Body)
	json.Unmarshal(raw, &req)
	text := req.Contents[0].Parts[0].Text
	g.texts = append(g.texts, text)
	json.NewEncoder(w).Encode(map[string]any{"candidates": []any{map[string]any{"content": map[string]any{"parts": []any{
		map[string]any{"inlineData": map[string]any{"mimeType": "audio/L16;codec=pcm;rate=24000", "data": base64.StdEncoding.EncodeToString([]byte(text))}}}}}}})
}

func withFakeTTS(t *testing.T, g *fakeTTS) {
	s := httptest.NewServer(http.HandlerFunc(g.handler))
	oldE, oldP, oldF := speechEndpoint, prepareSpeech, haveFFmpeg
	speechEndpoint = s.URL + "/%s:generateContent"
	// במקום ffmpeg: מחזיר את ה-PCM שבתוך ה-WAV (כלומר את הטקסט)
	prepareSpeech = func(wav []byte) ([]byte, error) { return append([]byte("PHONE:"), wav[44:]...), nil }
	haveFFmpeg = func() bool { return true }
	t.Cleanup(func() { s.Close(); speechEndpoint, prepareSpeech, haveFFmpeg = oldE, oldP, oldF })
}

func TestSpeechUploadsWav(t *testing.T) {
	g := &fakeTTS{status: map[string]int{speechModels[0]: 429}} // המודל הראשון במכסה — עוברים לבא
	withFakeTTS(t, g)
	defer func() { nowFunc = time.Now }()
	loc, _ := time.LoadLocation("Asia/Jerusalem")
	day := time.Date(2026, 9, 24, 20, 0, 0, 0, loc)
	nowFunc = func() time.Time { return day }
	f := archiveServer([]FeedItem{{ID: 1, Channel: "a", TS: day.Add(-time.Hour).Unix(), Text: "הודעה חדשה"}},
		`{"channels":[{"name":"a","title":"אלישע ירד"}]}`)
	srv := httptest.NewServer(http.HandlerFunc(f.handler))
	defer srv.Close()
	cfg := newTestCfg(srv)
	cfg.speech = newSpeaker("GKEY", "", "on")
	st := &state{}
	if err := syncOnce(&cfg, st); err != nil {
		t.Fatal(err)
	}
	// קובץ קול אחד — לשלוחה 1 ולשלוחת הכתב, עם שם הכתב והיום בשבוע
	if got := f.files["ivr2:/1/10001.wav"]; got != "AUDIO:PHONE:אלישע ירד, ביום חמישי בשעה 7 בערב. הודעה חדשה;convert=1" {
		t.Fatalf("ext 1 wav: %q", got)
	}
	if got := f.files["ivr2:/2/1/10001.wav"]; got != f.files["ivr2:/1/10001.wav"] {
		t.Fatalf("reporter wav: %q", got)
	}
	msgs := 0
	for _, tx := range g.texts {
		if strings.Contains(tx, "הודעה חדשה") {
			msgs++
		}
	}
	if msgs != 1 {
		t.Fatalf("one synthesis per message, got %d", msgs)
	}
	if !strings.Contains(f.files["ivr2:/1/archive.txt"], "e a/1 10000 ") || !strings.Contains(f.files["ivr2:/1/archive.txt"], " w\n") {
		t.Fatalf("voiced flag not saved: %q", f.files["ivr2:/1/archive.txt"])
	}
	if f.files["ivr2:/1/10001.tts"] == "" {
		t.Fatal("text must stay as fallback")
	}
	if !st.arch["1"].hasFile("10001.wav") {
		t.Fatal("wav not tracked in archive files (trim would miss it)")
	}
	// המודל שבמכסה לא נוסה שוב בהודעה השנייה
	n := 0
	for _, c := range g.calls {
		if c == speechModels[0] {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("quota model retried: %v", g.calls)
	}
	for _, tx := range g.texts {
		if strings.Contains(tx, "הקרא") {
			t.Fatalf("instructions sent: %q", tx)
		}
	}

	st.speechTick(&cfg) // התפריטים (עולים בסוף הסבב) — כמו ברקע
	// אחרי חצות: הטקסט מתעדכן ל"אתמול"; הקול ("ביום חמישי") נשאר — בלי ליצור מחדש
	nowFunc = func() time.Time { return day.Add(6 * time.Hour) }
	n = len(g.texts)
	if err := syncOnce(&cfg, &state{}); err != nil { // גם אחרי הפעלה מחדש
		t.Fatal(err)
	}
	if !strings.Contains(f.files["ivr2:/1/10001.tts"], "אתמול בשעה 7 בערב") || f.files["ivr2:/1/10001.wav"] == "" || len(g.texts) != n {
		t.Fatalf("after midnight: tts=%q wav=%q synth=%q", f.files["ivr2:/1/10001.tts"], f.files["ivr2:/1/10001.wav"], g.texts[n:])
	}
	// אחרי שבוע (תאריך) — הקול נמחק, מושמע הטקסט
	nowFunc = func() time.Time { return day.Add(8 * 24 * time.Hour) }
	if err := syncOnce(&cfg, &state{}); err != nil {
		t.Fatal(err)
	}
	if f.files["ivr2:/1/10001.wav"] != "" || !strings.Contains(f.files["ivr2:/1/10001.tts"], "ב 24 בספטמבר") {
		t.Fatalf("after a week: wav=%q tts=%q", f.files["ivr2:/1/10001.wav"], f.files["ivr2:/1/10001.tts"])
	}
}

// בלי קול (כל המודלים נכשלים) — ההודעה נשארת כטקסט, והקו ממשיך כרגיל.
func TestSpeechFailureKeepsText(t *testing.T) {
	g := &fakeTTS{status: map[string]int{}}
	for _, m := range speechModels {
		g.status[m] = 429
	}
	withFakeTTS(t, g)
	now := time.Now().Unix()
	f := archiveServer([]FeedItem{{ID: 1, Channel: "a", TS: now - 60, Text: "הודעה"}}, `{"channels":[{"name":"a","title":"אלישע ירד"}]}`)
	srv := httptest.NewServer(http.HandlerFunc(f.handler))
	defer srv.Close()
	cfg := newTestCfg(srv)
	cfg.speech = newSpeaker("GKEY", "", "on")
	if err := syncOnce(&cfg, &state{}); err != nil {
		t.Fatal(err)
	}
	if f.files["ivr2:/1/10001.tts"] == "" || f.files["ivr2:/1/10001.wav"] != "" {
		t.Fatalf("tts=%q wav=%q", f.files["ivr2:/1/10001.tts"], f.files["ivr2:/1/10001.wav"])
	}
	if len(g.calls) != len(speechModels) {
		t.Fatalf("after all-quota should pause, calls=%v", g.calls)
	}
}

func TestSpeechOff(t *testing.T) {
	if newSpeaker("", "", "on") != nil || newSpeaker("k", "", "off") != nil {
		t.Fatal("should be off")
	}
	var s *speaker
	s.add("a/1", 1, "1", 10000, "x", false) // nil — לא קורס
	if v := newSpeaker("k", "", ""); v == nil || v.voice != "Charon" {
		t.Fatal("default voice")
	}
}

// התפריט הראשי, תפריט בחירת הכתב וכותרות הכתבים — גם כקול מוכן, בקול התפריטים.
// כשהטקסט משתנה, הקול הישן נמחק ונוצר חדש; כשלא השתנה — לא נוגעים.
func TestSpeechMenus(t *testing.T) {
	g := &fakeTTS{status: map[string]int{}}
	withFakeTTS(t, g)
	now := time.Now().Unix()
	f := archiveServer([]FeedItem{{ID: 1, Channel: "a", TS: now - 60, Text: "הודעה"}}, `{"channels":[{"name":"a","title":"אלישע ירד"}]}`)
	srv := httptest.NewServer(http.HandlerFunc(f.handler))
	defer srv.Close()
	cfg := newTestCfg(srv)
	cfg.speech = newSpeakerVoices("GKEY", "Charon", "Puck", "on")
	st := &state{}
	if err := syncOnce(&cfg, st); err != nil {
		t.Fatal(err)
	}
	st.speechTick(&cfg) // התפריט עולה בסוף הסבב — ברקע הוא נוצר מיד
	welcome := f.files["ivr2:/M1000.tts"]
	if welcome == "" || f.files["ivr2:/M1000.wav"] != "AUDIO:PHONE:"+welcome+";convert=1" {
		t.Fatalf("welcome wav: %q (tts %q)", f.files["ivr2:/M1000.wav"], welcome)
	}
	if f.files["ivr2:/2/M1000.wav"] == "" || f.files["ivr2:/2/1/99999.wav"] != "AUDIO:PHONE:עדכוני אלישע ירד.;convert=1" {
		t.Fatalf("chooser/title wav: %q | %q", f.files["ivr2:/2/M1000.wav"], f.files["ivr2:/2/1/99999.wav"])
	}
	// הפעלה מחדש, אותו טקסט — לא מוחקים ולא יוצרים שוב
	calls := len(g.calls)
	cfg.speech = newSpeakerVoices("GKEY", "Charon", "Puck", "on")
	if err := syncOnce(&cfg, &state{}); err != nil {
		t.Fatal(err)
	}
	if len(g.calls) != calls {
		t.Fatalf("regenerated unchanged menus: %v", g.calls[calls:])
	}
	// הודעת פתיחה חדשה — הקול מתעדכן
	cfg.welcome = "ברוכים הבאים. נוסח חדש."
	st2 := &state{}
	if err := syncOnce(&cfg, st2); err != nil {
		t.Fatal(err)
	}
	st2.speechTick(&cfg)
	if f.files["ivr2:/M1000.wav"] != "AUDIO:PHONE:ברוכים הבאים. נוסח חדש.;convert=1" {
		t.Fatalf("new welcome wav: %q", f.files["ivr2:/M1000.wav"])
	}
}

func TestSpeechMenuVoice(t *testing.T) {
	s := newSpeakerVoices("k", "Charon", "", "on")
	s.addMenu("", "M1000.wav", "שלום")
	s.add("a/1", 1, "1", 10000, "הודעה", true)
	j, _ := s.next()
	if j.targets[0].file != "M1000.wav" || j.voice != "Puck" {
		t.Fatalf("menu first with menu voice: %+v", j)
	}
}

// הודעה ארוכה: הטקסט נחתך (מגבלת ימות), אבל הקול מקריא הכול — גם אחרי שינוי ניסוח הזמן.
func TestSpeechLongMessageFull(t *testing.T) {
	g := &fakeTTS{status: map[string]int{}}
	withFakeTTS(t, g)
	defer func() { nowFunc = time.Now }()
	loc, _ := time.LoadLocation("Asia/Jerusalem")
	day := time.Date(2026, 9, 24, 20, 0, 0, 0, loc)
	nowFunc = func() time.Time { return day }
	long := strings.Repeat("זה משפט ארוך מאוד עם הרבה מילים. ", 60) + "סוף ההודעה."
	f := archiveServer([]FeedItem{{ID: 1, Channel: "a", TS: day.Add(-time.Hour).Unix(), Text: long}},
		`{"channels":[{"name":"a","title":"אלישע ירד"}]}`)
	srv := httptest.NewServer(http.HandlerFunc(f.handler))
	defer srv.Close()
	cfg := newTestCfg(srv)
	cfg.speech = newSpeaker("GKEY", "", "on")
	st := &state{}
	if err := syncOnce(&cfg, st); err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(f.files["ivr2:/1/10001.tts"], "המשך ההודעה לא הוקרא.") {
		t.Fatalf("tts should be cut: %q", f.files["ivr2:/1/10001.tts"][len(f.files["ivr2:/1/10001.tts"])-80:])
	}
	wav := f.files["ivr2:/1/10001.wav"]
	if !strings.Contains(wav, "סוף ההודעה") || strings.Contains(wav, "לא הוקרא") || !strings.HasPrefix(wav, "AUDIO:PHONE:אלישע ירד, ביום חמישי בשעה 7 בערב.") {
		t.Fatalf("wav should be full: len=%d", len(wav))
	}
}

// הודעה ארוכה שעלתה לפני התיקון (טקסט מקוצר, בלי טקסט מלא) — אחרי הפעלה מחדש
// הקול נוצר במלואו מההודעה שבשרת.
func TestSpeechBackfillLong(t *testing.T) {
	g := &fakeTTS{status: map[string]int{}}
	withFakeTTS(t, g)
	now := time.Now().Unix()
	long := strings.Repeat("זה משפט ארוך מאוד עם הרבה מילים. ", 60) + "סוף ההודעה"
	f := archiveServer([]FeedItem{{ID: 1, Channel: "a", TS: now - 3600, Text: long}}, `{"channels":[{"name":"a","title":"אלישע ירד"}]}`)
	srv := httptest.NewServer(http.HandlerFunc(f.handler))
	defer srv.Close()
	cfg := newTestCfg(srv)
	if err := syncOnce(&cfg, &state{}); err != nil { // בלי קול — כמו הגרסה הקודמת
		t.Fatal(err)
	}
	if f.files["ivr2:/1/10001-full.txt"] != "" || f.files["ivr2:/1/10001.wav"] != "" {
		t.Fatal("setup")
	}
	cfg.speech = newSpeaker("GKEY", "", "on")
	st := &state{}
	if err := syncOnce(&cfg, st); err != nil {
		t.Fatal(err)
	}
	st.speechTick(&cfg)
	if w := f.files["ivr2:/1/10001.wav"]; !strings.Contains(w, "סוף ההודעה") || strings.Contains(w, "לא הוקרא") {
		t.Fatalf("backfilled wav not full: len=%d", len(w))
	}
}

// קובץ הפרטים שימות המשיח יוצרים לכל קובץ קול (NNNNN.txt) — לא נחשב טקסט מלא.
func TestFullFileName(t *testing.T) {
	if fullFile(10190) != "10191-full.txt" || fileNum("10191-full.txt") != 10191 || fileNum("10191.txt") != -1 || !isBridgeFile("10191-full.txt") {
		t.Fatal(fullFile(10190), fileNum("10191-full.txt"), fileNum("10191.txt"))
	}
}

// מגבלה לדקה — ממתינים כמה ש-Google מבקשים, לא שעה; מכסה יומית — שעה.
func TestRetryAfter(t *testing.T) {
	perMin := []byte(`{"error":{"code":429,"message":"You exceeded your current quota. Please retry in 7.5s.","details":[{"violations":[{"quotaId":"GenerateRequestsPerMinutePerProjectPerModel"}]},{"@type":"type.googleapis.com/google.rpc.RetryInfo","retryDelay":"7s"}]}}`)
	if w := retryAfter(perMin); w < 7*time.Second || w > 10*time.Second {
		t.Fatalf("per minute: %v", w)
	}
	perDay := []byte(`{"error":{"code":429,"details":[{"violations":[{"quotaId":"GenerateRequestsPerDayPerProjectPerModel"}]}]}}`)
	if retryAfter(perDay) != speechDayPause {
		t.Fatal("per day")
	}
	if retryAfter([]byte(`{}`)) != time.Minute {
		t.Fatal("default")
	}
}

// כמה מפתחות (מפרויקטים נפרדים): כשהראשון במכסה — הבא, בלי לחכות.
func TestSpeechSeveralKeys(t *testing.T) {
	g := &fakeTTS{status: map[string]int{}, quota: map[string]bool{"K1": true}}
	withFakeTTS(t, g)
	keys := speechKeys(func(n string) string {
		return map[string]string{"GEMINI_API_KEY": "K1", "GEMINI_API_KEY_2": " K2\n", "GEMINI_API_KEY_3": "K1", "GEMINI_API_KEY_5": "K5"}[n]
	})
	if strings.Join(keys, ",") != "K1,K2,K5" {
		t.Fatalf("keys: %v", keys)
	}
	s := newSpeakerKeys(keys, "", "", "on")
	_, model, err := s.synthesize("שלום", "")
	if err != nil || !strings.Contains(model, "מפתח 2") {
		t.Fatalf("second key: %q %v", model, err)
	}
	// כל המפתחות במכסה — הפסקה, והקו ממשיך בטקסט
	g.mu.Lock()
	g.quota = map[string]bool{"K1": true, "K2": true, "K5": true}
	g.mu.Unlock()
	s2 := newSpeakerKeys(keys, "", "", "on")
	_, _, err = s2.synthesize("שלום", "")
	var busy *speechBusyErr
	if !errors.As(err, &busy) || busy.wait < 30*time.Minute {
		t.Fatalf("all keys at quota: %v", err)
	}
}

// הצפצוף בין ההודעות חוזר: play_beep=no שהוכנס בגרסה קודמת — יורד.
func TestBeepRestored(t *testing.T) {
	if got, ok := removeIniKey("type=playfile\nfile_amount_digits=5\nplay_beep=no\nvoice=Charon", "play_beep"); !ok || got != "type=playfile\nfile_amount_digits=5\nvoice=Charon" {
		t.Fatalf("%v %q", ok, got)
	}
	if !isBridgeIni("type=playfile\nfile_amount_digits=5\nplay_beep=no") {
		t.Fatal("old ini still ours")
	}
}
