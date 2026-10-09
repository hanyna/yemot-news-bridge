package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

var ilLoc = func() *time.Location {
	l, err := time.LoadLocation("Asia/Jerusalem")
	if err != nil {
		l = time.FixedZone("IL", 3*3600)
	}
	return l
}()

func TestIsC14(t *testing.T) {
	if !isC14("c14://playlist/ABC") {
		t.Fatal("c14:// לא זוהה")
	}
	if isC14("https://example.com/feed") {
		t.Fatal("קישור רגיל זוהה בטעות כ-c14")
	}
}

func TestParseC14(t *testing.T) {
	s, err := parseC14("c14://playlist/PL123?start=21:00&len=93&keep=7")
	if err != nil {
		t.Fatalf("שגיאה: %v", err)
	}
	if s.playlist != "PL123" {
		t.Errorf("playlist=%q", s.playlist)
	}
	if s.start != [2]int{21, 0} {
		t.Errorf("start=%v", s.start)
	}
	if s.length != 93*time.Minute {
		t.Errorf("length=%v", s.length)
	}
	if s.keep != 7 {
		t.Errorf("keep=%d", s.keep)
	}
}

func TestParseC14Defaults(t *testing.T) {
	s, err := parseC14("c14://playlist/PL123")
	if err != nil {
		t.Fatalf("שגיאה: %v", err)
	}
	if s.start != [2]int{21, 0} || s.length != 93*time.Minute || s.keep != 0 {
		t.Errorf("ברירות מחדל שגויות: %+v", s)
	}
}

func TestParseC14Errors(t *testing.T) {
	for _, bad := range []string{
		"c14://playlist/",
		"c14://x/PL1",
		"c14://playlist/PL1?start=99:00",
		"c14://playlist/PL1?start=abc",
		"c14://playlist/PL1?len=0",
		"c14://playlist/PL1?len=999",
		"c14://playlist/PL1?keep=0",
	} {
		if _, err := parseC14(bad); err == nil {
			t.Errorf("ציפיתי לשגיאה עבור %q", bad)
		}
	}
}

func TestC14FragTimes(t *testing.T) {
	// 12 שניות → 3 קטעים של 4 שניות, באלפיות שנייה מאז 2001.
	start := int64(c14Epoch + 100)
	end := int64(c14Epoch + 112)
	got := c14FragTimes(start, end)
	if len(got) != 3 {
		t.Fatalf("מספר קטעים=%d, ציפיתי 3: %v", len(got), got)
	}
	if got[0] != 100000 || got[1] != 104000 || got[2] != 108000 {
		t.Errorf("זמנים=%v", got)
	}
}

// feed מדומה: XML של רשימת השמעה ביוטיוב.
func c14FeedXML(entries ...string) string {
	var b strings.Builder
	b.WriteString(`<?xml version="1.0"?><feed><title>הפטריוטים</title>`)
	for _, e := range entries {
		b.WriteString(e)
	}
	b.WriteString(`</feed>`)
	return b.String()
}

func c14Entry(title, published string) string {
	return fmt.Sprintf(`<entry><title>%s</title><published>%s</published></entry>`, title, published)
}

func TestFetchC14(t *testing.T) {
	now := time.Date(2026, 10, 7, 23, 0, 0, 0, ilLoc) // אחרי שידורי 6 ו-7 באוקטובר

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, c14FeedXML(
			// תוכנית מלאה אתמול (6.10) — בחלון, הסתיימה
			c14Entry("הפטריוטים עם ינון מגל | 6.10.2026 | התוכנית המלאה", "2026-10-06T20:23:55+00:00"),
			// קטע רגיל (לא "התוכנית המלאה") — לא נספר
			c14Entry("ריאיון סוער עם מישהו", "2026-10-06T19:51:39+00:00"),
			// תוכנית מלאה היום (7.10) — בחלון, הסתיימה
			c14Entry("הפטריוטים עם ינון מגל | 7.10.2026 | התוכנית המלאה", "2026-10-07T20:00:06+00:00"),
			// תוכנית ישנה מדי (לפני 4 ימים) — מחוץ לחלון החזרה
			c14Entry("הפטריוטים עם ינון מגל | 3.10.2026 | התוכנית המלאה", "2026-10-03T20:21:01+00:00"),
		))
	}))
	defer srv.Close()

	oldBase := c14FeedBase
	c14FeedBase = srv.URL + "/?playlist_id="
	defer func() { c14FeedBase = oldBase }()

	title, eps, err := fetchC14("c14://playlist/PL1?start=21:00&len=93", ilLoc, now)
	if err != nil {
		t.Fatalf("שגיאה: %v", err)
	}
	if title != "הפטריוטים" {
		t.Errorf("title=%q", title)
	}
	if len(eps) != 2 {
		t.Fatalf("מספר פרקים=%d, ציפיתי 2 (6.10 ו-7.10): %v", len(eps), epTitles(eps))
	}
	// מהישן לחדש: 6.10 לפני 7.10
	if time.Unix(eps[0].ts, 0).In(ilLoc).Day() != 6 || time.Unix(eps[1].ts, 0).In(ilLoc).Day() != 7 {
		t.Errorf("סדר/תאריכים שגויים: %v", epTitles(eps))
	}
	for _, e := range eps {
		if !strings.HasPrefix(e.src, "c14dvr:") {
			t.Errorf("src לא תקין: %q", e.src)
		}
		if e.title != "הפטריוטים עם ינון מגל" {
			t.Errorf("title של פרק=%q (ציפיתי בלי התאריך והסימון)", e.title)
		}
		if e.secs != 93*60 {
			t.Errorf("secs=%d", e.secs)
		}
	}
}

func TestFetchC14SkipsUnfinished(t *testing.T) {
	// עכשיו 21:30 — שידור של אותו יום (שמתחיל 21:00, אורך 93 דק') עוד לא הסתיים.
	now := time.Date(2026, 10, 7, 21, 30, 0, 0, ilLoc)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, c14FeedXML(
			c14Entry("הפטריוטים עם ינון מגל | 7.10.2026 | התוכנית המלאה", "2026-10-07T18:05:00+00:00"),
		))
	}))
	defer srv.Close()
	oldBase := c14FeedBase
	c14FeedBase = srv.URL + "/?playlist_id="
	defer func() { c14FeedBase = oldBase }()

	_, eps, err := fetchC14("c14://playlist/PL1?start=21:00&len=93", ilLoc, now)
	if err != nil {
		t.Fatalf("שגיאה: %v", err)
	}
	if len(eps) != 0 {
		t.Fatalf("ציפיתי 0 פרקים (השידור לא הסתיים), קיבלתי %d", len(eps))
	}
}

func TestFetchC14Dedup(t *testing.T) {
	now := time.Date(2026, 10, 7, 23, 0, 0, 0, ilLoc)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, c14FeedXML(
			c14Entry("הפטריוטים עם ינון מגל | 6.10.2026 | התוכנית המלאה", "2026-10-06T20:23:55+00:00"),
			c14Entry("הפטריוטים חלק ב | 6.10.2026 | התוכנית המלאה", "2026-10-06T21:00:00+00:00"),
		))
	}))
	defer srv.Close()
	oldBase := c14FeedBase
	c14FeedBase = srv.URL + "/?playlist_id="
	defer func() { c14FeedBase = oldBase }()

	_, eps, err := fetchC14("c14://playlist/PL1", ilLoc, now)
	if err != nil {
		t.Fatalf("שגיאה: %v", err)
	}
	if len(eps) != 1 {
		t.Fatalf("ציפיתי פרק אחד לכל תאריך, קיבלתי %d", len(eps))
	}
}

func TestDownloadC14DVR(t *testing.T) {
	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		w.Write([]byte("TS" + r.URL.Query().Get("startTime")))
	}))
	defer srv.Close()
	oldStream, oldClient := c14Stream, mediaClient
	c14Stream = srv.URL
	mediaClient = srv.Client()
	defer func() { c14Stream, mediaClient = oldStream, oldClient }()

	dir := t.TempDir()
	path := dir + "/out.ts"
	start := int64(c14Epoch + 1000)
	end := start + 12 // 3 קטעים
	if err := downloadC14DVR(fmt.Sprintf("c14dvr:%d:%d", start, end), path); err != nil {
		t.Fatalf("שגיאה: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if hits != 3 {
		t.Errorf("מספר הורדות=%d, ציפיתי 3", hits)
	}
	// כל קטע הוא "TS<ms>" — שלושתם צריכים להיות בקובץ, לפי הסדר.
	for _, ms := range []int64{1000000, 1004000, 1008000} {
		if !bytes.Contains(data, []byte(fmt.Sprintf("TS%d", ms))) {
			t.Errorf("חסר קטע %d בקובץ: %q", ms, data)
		}
	}
}

func TestDownloadC14DVRTolerateMissing(t *testing.T) {
	// קטע אחד מתוך הרבה נכשל — ההקלטה עדיין מצליחה.
	var n int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n++
		if r.URL.Query().Get("startTime") == "1004000" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Write([]byte("X"))
	}))
	defer srv.Close()
	oldStream, oldClient := c14Stream, mediaClient
	c14Stream = srv.URL
	mediaClient = srv.Client()
	defer func() { c14Stream, mediaClient = oldStream, oldClient }()

	start := int64(c14Epoch + 1000)
	end := start + 400 // 100 קטעים — קטע חסר אחד מותר
	path := t.TempDir() + "/o.ts"
	if err := downloadC14DVR(fmt.Sprintf("c14dvr:%d:%d", start, end), path); err != nil {
		t.Fatalf("קטע חסר בודד לא אמור להכשיל: %v", err)
	}
}

func epTitles(eps []*podEpisode) []string {
	var out []string
	for _, e := range eps {
		out = append(out, e.title+" @"+time.Unix(e.ts, 0).In(ilLoc).Format("2.1"))
	}
	return out
}

// עמוד רשימת השמעה ביוטיוב (המבנה של אוקטובר 2026), כשההזנה מחזירה 404.
func c14Page(titles ...string) string {
	var b strings.Builder
	b.WriteString(`<html><head><meta property="og:title" content="הפטריוטים"></head><body><script>var ytInitialData = {`)
	for _, t := range titles {
		q, _ := json.Marshal(t)
		fmt.Fprintf(&b, `{"lockupViewModel":{"metadata":{"lockupMetadataViewModel":{"title":{"content":%s},"image":{}}}}},`, q)
	}
	b.WriteString(`};</script></body></html>`)
	return b.String()
}

func withC14YouTube(t *testing.T, h http.HandlerFunc) {
	srv := httptest.NewServer(h)
	oldF, oldP := c14FeedBase, c14PageBase
	c14FeedBase, c14PageBase = srv.URL+"/feed?playlist_id=", srv.URL+"/page?list="
	t.Cleanup(func() { srv.Close(); c14FeedBase, c14PageBase = oldF, oldP })
}

func TestFetchC14PageWhenFeed404(t *testing.T) {
	withC14YouTube(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/feed" {
			w.WriteHeader(404)
			return
		}
		fmt.Fprint(w, c14Page(
			`ארז תדמור: "הוא לא יודע מה התפקיד שלו"`,
			"הפטריוטים עם ינון מגל | 7.10.2026 | התוכנית המלאה",
			"הפטריוטים עם ינון מגל | 06.10.2026 | התוכנית המלאה",
		))
	})
	now := time.Date(2026, 10, 8, 9, 0, 0, 0, ilLoc)
	name, eps, err := fetchC14("c14://playlist/PL1?start=21:00&len=93", ilLoc, now)
	if err != nil {
		t.Fatal(err)
	}
	if name != "הפטריוטים" || len(eps) != 2 || eps[1].title != "הפטריוטים עם ינון מגל" {
		t.Fatalf("name=%q eps=%v", name, epTitles(eps))
	}
}

func TestFetchC14Schedule(t *testing.T) {
	// יוטיוב: יש 7.10 (רביעי), אין 8.10 (חמישי). עכשיו שישי.
	withC14YouTube(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/feed" {
			w.WriteHeader(404)
			return
		}
		fmt.Fprint(w, c14Page("הפטריוטים עם ינון מגל | 7.10.2026 | התוכנית המלאה"))
	})
	link := "c14://playlist/PL1?start=21:00&len=93&days=0-4&wait=12"
	early := time.Date(2026, 10, 9, 8, 30, 0, 0, ilLoc) // 10 שעות אחרי סוף השידור — עוד מחכים ליוטיוב
	_, eps, err := fetchC14(link, ilLoc, early)
	if err != nil || len(eps) != 1 {
		t.Fatalf("early: %v %v", err, epTitles(eps))
	}
	later := time.Date(2026, 10, 9, 11, 0, 0, 0, ilLoc)
	_, eps, err = fetchC14(link, ilLoc, later)
	if err != nil || len(eps) != 2 {
		t.Fatalf("later: %v %v", err, epTitles(eps))
	}
	th := eps[1]
	if d := time.Unix(th.ts, 0).In(ilLoc); d.Day() != 8 || d.Hour() != 21 || th.title != "הפטריוטים עם ינון מגל" {
		t.Fatalf("thursday: %v %q", d, th.title)
	}
	// אותו מזהה כמו אם יוטיוב היה מעלה את הסרטון — אין כפילות כשהוא מופיע אחר כך
	withC14YouTube(t, func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, c14FeedXML(c14Entry("הפטריוטים עם ינון מגל | 8.10.2026 | התוכנית המלאה", "2026-10-09T10:00:00+00:00")))
	})
	_, eps2, _ := fetchC14(link, ilLoc, later)
	if len(eps2) != 2 || eps2[1].id != th.id {
		t.Fatalf("id mismatch: %v", epTitles(eps2))
	}
	// שישי ושבת לא בימי השידור; ויוטיוב למטה לגמרי — עדיין לפי הלוח
	withC14YouTube(t, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(500) })
	_, eps3, err := fetchC14(link, ilLoc, time.Date(2026, 10, 10, 10, 0, 0, 0, ilLoc))
	if err != nil || len(eps3) != 1 || time.Unix(eps3[0].ts, 0).In(ilLoc).Day() != 8 {
		t.Fatalf("saturday: %v %v", err, epTitles(eps3))
	}
	// בלי days — יוטיוב למטה זו שגיאה (כמו קודם)
	if _, _, err := fetchC14("c14://playlist/PL1", ilLoc, later); err == nil {
		t.Fatal("expected error without days")
	}
}

func TestParseC14Days(t *testing.T) {
	s, err := parseC14("c14://playlist/X?days=0-4")
	if err != nil || !s.days[0] || !s.days[4] || s.days[5] || s.days[6] || s.wait != 12*time.Hour {
		t.Fatalf("%+v %v", s, err)
	}
	if s, err := parseC14("c14://playlist/X?days=6,0&wait=3"); err != nil || !s.days[6] || !s.days[0] || s.days[1] || s.wait != 3*time.Hour {
		t.Fatalf("%+v %v", s, err)
	}
	for _, bad := range []string{"days=5-2", "days=7", "days=x", "wait=99"} {
		if _, err := parseC14("c14://playlist/X?" + bad); err == nil {
			t.Errorf("%s: no error", bad)
		}
	}
}

func TestC14NoFriday(t *testing.T) {
	withC14YouTube(t, func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, c14FeedXML(c14Entry("הפטריוטים | 9.10.2026 | התוכנית המלאה", "2026-10-09T20:00:00+00:00")))
	})
	// גם אם יוטיוב מראה תוכנית בשישי, וגם עם days=0-6 — אין פרק לשישי
	_, eps, err := fetchC14("c14://playlist/PL1?days=0-6&wait=0", ilLoc, time.Date(2026, 10, 9, 23, 30, 0, 0, ilLoc))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range eps {
		if time.Unix(e.ts, 0).In(ilLoc).Weekday() == time.Friday {
			t.Fatalf("friday episode: %v", epTitles(eps))
		}
	}
	for _, c := range []struct {
		t     time.Time
		quiet bool
	}{
		{time.Date(2026, 10, 9, 9, 0, 0, 0, ilLoc), false},  // שישי בבוקר — עוד מחפשים (לתוכנית של חמישי)
		{time.Date(2026, 10, 9, 20, 0, 0, 0, ilLoc), true},  // ליל שבת
		{time.Date(2026, 10, 10, 12, 0, 0, 0, ilLoc), true}, // שבת
		{time.Date(2026, 10, 10, 22, 30, 0, 0, ilLoc), false},
		{time.Date(2026, 10, 11, 23, 0, 0, 0, ilLoc), false},
	} {
		if got := c14Quiet(c.t, ilLoc); got != c.quiet {
			t.Errorf("%v: quiet=%v", c.t, got)
		}
	}
}

func TestFeedKeyC14(t *testing.T) {
	if feedKey("c14://playlist/A?start=21:00") != feedKey("c14://playlist/A?days=0-4&wait=6") || feedKey("c14://playlist/A") == feedKey("c14://playlist/B") {
		t.Fatal("feedKey")
	}
}
