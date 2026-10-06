package main

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestXHandle(t *testing.T) {
	cases := map[string]string{
		"ariel__danino":                         "ariel__danino",
		"@ariel__danino":                        "ariel__danino",
		"https://x.com/ariel__danino":           "ariel__danino",
		"https://x.com/ariel__danino?s=21&t=ab": "ariel__danino",
		"https://twitter.com/amit_segal/status": "amit_segal",
		"x.com/kann_news":                       "kann_news",
		"לא שם":                                 "",
		"waytoolonghandle_123":                  "",
	}
	for in, want := range cases {
		if got := xHandle(in); got != want {
			t.Errorf("%q → %q (want %q)", in, got, want)
		}
	}
	s := newXSource("ariel__danino, @Ariel__Danino\nhttps://x.com/kann_news", "")
	if s == nil || len(s.accts) != 2 || s.accts[1].channel() != "x-kann_news" {
		t.Fatalf("%+v", s)
	}
	if newXSource("  ", "") != nil {
		t.Fatal("empty should be nil")
	}
}

// fakeX: שירות מדומה בצורה של api.fxtwitter.com.
type fakeX struct {
	mu         sync.Mutex
	statuses   map[string]string // handle → JSON של /2/profile/<h>/statuses
	profiles   map[string]string // handle → JSON של /<h>
	media      map[string]string // handle → JSON של /2/profile/<h>/media
	statusByID map[string]string // id → JSON של /2/status/<id>
	status     int               // != 0 — כל בקשה מחזירה את הקוד הזה
	calls      int
}

func (f *fakeX) handler(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if f.status != 0 {
		w.WriteHeader(f.status)
		return
	}
	p := strings.Trim(r.URL.Path, "/")
	if strings.HasPrefix(p, "2/status/") {
		if b, ok := f.statusByID[strings.TrimPrefix(p, "2/status/")]; ok {
			w.Write([]byte(b))
			return
		}
		w.WriteHeader(404)
		return
	}
	if strings.HasPrefix(p, "2/profile/") && strings.HasSuffix(p, "/media") {
		if b, ok := f.media[strings.TrimSuffix(strings.TrimPrefix(p, "2/profile/"), "/media")]; ok {
			w.Write([]byte(b))
			return
		}
		w.WriteHeader(404)
		return
	}
	if strings.HasPrefix(p, "2/profile/") && strings.HasSuffix(p, "/statuses") {
		h := strings.TrimSuffix(strings.TrimPrefix(p, "2/profile/"), "/statuses")
		if b, ok := f.statuses[h]; ok {
			w.Write([]byte(b))
			return
		}
		w.WriteHeader(404)
		w.Write([]byte(`{"code":404,"results":[],"cursor":{"top":null,"bottom":null}}`))
		return
	}
	if b, ok := f.profiles[p]; ok {
		w.Write([]byte(b))
		return
	}
	w.WriteHeader(404)
	w.Write([]byte(`{"code":404,"message":"User not found"}`))
}

func withFakeX(t *testing.T, f *fakeX) *httptest.Server {
	srv := httptest.NewServer(http.HandlerFunc(f.handler))
	old := xBase
	xBase = srv.URL
	t.Cleanup(func() { srv.Close(); xBase = old })
	return srv
}

func xTS(ago time.Duration) string {
	return itoa64(time.Now().Add(-ago).Unix())
}

func itoa64(n int64) string {
	var b [20]byte
	i := len(b)
	for n > 0 || i == len(b) {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}

// ציוצים בצורה שהשירות מחזיר (השדות שנבדקו מול השירות האמיתי).
func danioStatuses(img string) string {
	author := `"author":{"screen_name":"ariel__danino","name":"אריאל דנינו Ariel Danino"}`
	return `{"code":200,"results":[
	 {"type":"status","id":"2103195622446690666","text":"לכל המתרגשים מפסילתו של אבו שחאדה, יש לי פרט טריוויה","created_timestamp":` + xTS(50*time.Minute) + `,` + author + `,"reposted_by":null,"replying_to":null,"media":null},
	 {"type":"status","id":"2103195622446690700","text":"פינוי הגבעה הלילה","created_timestamp":` + xTS(40*time.Minute) + `,` + author + `,"reposted_by":null,"replying_to":null,
	  "media":{"all":[{"type":"photo","url":"` + img + `"}],"photos":[{"type":"photo","url":"` + img + `","width":1200,"height":800}]}},
	 {"type":"status","id":"2103195622446690800","text":"ריטוויט של מישהו אחר","created_timestamp":` + xTS(30*time.Minute) + `,"author":{"screen_name":"someone","name":"מישהו"},"reposted_by":{"screen_name":"ariel__danino"},"replying_to":null},
	 {"type":"status","id":"2103195622446690900","text":"@someone תגובה למישהו","created_timestamp":` + xTS(20*time.Minute) + `,` + author + `,"reposted_by":null,"replying_to":{"screen_name":"someone","status":"1"}},
	 {"type":"status","id":"2103195622446691000","text":"המשך השרשור שלי","created_timestamp":` + xTS(10*time.Minute) + `,` + author + `,"reposted_by":null,"replying_to":{"screen_name":"ariel__danino","status":"2103195622446690700"}},
	 {"type":"status","id":"2103195622446691100","text":"תיעוד","created_timestamp":` + xTS(5*time.Minute) + `,` + author + `,"reposted_by":null,"replying_to":null,
	  "media":{"videos":[{"type":"video","url":"https://video.twimg.com/ext_tw_video/1/pu/vid/720x1280/a.mp4","thumbnail_url":"https://pbs.twimg.com/t.jpg","duration":89.4}]}},
	 {"type":"status","id":"2103195622446691200","text":"","created_timestamp":` + xTS(4*time.Minute) + `,` + author + `,"reposted_by":null,"replying_to":null,
	  "quote":{"id":"9","text":"הודעה חשובה מהשטח","author":{"screen_name":"elisha","name":"אלישע ירד"}}}
	],"cursor":{"top":"a","bottom":"b"}}`
}

func TestXFetchFilters(t *testing.T) {
	f := &fakeX{statuses: map[string]string{"ariel__danino": danioStatuses("https://pbs.twimg.com/media/p.jpg")}}
	withFakeX(t, f)
	s := newXSource("ariel__danino", "")
	items, chans := s.poll(time.Now())
	if len(chans) != 1 || chans[0].Name != "x-ariel__danino" || chans[0].Title != "אריאל דנינו" {
		t.Fatalf("chans %+v", chans)
	}
	got := map[int]FeedItem{}
	for _, it := range items {
		got[it.ID] = it
	}
	if len(got) != 6 {
		t.Fatalf("want 6 (retweet in, no reply to others), got %d: %+v", len(got), items)
	}
	if rt := got[2103195622446690800]; rt.Text != "שיתף ציוץ של מישהו: ריטוויט של מישהו אחר" {
		t.Fatalf("retweet: %q", rt.Text)
	}
	if _, ok := got[2103195622446690900]; ok {
		t.Fatal("reply to other kept")
	}
	if it := got[2103195622446691000]; it.Text != "המשך השרשור שלי" {
		t.Fatalf("self thread: %+v", it)
	}
	if it := got[2103195622446690700]; !strings.Contains(it.HTML, `<div class="photo"><img src="https://pbs.twimg.com/media/p.jpg"`) {
		t.Fatalf("photo html: %s", it.HTML)
	}
	v := got[2103195622446691100]
	kind, _, secs, ok := audioSource(v.HTML)
	if !ok || kind != "v" || secs != 89 || !strings.HasSuffix(s.videoURL("x-ariel__danino", v.ID), "a.mp4") {
		t.Fatalf("video: %v %d %v — %s", kind, secs, ok, v.HTML)
	}
	if q := got[2103195622446691200]; q.Text != "ציטוט של אלישע ירד: הודעה חשובה מהשטח" {
		t.Fatalf("quote: %q", q.Text)
	}
	if it := got[2103195622446690666]; it.Channel != "x-ariel__danino" || time.Since(time.Unix(it.TS, 0)) > time.Hour {
		t.Fatalf("%+v", it)
	}
}

func TestXFailureAndBackoff(t *testing.T) {
	f := &fakeX{status: 503}
	withFakeX(t, f)
	s := newXSource("ariel__danino", "")
	now := time.Now()
	if items, chans := s.poll(now); len(items) != 0 || len(chans) != 1 {
		t.Fatal(items, chans)
	}
	s.poll(now.Add(10 * time.Second)) // עוד לא הגיע הזמן
	if f.calls != 1 || s.accts[0].fails != 1 {
		t.Fatalf("calls %d fails %d", f.calls, s.accts[0].fails)
	}
	f.status = 0
	f.statuses = map[string]string{"ariel__danino": danioStatuses("https://x/p.jpg")}
	if items, _ := s.poll(now.Add(2 * time.Minute)); len(items) != 6 || s.accts[0].fails != 0 {
		t.Fatalf("recovery: %d items, fails %d", len(items), s.accts[0].fails)
	}
}

func TestXUnknownAndEmpty(t *testing.T) {
	f := &fakeX{profiles: map[string]string{"quiet_one": `{"code":200,"user":{"screen_name":"quiet_one","name":"שקט"}}`}}
	withFakeX(t, f)
	s := newXSource("nosuchuser_x, quiet_one", "")
	_, chans := s.poll(time.Now())
	if s.accts[0].lastErr != errXNotFound.Error() || s.accts[0].nextTry.Sub(time.Now()) > 2*time.Minute {
		t.Fatalf("unknown: %+v", s.accts[0])
	}
	if s.accts[1].fails != 0 || chans[1].Title != "שקט" {
		t.Fatalf("empty: %+v %+v", s.accts[1], chans)
	}
}

// TestXInLine: ציוצים נכנסים לשלוחה 1 ולשלוחת כתב משלהם, לצד ערוצי הטלגרם.
func TestXInLine(t *testing.T) {
	now := time.Now().Unix()
	f := archiveServer([]FeedItem{
		{ID: 1, Channel: "a", TS: now - 7200, Text: "הודעה מטלגרם"},
	}, `{"channels":[{"name":"a","title":"אלישע ירד"}]}`)
	srv := httptest.NewServer(http.HandlerFunc(f.handler))
	defer srv.Close()
	fx := &fakeX{statuses: map[string]string{"ariel__danino": danioStatuses("https://x/p.jpg")}}
	withFakeX(t, fx)
	cfg := newTestCfg(srv)
	cfg.x = newXSource("ariel__danino", "")
	if err := syncOnce(&cfg, &state{}); err != nil {
		t.Fatal(err)
	}
	var main, rep []string
	for p, c := range f.files {
		switch {
		case strings.HasPrefix(p, "ivr2:/1/1") && strings.HasSuffix(p, ".tts"):
			main = append(main, c)
		case strings.HasPrefix(p, "ivr2:/2/2/1") && strings.HasSuffix(p, ".tts"):
			rep = append(rep, c)
		}
	}
	all := strings.Join(main, "\n")
	for _, want := range []string{"הודעה מטלגרם", "אריאל דנינו", "פסילתו של אבו שחאדה", "המשך השרשור שלי", "ציטוט של אלישע ירד"} {
		if !strings.Contains(all, want) {
			t.Errorf("ext 1 missing %q:\n%s", want, all)
		}
	}
	if !strings.Contains(all, "שיתף ציוץ של מישהו") || strings.Contains(all, "תגובה למישהו") || strings.Contains(all, "Ariel Danino") {
		t.Errorf("unexpected:\n%s", all)
	}
	if len(rep) != 6 {
		t.Errorf("reporter ext 2/2: %d files: %v", len(rep), rep)
	}
}

// TestXVideoAudio: הקול של סרטון מטוויטר יורד ישר מהכתובת שבציוץ (לא דרך טלגרם).
func TestXVideoAudio(t *testing.T) {
	vid := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("fake-mp4-bytes"))
	}))
	defer vid.Close()
	it := tgItem(tgMessage{ID: 77, Channel: "x-ariel__danino", Text: "סרטון", Time: time.Now().UTC().Format(time.RFC3339),
		Video: vid.URL + "/a.mp4", Duration: "0:30"})
	cfg := config{audio: true, tg: newTgSource("elisha_yered", "")}
	st := &state{}
	st.ensureMaps()
	kind, a := st.queueAudio(&cfg, it, "1", 5, time.Now())
	if kind != "v" || a != audioPending || len(st.aw.queue) != 1 {
		t.Fatalf("kind %q audio %d queue %d", kind, a, len(st.aw.queue))
	}
	j := st.aw.queue[0]
	if j.src != vid.URL+"/a.mp4" {
		t.Fatalf("src %q", j.src)
	}
	path := filepath.Join(t.TempDir(), "v.mp4")
	if err := downloadMedia(&cfg, j, path); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(path); string(b) != "fake-mp4-bytes" {
		t.Fatalf("got %q", b)
	}
	j2 := &audioJob{key: "x-ariel__danino/78", kind: "v", channel: "x-ariel__danino", id: 78}
	var rl *retryLaterError
	if err := downloadMedia(&cfg, j2, path); !errors.As(err, &rl) {
		t.Fatalf("no src after restart: %v", err)
	}
}

// TestMainNoLateMessages: כתב שנוסף עם ההיסטוריה שלו — בשלוחה 1 נכנסים רק
// הציוצים החדשים (הישנים לא נשמעים ראשונים); בשלוחת הכתב — גם הישנים.
func TestMainNoLateMessages(t *testing.T) {
	now := time.Now().Unix()
	f := archiveServer([]FeedItem{
		{ID: 1, Channel: "a", TS: now - 7200, Text: "הודעה מטלגרם"},
	}, `{"channels":[{"name":"a","title":"אלישע ירד"}]}`)
	srv := httptest.NewServer(http.HandlerFunc(f.handler))
	defer srv.Close()
	cfg := newTestCfg(srv)
	st := &state{}
	if err := syncOnce(&cfg, st); err != nil { // שלוחה 1 קיימת, בלי טוויטר
		t.Fatal(err)
	}
	a := `"author":{"screen_name":"meiretingr","name":"מאיר אטינגר"}`
	fx := &fakeX{statuses: map[string]string{"meiretingr": `{"code":200,"results":[
	 {"type":"status","id":"500","text":"ציוץ משבוע שעבר","created_timestamp":` + xTS(5*24*time.Hour) + `,` + a + `,"reposted_by":null,"replying_to":null},
	 {"type":"status","id":"600","text":"ציוץ חדש עכשיו","created_timestamp":` + xTS(3*time.Minute) + `,` + a + `,"reposted_by":null,"replying_to":null}]}`}}
	withFakeX(t, fx)
	cfg.x = newXSource("meiretingr", "")
	if err := syncOnce(&cfg, st); err != nil {
		t.Fatal(err)
	}
	var main, rep []string
	for p, c := range f.files {
		switch {
		case strings.HasPrefix(p, "ivr2:/1/1") && strings.HasSuffix(p, ".tts"):
			main = append(main, c)
		case strings.HasPrefix(p, "ivr2:/2/2/1") && strings.HasSuffix(p, ".tts"):
			rep = append(rep, c)
		}
	}
	m, r := strings.Join(main, "\n"), strings.Join(rep, "\n")
	if !strings.Contains(m, "ציוץ חדש עכשיו") || strings.Contains(m, "ציוץ משבוע שעבר") {
		t.Errorf("ext 1:\n%s", m)
	}
	if !strings.Contains(r, "ציוץ חדש עכשיו") || !strings.Contains(r, "ציוץ משבוע שעבר") {
		t.Errorf("reporter ext:\n%s", r)
	}
}

// TestFixXBacklog: הציוצים הישנים שנכנסו לשלוחה 1 בהפעלה הראשונה של טוויטר — נמחקים
// משם (ולא נכנסים שוב); הודעות טלגרם וציוצים חדשים נשארים.
func TestFixXBacklog(t *testing.T) {
	f := archiveServer(nil, `{"channels":[]}`)
	srv := httptest.NewServer(http.HandlerFunc(f.handler))
	defer srv.Close()
	cfg := newTestCfg(srv)
	old := xLineStart - 86400
	ar := &archive{ext: "1", withName: true, entries: map[string]*archEntry{
		"x-meiretingr/500": {key: "x-meiretingr/500", base: 10000, ts: old},
		"a/7":              {key: "a/7", base: 10002, ts: old},
		"x-meiretingr/600": {key: "x-meiretingr/600", base: 10004, ts: xLineStart + 3600},
	}}
	for _, n := range []string{"10001.tts", "10003.tts", "10005.tts"} {
		f.files["ivr2:/1/"+n] = "x"
		f.dirs["ivr2:/1"] = append(f.dirs["ivr2:/1"], n)
		ar.addFile(n)
	}
	st := &state{arch: map[string]*archive{"1": ar}}
	st.fixXBacklog(&cfg)
	if !st.xFixed || ar.entries["x-meiretingr/500"].base != -1 || ar.entries["a/7"].base != 10002 || ar.entries["x-meiretingr/600"].base != 10004 {
		t.Fatalf("%+v %+v %+v", ar.entries["x-meiretingr/500"], ar.entries["a/7"], ar.entries["x-meiretingr/600"])
	}
	if _, ok := f.files["ivr2:/1/10001.tts"]; ok {
		t.Fatal("old tweet file still there")
	}
	if _, ok := f.files["ivr2:/1/10003.tts"]; !ok {
		t.Fatal("telegram message deleted")
	}
}

// TestLateInTimeOrder: עם המספור עם הרווחים — הודעה שמגיעה באיחור נכנסת לשלוחה 1
// במקום שלה לפי השעה (לא בראש), גם כשהיא ישנה מכמה ימים.
func TestLateInTimeOrder(t *testing.T) {
	archStride = 10
	defer func() { archStride = 2 }()
	now := time.Now().Unix()
	f := archiveServer([]FeedItem{
		{ID: 1, Channel: "a", TS: now - 4*86400, Text: "לפני ארבעה ימים מטלגרם"},
		{ID: 2, Channel: "a", TS: now - 2*86400, Text: "לפני יומיים מטלגרם"},
		{ID: 3, Channel: "a", TS: now - 600, Text: "לפני עשר דקות מטלגרם"},
	}, `{"channels":[{"name":"a","title":"אלישע ירד"}]}`)
	srv := httptest.NewServer(http.HandlerFunc(f.handler))
	defer srv.Close()
	cfg := newTestCfg(srv)
	st := &state{}
	if err := syncOnce(&cfg, st); err != nil {
		t.Fatal(err)
	}
	a := `"author":{"screen_name":"meiretingr","name":"מאיר אטינגר"}`
	fx := &fakeX{statuses: map[string]string{"meiretingr": `{"code":200,"results":[
	 {"type":"status","id":"500","text":"ציוץ משלשום","created_timestamp":` + xTS(3*24*time.Hour) + `,` + a + `,"reposted_by":null,"replying_to":null},
	 {"type":"status","id":"510","text":"ציוץ מאתמול","created_timestamp":` + xTS(30*time.Hour) + `,` + a + `,"reposted_by":null,"replying_to":null},
	 {"type":"status","id":"520","text":"ציוץ מלפני שעה","created_timestamp":` + xTS(time.Hour) + `,` + a + `,"reposted_by":null,"replying_to":null},
	 {"type":"status","id":"600","text":"ציוץ חדש עכשיו","created_timestamp":` + xTS(time.Minute) + `,` + a + `,"reposted_by":null,"replying_to":null}]}`}}
	withFakeX(t, fx)
	cfg.x = newXSource("meiretingr", "")
	if err := syncOnce(&cfg, st); err != nil {
		t.Fatal(err)
	}
	type file struct {
		n    int
		text string
	}
	var main []file
	for p, c := range f.files {
		if strings.HasPrefix(p, "ivr2:/1/1") && strings.HasSuffix(p, ".tts") {
			main = append(main, file{fileNum(strings.TrimPrefix(p, "ivr2:/1/")), c})
		}
	}
	sort.Slice(main, func(i, j int) bool { return main[i].n > main[j].n }) // כמו בהשמעה: הגבוה ראשון
	want := []string{"ציוץ חדש עכשיו", "לפני עשר דקות מטלגרם", "ציוץ מלפני שעה", "ציוץ מאתמול", "לפני יומיים מטלגרם", "ציוץ משלשום", "לפני ארבעה ימים מטלגרם"}
	if len(main) != len(want) {
		t.Fatalf("got %d files: %+v", len(main), main)
	}
	for i, w := range want {
		if !strings.Contains(main[i].text, w) {
			t.Errorf("place %d: want %q, got %d %q", i+1, w, main[i].n, main[i].text)
		}
	}
}

// TestFixXOrder: שלוחת כתב של טוויטר מהמבנה הקודם — מתרוקנת פעם אחת ונבנית מחדש
// לפי השעה, באותה שלוחה.
func TestFixXOrder(t *testing.T) {
	f := archiveServer(nil, `{"channels":[]}`)
	f.dirs["ivr2:/2/8"] = []string{"ext.ini", "99999.tts", "10001.tts", "10003.tts"}
	f.files["ivr2:/2/8/10001.tts"], f.files["ivr2:/2/8/10003.tts"] = "x", "y"
	f.files["ivr2:/2/8/archive.txt"] = "next=10004\nchannel=x-meiretingr\n"
	srv := httptest.NewServer(http.HandlerFunc(f.handler))
	defer srv.Close()
	cfg := newTestCfg(srv)
	st := &state{}
	st.ensureMaps()
	st.mapped = true
	st.chMap["x-meiretingr"] = "2/8"
	ar, err := loadArchive(&cfg, "2/8", "x-meiretingr", false, time.Now())
	if err != nil || !ar.oldLayout {
		t.Fatal(err, ar.oldLayout)
	}
	st.arch["2/8"] = ar
	st.fixXOrder(&cfg)
	if !st.xOrdered || st.chMap["x-meiretingr"] != "2/8" || st.arch["2/8"] != nil {
		t.Fatalf("ordered %v map %q arch %v", st.xOrdered, st.chMap["x-meiretingr"], st.arch["2/8"])
	}
	for _, n := range []string{"10001.tts", "10003.tts", "archive.txt"} {
		if _, ok := f.files["ivr2:/2/8/"+n]; ok {
			t.Errorf("%s still there", n)
		}
	}
	// אחרי הבנייה מחדש — המבנה החדש, לא מתרוקנת שוב
	ar2 := &archive{ext: "2/8", channel: "x-meiretingr", entries: map[string]*archEntry{}}
	if parseArchive(ar2.encode(time.Now())).layout != archLayout {
		t.Fatal("layout not written")
	}
}

// TestXMediaTab: ציוץ שמופיע רק בלשונית המדיה (השירות השמיט אותו מהרשימה הראשית) — נכנס.
func TestXMediaTab(t *testing.T) {
	a := `"author":{"screen_name":"ariel__danino","name":"אריאל דנינו"}`
	f := &fakeX{
		statuses: map[string]string{"ariel__danino": `{"code":200,"results":[
		 {"type":"status","id":"100","text":"ציוץ רגיל","created_timestamp":` + xTS(2*time.Hour) + `,` + a + `,"reposted_by":null,"replying_to":null}]}`},
		media: map[string]string{"ariel__danino": `{"code":200,"results":[
		 {"type":"status","id":"100","text":"ציוץ רגיל","created_timestamp":` + xTS(2*time.Hour) + `,` + a + `,"reposted_by":null,"replying_to":null},
		 {"type":"status","id":"200","text":"סרטון שחסר ברשימה","created_timestamp":` + xTS(time.Hour) + `,` + a + `,"reposted_by":null,"replying_to":null,
		  "media":{"videos":[{"url":"https://video.twimg.com/v.mp4","duration":30,"type":"video"}]}}]}`},
	}
	withFakeX(t, f)
	s := newXSource("ariel__danino", "")
	items, _ := s.poll(time.Now())
	if len(items) != 2 {
		t.Fatalf("want 2, got %d: %+v", len(items), items)
	}
	if s.videoURL("x-ariel__danino", 200) != "https://video.twimg.com/v.mp4" {
		t.Fatal("media-only tweet missing")
	}
}
