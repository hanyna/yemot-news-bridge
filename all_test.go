package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestSpeech(t *testing.T) {
	cases := map[string]string{
		`כוחות צה״ל פעלו ביו"ש הלילה`:        "כוחות צהל פעלו ביהודה ושומרון הלילה",
		"בס״ד\nח\"כ פלוני ורה\"מ נפגשו":      "חבר הכנסת פלוני וראש הממשלה נפגשו",
		"המחיר עלה ב-5% ל-30₪ *דחוף* #חדשות": "המחיר עלה ב-5 אחוז ל-30 שקלים דחוף חדשות",
		"שלום @user https://t.me/x/1 סוף":    "שלום סוף",
		"שורה ראשונה\n\n\nשורה שנייה!!\n":    "שורה ראשונה. שורה שנייה!",
	}
	for in, want := range cases {
		if got := cleanForSpeech(in); got != want {
			t.Errorf("\nin:   %q\ngot:  %q\nwant: %q", in, got, want)
		}
	}
}

func TestWhen(t *testing.T) {
	loc, _ := time.LoadLocation("Asia/Jerusalem")
	now := time.Date(2026, 9, 22, 21, 0, 0, 0, loc)
	for _, c := range []struct {
		t    time.Time
		want string
	}{
		{time.Date(2026, 9, 22, 20, 5, 0, 0, loc), "בשעה 20 ו 5 דקות"},
		{time.Date(2026, 9, 21, 23, 0, 0, 0, loc), "אתמול בשעה 23 בדיוק"},
		{time.Date(2026, 9, 19, 8, 30, 0, 0, loc), "ביום שבת בשעה 8 ו 30 דקות"},
		{time.Date(2026, 9, 3, 8, 30, 0, 0, loc), "ב 3 בספטמבר בשעה 8 ו 30 דקות"},
	} {
		if got := spokenWhen(c.t, now); got != c.want {
			t.Errorf("got %q want %q", got, c.want)
		}
	}
}

func TestDedupe(t *testing.T) {
	long := "הודעה חשובה מאוד על אירוע ביטחוני בצומת הגדול ליד היישוב"
	items := prepare([]FeedItem{
		{Channel: "b", TS: 200, Text: long + " 📢"},
		{Channel: "a", TS: 100, Text: long},
		{Channel: "c", TS: 300, Text: long + " הצטרפו"},
		{Channel: "d", TS: 400, Text: "משהו אחר לגמרי"},
	})
	if len(items) != 2 || items[0].Channel != "a" {
		t.Fatalf("%+v", items)
	}
}

func TestIni(t *testing.T) {
	out, ch := setIniValues("type=menu\nvoice=Jacob\n", [][2]string{{"voice", "Sivan"}, {"rate", "-2"}})
	if !ch || out != "type=menu\nvoice=Sivan\nrate=-2\n" {
		t.Fatalf("%q", out)
	}
	if !isBridgeIni("type=playfile\nvoice=Sivan") || isBridgeIni("type=menu") || isBridgeIni("type=playfile\nx=1") {
		t.Fatal("isBridgeIni")
	}
}

type fakeYemot struct {
	mu      sync.Mutex
	files   map[string]string
	uploads []string
	deletes int
}

func TestFullSync(t *testing.T) {
	fy := &fakeYemot{files: map[string]string{
		"ivr2:/ext.ini":   "type=menu",
		"ivr2:/3/ext.ini": "type=menu\nsomething=1", // שלוחה תפוסה
	}}
	items := []FeedItem{
		{Channel: "elisha_yered", TS: time.Now().Unix() - 60, Text: "ראשונה מאלישע ביו״ש"},
		{Channel: "hakol", TS: time.Now().Unix() - 30, Text: "מהקול"},
	}
	var mu sync.Mutex
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		fy.mu.Lock()
		defer fy.mu.Unlock()
		r.ParseForm()
		ok := func(extra map[string]any) {
			m := map[string]any{"responseStatus": "OK"}
			for k, v := range extra {
				m[k] = v
			}
			json.NewEncoder(w).Encode(m)
		}
		switch {
		case r.URL.Path == "/api/messages":
			json.NewEncoder(w).Encode(map[string]any{"items": items})
		case r.URL.Path == "/api/channels":
			w.Write([]byte(`{"channels":[{"name":"elisha_yered","title":"אלישע ירד"},{"name":"hakol","title":"@hakol"},{"name":"third","title":"שלישי"}]}`))
		case strings.HasSuffix(r.URL.Path, "UploadTextFile"):
			fy.files[r.PostForm.Get("what")] = r.PostForm.Get("contents")
			fy.uploads = append(fy.uploads, r.PostForm.Get("what"))
			ok(nil)
		case strings.HasSuffix(r.URL.Path, "GetTextFile"):
			c, found := fy.files[r.PostForm.Get("what")]
			if !found {
				json.NewEncoder(w).Encode(map[string]any{"responseStatus": "ERROR", "message": "file not found"})
				return
			}
			ok(map[string]any{"contents": c})
		case strings.HasSuffix(r.URL.Path, "FileAction"):
			fy.deletes++
			ok(nil)
		case strings.HasSuffix(r.URL.Path, "GetIVR2Dir"):
			ok(map[string]any{"files": []map[string]string{{"name": "M1000.wav"}}})
		}
	}))
	defer srv.Close()
	yemotBase = srv.URL + "/ym/api/"
	loc, _ := time.LoadLocation("Asia/Jerusalem")
	cfg := config{feedURL: srv.URL + "/api/messages", feedKey: "k", ext: "1", maxMsgs: 10, perChan: 5,
		newestFirst: false, channelExts: true, voice: "Sivan", loc: loc,
		y: &yemot{client: srv.Client(), apiKey: "KEY"}, client: srv.Client(), feedClient: srv.Client()}
	diagnoseRoot(cfg.y)
	st := &state{files: map[string][]string{}, known: map[string]bool{}, chExt: map[string]string{}, blocked: map[string]bool{}}
	if err := syncOnce(&cfg, st); err != nil {
		t.Fatal(err)
	}
	f := fy.files
	// 001 = הישנה (אלישע), 002 = החדשה (hakol) — ימות המשיח משמיע מ-002.
	if !strings.HasPrefix(f["ivr2:/1/002.tts"], "hakol, ") || !strings.Contains(f["ivr2:/1/001.tts"], "אלישע ירד, ") || !strings.Contains(f["ivr2:/1/001.tts"], "ביהודה ושומרון") {
		t.Fatalf("ext1: %q | %q", f["ivr2:/1/001.tts"], f["ivr2:/1/002.tts"])
	}
	if f["ivr2:/2/ext.ini"] != "type=playfile\nvoice=Sivan" || !strings.HasPrefix(f["ivr2:/2/001.tts"], "עדכוני אלישע ירד. בשעה") {
		t.Fatalf("ext2: %q %q", f["ivr2:/2/ext.ini"], f["ivr2:/2/001.tts"])
	}
	if _, touched := f["ivr2:/3/001.tts"]; touched || f["ivr2:/3/ext.ini"] != "type=menu\nsomething=1" {
		t.Fatal("touched a foreign extension")
	}
	if !strings.Contains(f["ivr2:/4/001.tts"], "אין כרגע עדכונים חדשים משלישי") {
		t.Fatalf("ext4: %q", f["ivr2:/4/001.tts"])
	}
	if f["ivr2:/ext.ini"] != "type=menu\nvoice=Sivan" {
		t.Fatalf("root ini: %q", f["ivr2:/ext.ini"])
	}
	w := f["ivr2:/M1000.tts"]
	if !strings.Contains(w, "לעדכוני אלישע ירד הקישו 2.") || strings.Contains(w, "הקישו 3") || !strings.Contains(w, "לעדכוני שלישי הקישו 4.") {
		t.Fatalf("welcome: %q", w)
	}
	t.Logf("welcome: %s", w)
	t.Logf("1/001: %s", f["ivr2:/1/001.tts"])
	t.Logf("1/002: %s", f["ivr2:/1/002.tts"])

	fy.uploads = nil
	if err := syncOnce(&cfg, st); err != nil {
		t.Fatal(err)
	}
	if len(fy.uploads) != 0 {
		t.Fatalf("second round uploaded: %v", fy.uploads)
	}
}
