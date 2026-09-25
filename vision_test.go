package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"image"
	"image/color"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func testPNG() []byte {
	img := image.NewRGBA(image.Rect(0, 0, 4, 4))
	img.Set(1, 1, color.RGBA{255, 0, 0, 255})
	var b bytes.Buffer
	_ = png.Encode(&b, img)
	return b.Bytes()
}

type fakeGemini struct {
	mu       sync.Mutex
	calls    int
	paths    []string
	images   int
	status   map[string]int // path substring → status
	reply    string
	lastBody string
}

func (g *fakeGemini) handler(w http.ResponseWriter, r *http.Request) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.calls++
	g.paths = append(g.paths, r.URL.Path)
	if r.Header.Get("x-goog-api-key") != "GKEY" {
		w.WriteHeader(403)
		io.WriteString(w, `{"error":{"message":"bad key"}}`)
		return
	}
	for sub, st := range g.status {
		if strings.Contains(r.URL.Path, sub) {
			w.WriteHeader(st)
			io.WriteString(w, `{"error":{"message":"nope"}}`)
			return
		}
	}
	raw, _ := io.ReadAll(r.Body)
	g.lastBody = string(raw)
	var req struct {
		Contents []struct {
			Parts []struct {
				InlineData *struct {
					MimeType string `json:"mimeType"`
				} `json:"inlineData"`
			} `json:"parts"`
		} `json:"contents"`
	}
	_ = json.Unmarshal(raw, &req)
	for _, p := range req.Contents[0].Parts {
		if p.InlineData != nil && p.InlineData.MimeType == "image/png" {
			g.images++
		}
	}
	resp := map[string]any{"candidates": []any{map[string]any{
		"content": map[string]any{"parts": []any{map[string]any{"text": g.reply}}}}}}
	json.NewEncoder(w).Encode(resp)
}

func withFakeGemini(t *testing.T, g *fakeGemini) *httptest.Server {
	gs := httptest.NewServer(http.HandlerFunc(g.handler))
	oldE, oldGap := visionEndpoints, visionGap
	visionEndpoints = []string{gs.URL + "/studio/%s:generateContent", gs.URL + "/vertex/%s:generateContent"}
	visionGap = 0
	t.Cleanup(func() { gs.Close(); visionEndpoints, visionGap = oldE, oldGap })
	return gs
}

func TestPhotoURLs(t *testing.T) {
	cases := map[string]int{
		`<div class="msgtext">x</div><div class="photo"><img src="https://cdn4.telesco.pe/a.jpg" alt=""></div>`:                            1,
		`<div class="photo"><div class="album two"><img src="https://c/a.jpg"><img src="https://c/b.jpg"></div></div>`:                     2,
		`<div class="photo"><div class="vidwrap"><video src="/api/media?channel=a&amp;id=2" poster="https://c/p.jpg"></video></div></div>`: 0,
		`<div class="photo"><div class="vidwrap embedwrap" data-embed="a/1"><img src="https://c/thumb.jpg" alt=""><div class="playbtn">`:   0,
		`<div class="stickerbox"><img src="https://c/s.webp"></div>`:                                                                       0,
		`<div class="msgtext">x</div><a class="linkbox"><img src="https://c/l.jpg"></a>`:                                                   0,
		// ה-div של תמונה לא תמיד נפתח בדיוק ב-`<div class="photo">` — יכולות
		// להיות עוד תכונות (id, style וכו') לפני/אחרי class="photo".
		`<div id="m1" class="photo" style="x"><img src="https://c/a.jpg"></div>`: 1,
	}
	for h, want := range cases {
		if got := photoURLs(h, "https://x.onrender.com/api/messages"); len(got) != want {
			t.Errorf("%s → %v (want %d)", h, got, want)
		}
	}
	if got := photoURLs(`<div class="photo"><img src="/api/img?x=1&amp;y=2"></div>`, "https://x.onrender.com/api/messages"); len(got) != 1 || got[0] != "https://x.onrender.com/api/img?x=1&y=2" {
		t.Errorf("relative: %v", got)
	}
}

func TestVisionInArchive(t *testing.T) {
	g := &fakeGemini{reply: "```json\n{\"description\": \"רכבי צבא בכניסה ליישוב\", \"text\": \"סגר על האזור\"}\n```",
		status: map[string]int{"/studio/": 403}} // מפתח "AQ." של Vertex: הכתובת הראשונה דוחה
	withFakeGemini(t, g)
	pic := testPNG()
	now := time.Now().Unix()
	var f *fakeYemotServer
	mux := http.NewServeMux()
	mux.HandleFunc("/img/", func(w http.ResponseWriter, r *http.Request) { w.Write(pic) })
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) { f.handler(w, r) })
	srv := httptest.NewServer(mux)
	defer srv.Close()
	f = archiveServer([]FeedItem{
		{ID: 1, Channel: "a", TS: now - 300, HTML: `<div class="photo"><img src="` + srv.URL + `/img/1.jpg" alt=""></div>`},
		{ID: 2, Channel: "a", TS: now - 200, Text: "תיעוד מהשטח הערב", HTML: `<div class="msgtext">תיעוד מהשטח הערב</div><div class="photo"><div class="album two"><img src="/img/2.jpg"><img src="/img/3.jpg"></div></div>`},
		{ID: 3, Channel: "a", TS: now - 100, Text: "הודעה בלי תמונה"},
		{ID: 4, Channel: "a", TS: now - 90*3600, HTML: `<div class="photo"><img src="/img/old.jpg"></div>`}, // ישנה — בלי ניתוח
	}, `{"channels":[{"name":"a","title":"אלישע ירד"}]}`)
	cfg := newTestCfg(srv)
	cfg.vision = newVisionClient("GKEY", "", "on")
	if err := syncOnce(&cfg, &state{}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(f.files["ivr2:/1/10001.tts"], "בתמונה") {
		t.Fatalf("old: %q", f.files["ivr2:/1/10001.tts"])
	}
	one := f.files["ivr2:/1/10003.tts"]
	if !strings.HasSuffix(one, "פורסמה תמונה. בתמונה: רכבי צבא בכניסה ליישוב. כתוב בתמונה: סגר על האזור.") {
		t.Fatalf("photo only: %q", one)
	}
	two := f.files["ivr2:/1/10005.tts"]
	if !strings.Contains(two, "תיעוד מהשטח הערב") || !strings.HasSuffix(two, "2 תמונות. בתמונות: רכבי צבא בכניסה ליישוב. כתוב בתמונות: סגר על האזור.") {
		t.Fatalf("text+album: %q", two)
	}
	if strings.Contains(f.files["ivr2:/1/10007.tts"], "בתמונה") {
		t.Fatalf("no photo: %q", f.files["ivr2:/1/10007.tts"])
	}
	if f.files["ivr2:/2/1/10003.tts"] == "" || !strings.HasSuffix(f.files["ivr2:/2/1/10003.tts"], "כתוב בתמונה: סגר על האזור.") {
		t.Fatalf("reporter ext: %q", f.files["ivr2:/2/1/10003.tts"])
	}
	// שתי הודעות עם תמונה → 2 בקשות מוצלחות (שלוחת הכתב מקבלת מהמטמון) + ניסיון ראשון בכתובת שנדחתה
	var ok int
	for _, p := range g.paths {
		if strings.HasPrefix(p, "/vertex/") {
			ok++
		}
	}
	if ok != 2 || g.images != 3 {
		t.Fatalf("calls %v images %d", g.paths, g.images)
	}
	if !strings.Contains(g.lastBody, "תיעוד מהשטח הערב") {
		t.Fatal("message text not sent as context")
	}
	for _, u := range f.uploads {
		if strings.Contains(u, "GKEY") {
			t.Fatal("key leaked")
		}
	}
}

func TestVisionFailureDoesNotBlock(t *testing.T) {
	g := &fakeGemini{status: map[string]int{"/": 429}}
	withFakeGemini(t, g)
	pic := testPNG()
	now := time.Now().Unix()
	var f *fakeYemotServer
	mux := http.NewServeMux()
	mux.HandleFunc("/img/", func(w http.ResponseWriter, r *http.Request) { w.Write(pic) })
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) { f.handler(w, r) })
	srv := httptest.NewServer(mux)
	defer srv.Close()
	f = archiveServer([]FeedItem{
		{ID: 1, Channel: "a", TS: now - 300, HTML: `<div class="photo"><img src="/img/1.jpg"></div>`},
		{ID: 2, Channel: "a", TS: now - 200, HTML: `<div class="photo"><img src="/img/2.jpg"></div>`},
	}, `{"channels":[{"name":"a","title":"אלישע ירד"}]}`)
	cfg := newTestCfg(srv)
	cfg.vision = newVisionClient("GKEY", "", "on")
	if err := syncOnce(&cfg, &state{}); err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(f.files["ivr2:/1/10001.tts"], "פורסמה תמונה") || !strings.HasSuffix(f.files["ivr2:/1/10003.tts"], "פורסמה תמונה") {
		t.Fatalf("%q | %q", f.files["ivr2:/1/10001.tts"], f.files["ivr2:/1/10003.tts"])
	}
	if g.calls != 1 {
		t.Fatalf("after quota error should pause, calls=%d", g.calls)
	}
}

func TestVisionOff(t *testing.T) {
	if newVisionClient("", "", "on") != nil || newVisionClient("k", "", "off") != nil {
		t.Fatal("should be off")
	}
	if v := newVisionClient(" k\n", "my-model", ""); v == nil || v.key != "k" || v.models[0] != "my-model" {
		t.Fatalf("%+v", v)
	}
}

// עומס על המודל הראשי (503) — מנסים מודל אחר ברשימה, ולא מוותרים על התיאור.
func TestVisionOverloadFallsBack(t *testing.T) {
	g := &fakeGemini{reply: `{"description": "תצלומי דיוקן של שני חיילים", "text": ""}`}
	withFakeGemini(t, g)
	pic := testPNG()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write(pic) }))
	defer srv.Close()
	v := newVisionClient("GKEY", "", "on")
	// בפעם הראשונה המודל הראשי עונה — הוא נבחר
	if _, _, err := v.analyze([]string{srv.URL + "/a.jpg"}, ""); err != nil || v.model != 0 {
		t.Fatalf("first: %v model=%d", err, v.model)
	}
	// עכשיו הוא עמוס
	g.mu.Lock()
	g.status = map[string]int{"/" + visionModels[0] + ":": 503}
	g.mu.Unlock()
	desc, _, err := v.analyze([]string{srv.URL + "/b.jpg"}, "")
	if err != nil || desc != "תצלומי דיוקן של שני חיילים" {
		t.Fatalf("fallback: %q %v (paths %v)", desc, err, g.paths)
	}
	if v.model != 0 {
		t.Fatal("the main model should stay the default")
	}
	// גם בגילוי הראשון: מודל עמוס → הבא, בלי "המפתח לא התקבל"
	v2 := newVisionClient("GKEY", "", "on")
	if _, _, err := v2.analyze([]string{srv.URL + "/c.jpg"}, ""); err != nil || v2.model != 1 {
		t.Fatalf("discovery: %v model=%d", err, v2.model)
	}
	// כולם עמוסים — שגיאה רגילה (ננסה שוב), לא הפסקה של 10 דקות
	g.mu.Lock()
	g.status = map[string]int{"/": 503}
	g.mu.Unlock()
	v3 := newVisionClient("GKEY", "", "on")
	_, _, err = v3.analyze([]string{srv.URL + "/d.jpg"}, "")
	var pe *visionPauseErr
	if err == nil || errors.As(err, &pe) {
		t.Fatalf("all busy: %v", err)
	}
}
