package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// עמוד רשימת השמעה בסגנון יוטיוב (אוקטובר 2026): לכל סרטון בלוק lockupViewModel
// עם contentId, ואז קינון JSON (כפתורים), ואז הכותרת ב-lockupMetadataViewModel.
func ytPage(vids ...[2]string) string {
	var b strings.Builder
	b.WriteString(`<html><head><meta property="og:title" content="הפטריוטים בהגשת ינון מגל"></head><body><script>var x={"contents":[`)
	for _, v := range vids {
		b.WriteString(`{"lockupViewModel":{"contentId":"` + v[0] + `","rendererContext":{"commandContext":{"onTap":{"innertubeCommand":{"watchEndpoint":{"videoId":"` + v[0] + `"}}}}},"metadata":{"lockupMetadataViewModel":{"title":{"content":"` + v[1] + `"},"image":{"decoratedAvatarViewModel":{}}}}}},`)
	}
	b.WriteString(`]};</script></body></html>`)
	return b.String()
}

func withYTPage(t *testing.T, body string) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(body))
	}))
	old := c14PageBase
	c14PageBase = srv.URL + "/playlist?list="
	t.Cleanup(func() { srv.Close(); c14PageBase = old })
}

func TestC14PageVideos(t *testing.T) {
	withYTPage(t, ytPage(
		[2]string{"BBW14nZBPjk", "הפטריוטים עם ינון מגל | 10.10.2026 | התוכנית המלאה"},
		[2]string{"RiPda3N8N-4", "דיצה אור בפטריוטים: ריאיון"},
		[2]string{"99Yq0j12NXg", "הפטריוטים עם ינון מגל | 7.10.2026 | התוכנית המלאה"},
	))
	name, vids, err := c14PageVideos("PL1")
	if err != nil {
		t.Fatal(err)
	}
	if name != "הפטריוטים בהגשת ינון מגל" {
		t.Errorf("name=%q", name)
	}
	if len(vids) != 3 || vids[0].id != "BBW14nZBPjk" || vids[0].title != "הפטריוטים עם ינון מגל | 10.10.2026 | התוכנית המלאה" {
		t.Fatalf("vids=%+v", vids)
	}
	if vids[2].id != "99Yq0j12NXg" {
		t.Errorf("third id=%q", vids[2].id)
	}
}

func TestC14PageVideosEmpty(t *testing.T) {
	withYTPage(t, `<html><body>no videos here</body></html>`)
	if _, _, err := c14PageVideos("PL1"); err == nil {
		t.Fatal("expected error on empty page")
	}
}

// המזהה חייב להיות זהה לזה שהגשר יוצר ב-fetchC14 (אותו קלט → אותו מזהה).
func TestC14EpisodeIDMatchesBridge(t *testing.T) {
	withC14YouTube(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/feed" {
			w.WriteHeader(404)
			return
		}
		w.Write([]byte(ytPage([2]string{"AAAAAAAAAAA", "הפטריוטים עם ינון מגל | 7.10.2026 | התוכנית המלאה"})))
	})
	// withC14YouTube מגדיר c14PageBase ל-/page?list= — נתאים את העמוד לכך
	now := time.Date(2026, 10, 8, 9, 0, 0, 0, ilLoc)
	_, eps, err := fetchC14("c14://playlist/PLX?start=21:00&len=93", ilLoc, now)
	if err != nil || len(eps) != 1 {
		t.Fatalf("fetchC14: %v %v", err, epTitles(eps))
	}
	want := c14EpisodeID("PLX", "2026-10-07")
	if eps[0].id != want {
		t.Fatalf("id mismatch: bridge=%q helper=%q", eps[0].id, want)
	}
}

func TestPatriotsIntro(t *testing.T) {
	s := patriotsIntro("הפטריוטים עם ינון מגל", time.Date(2026, 10, 8, 21, 0, 0, 0, ilLoc), 93*60)
	if !strings.HasPrefix(s, "הפטריוטים עם ינון מגל. פורסם ב 8 באוקטובר") || !strings.Contains(s, "שעה ו 33 דקות") {
		t.Fatalf("intro=%q", s)
	}
}

func TestFilterSearchLines(t *testing.T) {
	out := strings.Join([]string{
		"Z3z0C86j2G0|C14|הפטריוטים עם יונן מגל | 5.11.2026 | התוכנית המלאה", // המטרה (כולל '|' בכותרת)
		"AAAAAAAAAAA|C14|הפטריוטים עם ינון מגל | 4.11.2026 | התוכנית המלאה", // יום אחר
		"BBBBBBBBBBB|ערוץ אחר|הפטריוטים עם ינון מגל | 5.11.2026 | התוכנית המלאה", // העתקה מערוץ אחר
		"CCCCCCCCCCC|C14|הפטריוטים: קטע קצר 5.11.2026",                      // לא תוכנית מלאה
		"DDDDDDDDDDD|C14|תוכנית אחרת | 5.11.2026 | התוכנית המלאה",            // תוכנית אחרת
		"bad line",
	}, "\n")
	got := filterSearchLines(out, "הפטריוטים", "C14", "5.11.2026")
	if len(got) != 1 || got[0].id != "Z3z0C86j2G0" {
		t.Fatalf("ציפיתי בדיוק לתוצאה אחת (Z3z0C86j2G0), קיבלתי %+v", got)
	}
	if got[0].title != "הפטריוטים עם יונן מגל | 5.11.2026 | התוכנית המלאה" {
		t.Errorf("הכותרת נחתכה בטעות ב-'|': %q", got[0].title)
	}
	// בלי סינון ערוץ — גם ההעתקה נכנסת.
	if n := len(filterSearchLines(out, "הפטריוטים", "", "5.11.2026")); n != 2 {
		t.Errorf("בלי סינון ערוץ ציפיתי ל-2, קיבלתי %d", n)
	}
}

func TestNormDate(t *testing.T) {
	m := reC14Date.FindStringSubmatch("x | 06.10.2026 | y")
	if got := normDate(m); got != "6.10.2026" {
		t.Errorf("normDate=%q, רצינו 6.10.2026", got)
	}
}
