package main

import (
	"bytes"
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
