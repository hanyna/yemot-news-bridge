package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// בבדיקות הרגילות — בלי Nitter אמיתי (כל בדיקה שצריכה — מגדירה שרת מדומה).
func init() { xNitterHosts = nil }

func rssDate(ago time.Duration) string {
	return time.Now().Add(-ago).UTC().Format(http.TimeFormat)
}

// הזנה בצורה ש-nitter.meowing.monster מחזיר (נבדק ב-6.10.2026).
func meirRSS() string {
	return `<?xml version="1.0" encoding="UTF-8"?>
<rss xmlns:atom="http://www.w3.org/2005/Atom" xmlns:dc="http://purl.org/dc/elements/1.1/" version="2.0">
  <channel>
    <title>מאיר אטינגר / @meiretingr</title>
      <item>
        <title>הזדמנות לציין</title>
        <dc:creator>@meiretingr</dc:creator>
        <description><![CDATA[<p>הזדמנות לציין: התכנית כבר לא באתר</p>
<hr/>
<blockquote>
<b>הקונספציה (@haconcepzia)</b>
<p>
<p>הסדרה בעזה היא אינטרס ישראלי</p>
<img src="https://pbs.twimg.com/media/HT9LYliXkAAgvs-.jpg" style="max-width:250px;" />
</p>
<footer>
— <cite><a href="http://nitter.x/haconcepzia/status/2107486039199600669#m">http://nitter.x/haconcepzia/status/2107486039199600669#m</a>
</footer>
</blockquote>]]></description>
        <pubDate>` + rssDate(10*time.Minute) + `</pubDate>
        <guid isPermaLink="false">2107513324569014775</guid>
      </item>
      <item>
        <title>RT by @meiretingr: בוקר קשה בהתיישבות</title>
        <dc:creator>@KYehudim45047</dc:creator>
        <description><![CDATA[<p>בוקר קשה בהתיישבות: ארבעה בתי משפחות הוחרבו</p>
<a href="http://nitter.x/KYehudim45047/status/2107395226335838337#m">
<br>Video<br>
  <img src="https://pbs.twimg.com/amplify_video_thumb/2107395181733437440/img/vY5P.jpg" style="max-width:250px;" />
</a>]]></description>
        <pubDate>` + rssDate(20*time.Minute) + `</pubDate>
        <guid isPermaLink="false">2107395226335838337</guid>
      </item>
      <item>
        <title>תמונה</title>
        <dc:creator>@meiretingr</dc:creator>
        <description><![CDATA[<p>קבלו את האבסורד הבא</p>
<img src="https://pbs.twimg.com/media/HSAAA.jpg" style="max-width:250px;" />]]></description>
        <pubDate>` + rssDate(30*time.Minute) + `</pubDate>
        <guid isPermaLink="false">2107300000000000000</guid>
      </item>
  </channel>
</rss>`
}

type fakeNitter struct {
	mu    sync.Mutex
	feeds map[string]string // handle → RSS
	down  bool
	calls int
}

func (f *fakeNitter) handler(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if f.down {
		w.Write([]byte(`<!doctype html><html><title>Making sure you're not a bot!</title></html>`))
		return
	}
	h := strings.TrimSuffix(strings.Trim(r.URL.Path, "/"), "/rss")
	if b, ok := f.feeds[h]; ok {
		w.Header().Set("Content-Type", "application/rss+xml")
		w.Write([]byte(b))
		return
	}
	w.WriteHeader(404)
}

func withNitter(t *testing.T, hosts ...string) {
	old := xNitterHosts
	xNitterHosts = hosts
	t.Cleanup(func() { xNitterHosts = old })
}

// FxTwitter אומר "לא נמצא" (כמו שקורה לו לפעמים) — הציוצים מגיעים מ-Nitter, ומושלמים
// מהציוץ הבודד ב-FxTwitter (סרטון, שם מלא). ואם גם הוא לא עונה — כמו שהם מ-Nitter.
func TestXNitterWhenFxFails(t *testing.T) {
	fx := &fakeX{}
	withFakeX(t, fx)
	bad := httptest.NewServer(http.HandlerFunc((&fakeNitter{down: true}).handler))
	defer bad.Close()
	n := &fakeNitter{feeds: map[string]string{"meiretingr": meirRSS()}}
	good := httptest.NewServer(http.HandlerFunc(n.handler))
	defer good.Close()
	withNitter(t, bad.URL, good.URL)

	s := newXSource("meiretingr", "")
	items, chans := s.poll(time.Now())
	a := s.accts[0]
	if a.fails != 0 || a.lastErr != "" {
		t.Fatalf("should not fail when Nitter works: %+v", a)
	}
	if chans[0].Title != "מאיר אטינגר" {
		t.Fatalf("title %q", chans[0].Title)
	}
	got := map[int]FeedItem{}
	for _, it := range items {
		got[it.ID] = it
	}
	if len(got) != 3 {
		t.Fatalf("want 3, got %d: %+v", len(got), items)
	}
	q := got[2107513324569014775]
	if q.Text != "הזדמנות לציין: התכנית כבר לא באתר\n\nציטוט של הקונספציה: הסדרה בעזה היא אינטרס ישראלי" || q.Channel != "x-meiretingr" {
		t.Fatalf("quote: %q", q.Text)
	}
	if time.Since(time.Unix(q.TS, 0)) > 11*time.Minute {
		t.Fatalf("time %v", time.Unix(q.TS, 0))
	}
	rt := got[2107395226335838337]
	if !strings.HasPrefix(rt.Text, "שיתף ציוץ של KYehudim45047: בוקר קשה בהתיישבות") || !strings.Contains(rt.HTML, "vidwrap") {
		t.Fatalf("retweet: %q %s", rt.Text, rt.HTML)
	}
	if p := got[2107300000000000000]; !strings.Contains(p.HTML, `<div class="photo"><img src="https://pbs.twimg.com/media/HSAAA.jpg"`) {
		t.Fatalf("photo: %s", p.HTML)
	}
	// האתר שעבד נשמר — הבדיקה הבאה הולכת ישר אליו
	if xNitterHosts[s.nitterCur] != good.URL {
		t.Fatal("did not stick to the working host")
	}
}

func TestXNitterEnrichedFromFx(t *testing.T) {
	a := `"author":{"screen_name":"KYehudim45047","name":"די לכיבוש הערבי של ארץ ישראל!"}`
	fx := &fakeX{statusByID: map[string]string{
		"2107395226335838337": `{"code":200,"status":{"type":"status","id":"2107395226335838337","text":"בוקר קשה בהתיישבות","created_timestamp":` + xTS(20*time.Minute) + `,` + a + `,"replying_to":null,
		 "media":{"videos":[{"type":"video","url":"https://video.twimg.com/v.mp4","duration":42}]}}}`,
	}}
	withFakeX(t, fx)
	n := &fakeNitter{feeds: map[string]string{"meiretingr": meirRSS()}}
	good := httptest.NewServer(http.HandlerFunc(n.handler))
	defer good.Close()
	withNitter(t, good.URL)

	s := newXSource("meiretingr", "")
	s.poll(time.Now())
	it := s.accts[0].items[2107395226335838337]
	if it.Text != "שיתף ציוץ של די לכיבוש הערבי של ארץ ישראל!: בוקר קשה בהתיישבות" {
		t.Fatalf("enriched text: %q", it.Text)
	}
	if s.videoURL("x-meiretingr", it.ID) != "https://video.twimg.com/v.mp4" {
		t.Fatal("video from FxTwitter missing")
	}
	// בבדיקה הבאה — לא מבקשים שוב את מה שכבר יש
	fx.mu.Lock()
	before := fx.calls
	fx.mu.Unlock()
	s.poll(time.Now().Add(3 * time.Minute))
	fx.mu.Lock()
	defer fx.mu.Unlock()
	if extra := fx.calls - before; extra > 3 { // רשימה + "לא נמצא" + מדיה; לא בקשות לציוצים בודדים
		t.Fatalf("asked again for known tweets: %d calls", extra)
	}
}

// כל אתרי ה-Nitter נופלים — FxTwitter לבד, כמו קודם; אתר שנכשל לא נבדק בכל סבב.
func TestXNitterAllDown(t *testing.T) {
	fx := &fakeX{statuses: map[string]string{"ariel__danino": danioStatuses("https://pbs.twimg.com/media/p.jpg")}}
	withFakeX(t, fx)
	n := &fakeNitter{down: true}
	bad := httptest.NewServer(http.HandlerFunc(n.handler))
	defer bad.Close()
	withNitter(t, bad.URL)
	s := newXSource("ariel__danino", "")
	items, _ := s.poll(time.Now())
	if len(items) != 6 || s.accts[0].fails != 0 {
		t.Fatalf("fx alone: %d items, fails %d", len(items), s.accts[0].fails)
	}
	s.poll(time.Now().Add(3 * time.Minute))
	if n.calls != 1 {
		t.Fatalf("bad host retried too soon: %d", n.calls)
	}
}

func TestSetNitterHosts(t *testing.T) {
	old := xNitterHosts
	defer func() { xNitterHosts = old }()
	setNitterHosts("off")
	if xNitterHosts != nil {
		t.Fatal("off")
	}
	setNitterHosts("https://nitter.a.org/, nitter.b.net")
	if len(xNitterHosts) != 2 || xNitterHosts[0] != "nitter.a.org" || xNitterHosts[1] != "nitter.b.net" {
		t.Fatalf("%v", xNitterHosts)
	}
	setNitterHosts("")
	if len(xNitterHosts) == 0 || xNitterHosts[0] != xNitterDefault[0] {
		t.Fatal("default")
	}
}
