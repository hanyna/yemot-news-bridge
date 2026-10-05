package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// דף בצורה של t.me/s/<ערוץ> האמיתי — אותה דוגמה שעליה נבדק הקוד בערוץ חי.
const tgRichPage = `<html><head>
<meta property="og:title" content="אלישע ירד">
</head><body>
<div class="tgme_widget_message" data-post="rich/101">
  <div class="tgme_widget_message_photo_wrap" style="background-image:url('https://cdn.example/a1.jpg')"></div>
  <a class="tgme_widget_message_photo_wrap" style="width:250px;background-image:url('https://cdn.example/a2.jpg')"></a>
  <div class="tgme_widget_message_text js-message_text">אלבום <b>משתי</b> תמונות<br/>שורה שנייה &amp; סוף</div>
  <time datetime="2026-08-11T10:00:00+00:00"></time>
</div>
<div class="tgme_widget_message" data-post="rich/102">
  <audio class="tgme_widget_message_voice js-message_voice" src="https://cdn.example/voice.ogg"></audio>
  <span class="tgme_widget_message_voice_duration">0:42</span>
  <time datetime="2026-08-11T10:01:00+00:00"></time>
</div>
<div class="tgme_widget_message" data-post="rich/103">
  <video class="tgme_widget_message_roundvideo js-message_roundvideo" src="https://cdn.example/round.mp4"></video>
  <time datetime="2026-08-11T10:02:00+00:00"></time>
</div>
<div class="tgme_widget_message" data-post="rich/104">
  <i class="tgme_widget_message_sticker" style="background-image:url('https://cdn.example/sticker.webp')"></i>
  <time datetime="2026-08-11T10:03:00+00:00"></time>
</div>
<div class="tgme_widget_message" data-post="rich/105">
  <div class="tgme_widget_message_poll_question">מי ינצח?</div>
  <div class="tgme_widget_message_poll_option">
    <div class="tgme_widget_message_poll_option_percent">67%</div>
    <div class="tgme_widget_message_poll_option_text">כן</div>
  </div>
  <div class="tgme_widget_message_poll_option">
    <div class="tgme_widget_message_poll_option_percent">33%</div>
    <div class="tgme_widget_message_poll_option_text">לא</div>
  </div>
  <time datetime="2026-08-11T10:04:00+00:00"></time>
</div>
<div class="tgme_widget_message" data-post="rich/106">
  <div class="tgme_widget_message_text js-message_text">תראו כתבה</div>
  <a class="tgme_widget_message_link_preview" href="https://news.example/article">
    <i class="link_preview_right_image" style="background-image:url('https://cdn.example/preview.jpg')"></i>
    <div class="link_preview_title">כותרת הכתבה</div>
  </a>
  <time datetime="2026-08-11T10:05:00+00:00"></time>
</div>
<div class="tgme_widget_message" data-post="rich/107">
  <div class="tgme_widget_message_video_player">
    <i class="tgme_widget_message_video_thumb" style="background-image:url('https://cdn.example/th.jpg')"></i>
    <video class="tgme_widget_message_video js-message_video" src="https://cdn.example/clip.mp4"></video>
    <time class="message_video_duration js-message_video_duration">1:05</time>
  </div>
  <time datetime="2026-08-11T10:07:00+00:00"></time>
</div>
<div class="tgme_widget_message" data-post="rich/108">
  <div class="tgme_widget_message_video_player">
    <i class="tgme_widget_message_video_thumb" style="background-image:url('https://cdn.example/big.jpg')"></i>
    <time class="message_video_duration js-message_video_duration">45:00</time>
  </div>
  <time datetime="2026-08-11T10:08:00+00:00"></time>
</div>
<div class="tgme_widget_message" data-post="rich/109">
  <a class="tgme_widget_message_photo_wrap" style="background-image:url('https://cdn.example/one.jpg')"></a>
  <time datetime="2026-08-11T10:09:00+00:00"></time>
</div>
</body></html>`

// ההודעות מהדף של טלגרם מזוהות בגשר בדיוק כמו אלה שמגיעות משרת ערוץ חי.
func TestTgParseMatchesServerFormat(t *testing.T) {
	msgs := tgParse(tgRichPage)
	if len(msgs) != 9 || tgTitle(tgRichPage) != "אלישע ירד" {
		t.Fatalf("parsed %d, title %q", len(msgs), tgTitle(tgRichPage))
	}
	by := map[int]FeedItem{}
	for _, m := range msgs {
		by[m.ID] = tgItem(m)
	}
	if it := by[101]; it.Text != "אלבום משתי תמונות\nשורה שנייה & סוף" || it.TS != 1786442400 {
		t.Fatalf("text/time: %q %d", it.Text, it.TS)
	}
	cases := []struct {
		id        int
		note      string
		onlyMedia bool
		skip      bool
		audio     string // סוג הקול שיורד (v/o) או ""
		photos    int
	}{
		{101, "2 תמונות", false, false, "", 2},
		{102, "הודעה קולית", true, false, "o", 0},
		{103, "סרטון קצר", true, false, "v", 0},
		{104, "", true, true, "", 0},
		{105, "סקר: מי ינצח?. האפשרויות: כן 67%, לא 33%", true, false, "", 0},
		{106, "", false, false, "", 0},
		{107, "סרטון באורך דקה ו 5 שניות", true, false, "v", 0},
		{108, "סרטון באורך 45 דקות", true, false, "", 0}, // ארוך — טלגרם לא נותנים בלי חשבון
		{109, "תמונה", true, false, "", 1},
	}
	for _, c := range cases {
		h := by[c.id].HTML
		note, only, skip := mediaNote(h)
		note = strings.Join(strings.Fields(note), " ") // רווחים כפולים — כמו מהשרת; ההקראה מנקה אותם
		if note != c.note || only != c.onlyMedia || skip != c.skip {
			t.Errorf("%d mediaNote = %q %v %v (want %q %v %v)\n%s", c.id, note, only, skip, c.note, c.onlyMedia, c.skip, h)
		}
		kind, src, _, ok := audioSource(h)
		if (c.audio == "") == ok || (ok && kind != c.audio) {
			t.Errorf("%d audioSource = %q %v", c.id, kind, ok)
		}
		if c.id == 102 && src != "https://cdn.example/voice.ogg" {
			t.Errorf("voice src %q", src)
		}
		if got := photoURLs(h, ""); len(got) != c.photos {
			t.Errorf("%d photos %v", c.id, got)
		}
	}
	// ההקראה של הודעות בלי טקסט: כמו בערוץ חי
	clean := prepareClean([]FeedItem{by[102], by[104], by[107]})
	if len(clean) != 2 || clean[0].Text != "פורסמה הודעה קולית" || !strings.HasPrefix(clean[1].Text, "פורסם סרטון באורך") {
		t.Fatalf("spoken: %+v", clean)
	}
}

// fakeTelegram: דפי ערוצים לפי מספרי הודעות. before=N מחזיר 3 הודעות לפני N.
type fakeTelegram struct {
	mu     sync.Mutex
	posts  map[string][]int // ערוץ → מספרי הודעות קיימות
	status int              // ≠0: כל בקשה מקבלת את הסטטוס הזה
	hits   []string
	embed  map[int]string    // פוסט → כתובת mp4 בדף ה-embed
	raw    map[string]string // ערוץ → הדף כמו שהוא (במקום הדף שנבנה מ-posts)
}

func (g *fakeTelegram) page(ch string, ids []int, now int64) string {
	var b strings.Builder
	b.WriteString(`<meta property="og:title" content="הערוץ ` + ch + `">`)
	for _, id := range ids {
		b.WriteString(fmt.Sprintf(`<div class="tgme_widget_message" data-post="%s/%d"><div class="tgme_widget_message_text js-message_text">הודעה מספר %d בערוץ %s</div><time datetime="%s"></time></div>`,
			ch, id, id, ch, time.Unix(now-int64(1000-id), 0).UTC().Format(time.RFC3339)))
	}
	return b.String()
}

func (g *fakeTelegram) handler(w http.ResponseWriter, r *http.Request) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.hits = append(g.hits, r.URL.RequestURI())
	if g.status != 0 {
		w.WriteHeader(g.status)
		return
	}
	now := time.Now().Unix()
	if strings.HasPrefix(r.URL.Path, "/s/") {
		ch := strings.TrimPrefix(r.URL.Path, "/s/")
		if page, ok := g.raw[ch]; ok {
			if r.URL.Query().Get("before") == "" {
				w.Write([]byte(page))
			}
			return
		}
		ids := g.posts[ch]
		if b := r.URL.Query().Get("before"); b != "" {
			n, _ := strconv.Atoi(b)
			var older []int
			for _, id := range ids {
				if id < n {
					older = append(older, id)
				}
			}
			ids = older
		}
		if len(ids) > 3 { // "20 אחרונות" בקטן
			ids = ids[len(ids)-3:]
		}
		w.Write([]byte(g.page(ch, ids, now)))
		return
	}
	// /<ערוץ>/<מספר>?embed=1 — דף של פוסט
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if len(parts) == 2 && r.URL.Query().Get("embed") == "1" {
		id, _ := strconv.Atoi(parts[1])
		if src := g.embed[id]; src != "" {
			w.Write([]byte(`<video src="` + src + `"></video>`))
			return
		}
	}
	w.Write([]byte(`<html></html>`))
}

func withFakeTelegram(t *testing.T, g *fakeTelegram) *httptest.Server {
	srv := httptest.NewServer(http.HandlerFunc(g.handler))
	oldBase, oldGap, oldFB := tgBase, tgGap, tgFallback
	tgBase, tgGap = srv.URL, 0
	t.Cleanup(func() { srv.Close(); tgBase, tgGap, tgFallback = oldBase, oldGap, oldFB })
	return srv
}

// מטלגרם ישר לקו: ההודעות נכנסות לשלוחה 1 ולשלוחת הכתב, עם השם מהדף של הערוץ,
// ובלי אף בקשה לשרת של ערוץ חי.
func TestSyncDirectFromTelegram(t *testing.T) {
	g := &fakeTelegram{posts: map[string][]int{"a": {5, 6, 7, 8}, "b": {20}}}
	withFakeTelegram(t, g)
	f := archiveServer(nil, `{"channels":[]}`)
	var serverHits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/") {
			serverHits++
		}
		f.handler(w, r)
	}))
	defer srv.Close()
	cfg := newTestCfg(srv)
	cfg.tg = newTgSource("a, @b", "")
	st := &state{}
	if err := syncOnce(&cfg, st); err != nil {
		t.Fatal(err)
	}
	if serverHits != 0 {
		t.Fatalf("ערוץ חי נשאל %d פעמים", serverHits)
	}
	var all []string
	for p, c := range f.files {
		if strings.HasPrefix(p, "ivr2:/1/1") && strings.HasSuffix(p, ".tts") {
			all = append(all, c)
		}
	}
	joined := strings.Join(all, "\n")
	for _, want := range []string{"הודעה מספר 5 בערוץ a", "הודעה מספר 8 בערוץ a", "הודעה מספר 20 בערוץ b", "הערוץ a"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("חסר %q בשלוחה 1:\n%s", want, joined)
		}
	}
	// הפעלה ראשונה: גם הדף הקודם של a (5 לא בדף האחרון)
	if !strings.Contains(strings.Join(g.hits, " "), "/s/a?before=6") {
		t.Fatalf("no history page: %v", g.hits)
	}
	// בסבב הבא — לא נבדק שוב לפני הזמן
	g.hits = nil
	if err := syncOnce(&cfg, st); err != nil {
		t.Fatal(err)
	}
	if len(g.hits) != 0 {
		t.Fatalf("polled too soon: %v", g.hits)
	}
}

// הרבה הודעות מאז הבדיקה הקודמת → נקראים גם הדפים הקודמים, בלי חור.
func TestTgFillsGap(t *testing.T) {
	g := &fakeTelegram{posts: map[string][]int{"a": {1, 2, 3}}}
	withFakeTelegram(t, g)
	s := newTgSource("a", "")
	now := time.Now()
	s.poll(now)
	g.posts["a"] = []int{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11}
	g.hits = nil
	items, _, _ := s.poll(now.Add(time.Hour))
	have := map[int]bool{}
	for _, it := range items {
		have[it.ID] = true
	}
	for id := 1; id <= 11; id++ {
		if !have[id] {
			t.Fatalf("missing %d (hits %v)", id, g.hits)
		}
	}
}

// טלגרם חוסמים: המתנה מדורגת, ואחרי tgFallback — ההודעות משרת ערוץ חי.
func TestTgBlockedFallsBackToServer(t *testing.T) {
	g := &fakeTelegram{status: 429}
	withFakeTelegram(t, g)
	now := time.Now().Unix()
	f := archiveServer([]FeedItem{{ID: 1, Channel: "a", TS: now - 60, Text: "הודעה מהשרת של ערוץ חי"}}, `{"channels":[{"name":"a","title":"אלישע ירד"}]}`)
	srv := httptest.NewServer(http.HandlerFunc(f.handler))
	defer srv.Close()
	cfg := newTestCfg(srv)
	cfg.tg = newTgSource("a", "")
	st := &state{}
	if err := syncOnce(&cfg, st); err != nil {
		t.Fatal(err)
	}
	if f.files["ivr2:/1/10001.tts"] != "" {
		t.Fatal("should wait for Telegram first")
	}
	if n := len(g.hits); n != 1 {
		t.Fatalf("hits %d", n)
	}
	if err := syncOnce(&cfg, st); err != nil || len(g.hits) != 1 {
		t.Fatalf("backoff not respected: %v %v", err, g.hits)
	}
	tgFallback = 0
	if err := syncOnce(&cfg, st); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(f.files["ivr2:/1/10001.tts"], "הודעה מהשרת של ערוץ חי") {
		t.Fatalf("fallback: %q", f.files["ivr2:/1/10001.tts"])
	}
	// בלי מפתח לשרת — שגיאה (שמגיעה במייל אם היא נמשכת)
	cfg.feedKey = ""
	if err := syncOnce(&cfg, st); err == nil {
		t.Fatal("expected error without TGPOPUP_KEY")
	}
}

// סרטון: הכתובת מהדף פגה → כתובת חדשה מדף ה-embed של הפוסט.
func TestTgVideoRefreshesExpiredURL(t *testing.T) {
	var mu sync.Mutex
	cdn := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if r.URL.Path == "/old.mp4" {
			w.WriteHeader(403)
			return
		}
		w.Write([]byte("MP4DATA"))
	}))
	defer cdn.Close()
	g := &fakeTelegram{embed: map[int]string{7: cdn.URL + "/new.mp4"}}
	withFakeTelegram(t, g)
	cfg := config{tg: newTgSource("a", "")}
	cfg.tg.chans[0].videos[7] = cdn.URL + "/old.mp4"
	path := filepath.Join(t.TempDir(), "v.mp4")
	if err := downloadMedia(&cfg, &audioJob{key: "a/7", kind: "v", channel: "a", id: 7}, path); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(path); string(b) != "MP4DATA" {
		t.Fatalf("%q", b)
	}
}

func TestTgSourceConfig(t *testing.T) {
	if newTgSource("a,b", "server") != nil || newTgSource("", "") != nil {
		t.Fatal("should be off")
	}
	s := newTgSource("https://t.me/s/Elisha_Yered, @b\nb", "")
	if len(s.chans) != 2 || s.chans[0].name != "Elisha_Yered" || s.every() != tgMinEvery {
		t.Fatalf("%+v %v", s.chans, s.every())
	}
	if d := newTgSource(defaultChannels, "").every(); d != 42*time.Second {
		t.Fatalf("7 channels: %v", d)
	}
}

// סרטון קצר שבדף של הערוץ מופיע רק כתמונה: הקול שלו יורד מהדף של הפוסט.
// סרטון ש"גדול מדי" (טלגרם לא נותנים בלי חשבון) — מוותרים מיד, בלי ניסיונות חוזרים.
func TestTgThumbOnlyShortVideo(t *testing.T) {
	withFakeTranscode(t)
	cdn := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("CLIP")) }))
	defer cdn.Close()
	now := time.Now()
	post := func(id int, dur string) string {
		return fmt.Sprintf(`<div class="tgme_widget_message" data-post="a/%d"><div class="tgme_widget_message_video_player">`+
			`<i class="tgme_widget_message_video_thumb" style="background-image:url('https://cdn.example/th.jpg')"></i>`+
			`<time class="message_video_duration js-message_video_duration">%s</time></div>`+
			`<time datetime="%s"></time></div>`, id, dur, now.Add(-time.Duration(100-id)*time.Second).UTC().Format(time.RFC3339))
	}
	g := &fakeTelegram{embed: map[int]string{7: cdn.URL + "/clip.mp4"},
		raw: map[string]string{"a": `<meta property="og:title" content="אלישע ירד">` + post(7, "0:40") + post(8, "12:00")}}
	withFakeTelegram(t, g)
	f := archiveServer(nil, `{"channels":[]}`)
	srv := httptest.NewServer(http.HandlerFunc(f.handler))
	defer srv.Close()
	cfg := newTestCfg(srv)
	cfg.audio, cfg.audioMax = true, 20*60
	cfg.tg = newTgSource("a", "")
	cfg.tg.client = &http.Client{Transport: tooBigTransport{base: http.DefaultTransport, id: 8}}
	defer func(b []time.Duration) { retryBackoff = b }(retryBackoff)
	retryBackoff = []time.Duration{0, 0, 0}
	if err := syncOnce(&cfg, &state{}); err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(f.files["ivr2:/1/10001.tts"], "פורסם סרטון באורך 40 שניות") {
		t.Fatalf("intro: %q", f.files["ivr2:/1/10001.tts"])
	}
	if f.files["ivr2:/1/10000.wav"] != "AUDIO:MP3:CLIP;convert=1" {
		t.Fatalf("short thumb video: %q", f.files["ivr2:/1/10000.wav"])
	}
	if f.has("ivr2:/1", "10002.wav") {
		t.Fatal("too big video got audio")
	}
	if !regexp.MustCompile(`(?m)^e a/8 10002 \d+ 0 3 v$`).MatchString(f.files["ivr2:/1/archive.txt"]) {
		t.Fatalf("index:\n%s", f.files["ivr2:/1/archive.txt"])
	}
	for _, h := range g.hits {
		if strings.HasPrefix(h, "/a/8") { // "Media is too big" — אף בקשה נוספת לפוסט
			t.Fatalf("too-big retried: %v", g.hits)
		}
	}
}

// tooBigTransport: דף ה-embed של הפוסט id עונה "Media is too big".
type tooBigTransport struct {
	base http.RoundTripper
	id   int
}

func (tr tooBigTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.URL.Path == "/a/"+strconv.Itoa(tr.id) && r.URL.Query().Get("embed") == "1" {
		rec := httptest.NewRecorder()
		rec.WriteString(`<div class="message_media_not_supported_label">Media is too big</div>`)
		return rec.Result(), nil
	}
	return tr.base.RoundTrip(r)
}
