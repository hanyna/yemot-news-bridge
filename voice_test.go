package main

import (
	"encoding/base64"
	"encoding/json"
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
	status map[string]int // מודל → סטטוס
	calls  []string
	texts  []string
}

func (g *fakeTTS) handler(w http.ResponseWriter, r *http.Request) {
	g.mu.Lock()
	defer g.mu.Unlock()
	model := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/"), ":generateContent")
	g.calls = append(g.calls, model)
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
	if got := f.files["ivr2:/1/10001.wav"]; got != "AUDIO:PHONE:אלישע ירד, בשעה 7 בערב. הודעה חדשה;convert=1" {
		t.Fatalf("ext 1 wav: %q", got)
	}
	if got := f.files["ivr2:/2/1/10001.wav"]; got != "AUDIO:PHONE:בשעה 7 בערב. הודעה חדשה;convert=1" {
		t.Fatalf("reporter wav: %q", got)
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

	// אחרי חצות: הטקסט מתעדכן ל"אתמול", והקול נוצר מחדש עם הניסוח החדש
	nowFunc = func() time.Time { return day.Add(6 * time.Hour) }
	if err := syncOnce(&cfg, st); err != nil {
		t.Fatal(err)
	}
	if got := f.files["ivr2:/1/10001.wav"]; got != "AUDIO:PHONE:אלישע ירד, אתמול בשעה 7 בערב. הודעה חדשה;convert=1" {
		t.Fatalf("rerendered wav: %q", got)
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
	s.add("1", 10000, "x", false) // nil — לא קורס
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
	s.add("1", 10000, "הודעה", false)
	j, _ := s.next()
	if j.file != "M1000.wav" || j.voice != "Puck" {
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
	if !strings.Contains(wav, "סוף ההודעה") || strings.Contains(wav, "לא הוקרא") || !strings.HasPrefix(wav, "AUDIO:PHONE:אלישע ירד, בשעה 7 בערב.") {
		t.Fatalf("wav should be full: len=%d tail=%q txt=%v", len(wav), wav[len(wav)-120:], f.files["ivr2:/1/10001.txt"] != "")
	}
	if !strings.HasSuffix(f.files["ivr2:/1/10001.txt"], "סוף ההודעה") || !isBridgeFile("10001.txt") {
		t.Fatal("full text not kept")
	}
	nowFunc = func() time.Time { return day.Add(6 * time.Hour) }
	if err := syncOnce(&cfg, st); err != nil {
		t.Fatal(err)
	}
	wav = f.files["ivr2:/1/10001.wav"]
	if !strings.HasPrefix(wav, "AUDIO:PHONE:אלישע ירד, אתמול בשעה 7 בערב.") || !strings.Contains(wav, "סוף ההודעה") {
		t.Fatalf("rerendered full wav: %.120q", wav)
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
	if f.files["ivr2:/1/10001.txt"] != "" || f.files["ivr2:/1/10001.wav"] != "" {
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
