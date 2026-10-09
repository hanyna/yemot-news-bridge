package main

// תוכנית טלוויזיה מערוץ 14 כפודקאסט בקו (למשל "הפטריוטים" בשלוחה 3/3).
//
// שורה ב-PODCASTS עם קישור מיוחד במקום הזנת RSS:
//
//	הפטריוטים | c14://playlist/<מזהה רשימת השמעה ביוטיוב>?start=21:00&len=93&keep=7
//
// איך זה עובד: ערוץ 14 לא מפרסמים את התוכניות כפודקאסט, ויוטיוב חוסם הורדות
// משרתים. אבל לשידור החי של הערוץ יש חלון חזרה של יומיים ב-CDN (redge), שממנו
// אפשר להוריד כל קטע לפי זמן. לכן:
//
//  1. ההזנה הציבורית של רשימת ההשמעה ביוטיוב (XML פתוח, בלי מפתח) משמשת רק
//     כדי לדעת שהתוכנית שודרה ובאיזה תאריך — מחפשים "התוכנית המלאה" עם תאריך
//     בכותרת. ביום שאין תוכנית (שישי) — אין סרטון, ואין פרק.
//  2. הקול של השידור עצמו (start עד start+len, שעון ישראל) יורד מהשידור החי
//     קטע-קטע (fragment.ts של 4 שניות, רק הקול), מודבק, ועולה לשלוחה כפרק —
//     באותו מסלול כמו כל פרק פודקאסט (audio.go).
//
// פרמטרים בקישור: start — שעת תחילת השידור (ברירת מחדל 21:00); len — אורך
// ההקלטה בדקות (ברירת מחדל 93); keep — כמה פרקים נשמרים בשלוחה (אם חסר —
// PODCAST_KEEP); days — ימי השידור הקבועים (0=ראשון ... 6=שבת, למשל days=0-4);
// wait — כמה שעות אחרי סוף השידור מחכים ליוטיוב (ברירת מחדל 12).
// תוכנית ששודרה לפני יותר מ-c14DVRMax כבר לא ב-CDN — מדלגים.
//
// ההזנה (RSS) של רשימת השמעה ביוטיוב החזירה 404 (אוקטובר 2026), לכן אם היא לא
// עובדת קוראים את עמוד הרשימה עצמו. ויוטיוב לא תמיד מעלה את התוכנית המלאה (בעיקר
// בחמישי): ביום שידור קבוע (days) שעבר wait שעות מסוף השידור ועדיין אין סרטון —
// מקליטים לפי לוח השידורים, כדי לא לפספס את חלון החזרה.

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"hash/fnv"
	"html"
	"io"
	"log"
	"net/http"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	c14Scheme = "c14://"
	c14DVRMax = 42 * time.Hour   // עומק חלון החזרה שסומכים עליו (נבדק: יומיים)
	c14Epoch  = 978307200        // זמני הקטעים ב-CDN נספרים מ-1.1.2001 (אלפיות שנייה)
	c14Frag   = 4                // אורך קטע בשניות
	c14Total  = 45 * time.Minute // מגבלת זמן להורדת תוכנית שלמה
	c14Full   = "התוכנית המלאה"  // הסימן בכותרת ביוטיוב שזו תוכנית שלמה
)

// c14Stream: כתובת הבסיס של השידור החי. משתנה — לבדיקות ולעקיפה (C14_STREAM).
var c14Stream = "https://r.il.cdn-redge.media/livehls/oil/ch14/live/ch14/live.livx"

// c14FeedBase: הזנת רשימת השמעה ביוטיוב. משתנה — לבדיקות.
var c14FeedBase = "https://www.youtube.com/feeds/videos.xml?playlist_id="

// c14PageBase: עמוד רשימת ההשמעה ביוטיוב — כשההזנה לא עובדת. משתנה — לבדיקות.
var c14PageBase = "https://www.youtube.com/playlist?list="

type c14Source struct {
	playlist string
	start    [2]int // שעה, דקה (שעון ישראל)
	length   time.Duration
	keep     int
	days     [7]bool       // ימי שידור קבועים (time.Weekday) — להקלטה גם בלי סרטון ביוטיוב
	wait     time.Duration // כמה מחכים ליוטיוב אחרי סוף השידור
}

func isC14(link string) bool { return strings.HasPrefix(link, c14Scheme) }

// parseC14 מפרק קישור c14://playlist/<id>?start=21:00&len=93&keep=7.
func parseC14(link string) (c14Source, error) {
	s := c14Source{start: [2]int{21, 0}, length: 93 * time.Minute, wait: 12 * time.Hour}
	rest, ok := strings.CutPrefix(link, c14Scheme+"playlist/")
	if !ok {
		return s, fmt.Errorf("קישור c14 לא מובן (צריך c14://playlist/<מזהה>): %q", link)
	}
	q := ""
	if i := strings.IndexByte(rest, '?'); i >= 0 {
		rest, q = rest[:i], rest[i+1:]
	}
	if rest == "" {
		return s, fmt.Errorf("קישור c14 בלי מזהה רשימת השמעה: %q", link)
	}
	s.playlist = rest
	for _, kv := range strings.Split(q, "&") {
		k, v, _ := strings.Cut(kv, "=")
		switch k {
		case "start":
			h, m, ok := strings.Cut(v, ":")
			hh, err1 := strconv.Atoi(h)
			mm, err2 := strconv.Atoi(m)
			if !ok || err1 != nil || err2 != nil || hh < 0 || hh > 23 || mm < 0 || mm > 59 {
				return s, fmt.Errorf("שעת התחלה לא מובנת (צריך למשל start=21:00): %q", v)
			}
			s.start = [2]int{hh, mm}
		case "len":
			n, err := strconv.Atoi(v)
			if err != nil || n < 5 || n > 240 {
				return s, fmt.Errorf("אורך לא מובן (דקות, 5 עד 240): %q", v)
			}
			s.length = time.Duration(n) * time.Minute
		case "keep":
			n, err := strconv.Atoi(v)
			if err != nil || n < 1 || n > 50 {
				return s, fmt.Errorf("keep לא מובן (1 עד 50): %q", v)
			}
			s.keep = n
		case "days":
			d, err := parseC14Days(v)
			if err != nil {
				return s, err
			}
			s.days = d
		case "wait":
			n, err := strconv.Atoi(v)
			if err != nil || n < 0 || n > 36 {
				return s, fmt.Errorf("wait לא מובן (שעות, 0 עד 36): %q", v)
			}
			s.wait = time.Duration(n) * time.Hour
		}
	}
	return s, nil
}

// parseC14Days: "0-4" או "0,1,2,3,4" (0=ראשון ... 6=שבת).
func parseC14Days(v string) ([7]bool, error) {
	var d [7]bool
	bad := fmt.Errorf("days לא מובן (למשל days=0-4, כש-0 הוא יום ראשון): %q", v)
	for _, part := range strings.Split(v, ",") {
		a, b, rng := strings.Cut(strings.TrimSpace(part), "-")
		from, err1 := strconv.Atoi(a)
		to := from
		var err2 error
		if rng {
			to, err2 = strconv.Atoi(b)
		}
		if err1 != nil || err2 != nil || from < 0 || to > 6 || from > to {
			return d, bad
		}
		for i := from; i <= to; i++ {
			d[i] = true
		}
	}
	return d, nil
}

func (s c14Source) anyDay() bool {
	for _, d := range s.days {
		if d {
			return true
		}
	}
	return false
}

// reC14Date: תאריך בכותרת ביוטיוב — "06.10.2026" או "6.10.2026".
var reC14Date = regexp.MustCompile(`(\d{1,2})\.(\d{1,2})\.(\d{4})`)

type ytFeed struct {
	Title   string `xml:"title"`
	Entries []struct {
		Title     string `xml:"title"`
		Published string `xml:"published"`
	} `xml:"entry"`
}

// fetchC14 מחזיר פרק לכל תוכנית מלאה שעדיין בחלון החזרה של ה-CDN, מהישנה
// לחדשה: לפי הסרטונים ברשימת ההשמעה ביוטיוב, ובימי השידור הקבועים (days) — גם
// בלי סרטון, אחרי wait שעות.
func fetchC14(link string, loc *time.Location, now time.Time) (string, []*podEpisode, error) {
	src, err := parseC14(link)
	if err != nil {
		return "", nil, err
	}
	name, titles, err := c14Titles(src.playlist)
	if err != nil {
		if !src.anyDay() {
			return "", nil, err
		}
		log.Printf("הערה: רשימת ההשמעה של ערוץ 14 ביוטיוב לא נקראה (%v) — ממשיך לפי לוח השידורים.", err)
	}
	seen := map[string]bool{}
	var eps []*podEpisode
	add := func(start time.Time, title string) {
		if start.Weekday() == time.Friday {
			return // בליל שבת אין תוכנית — גם אם משהו מופיע ביוטיוב
		}
		day := start.Format("2006-01-02")
		end := start.Add(src.length)
		if seen[day] || now.Sub(start) > c14DVRMax || now.Sub(end) < 5*time.Minute {
			return // כבר יש, יצא מחלון החזרה, או שהשידור עוד לא הסתיים
		}
		seen[day] = true
		h := fnv.New64a()
		fmt.Fprintf(h, "c14:%s:%s", src.playlist, day)
		eps = append(eps, &podEpisode{
			id:    fmt.Sprintf("%016x", h.Sum64()),
			ts:    start.Unix(),
			title: title,
			secs:  int(src.length.Seconds()),
			src:   fmt.Sprintf("c14dvr:%d:%d", start.Unix(), end.Unix()),
		})
	}
	show := "" // שם התוכנית מהסרטון האחרון ("הפטריוטים עם ינון מגל") — לפרקים לפי לוח השידורים
	for _, t := range titles {
		if !strings.Contains(t, c14Full) {
			continue
		}
		m := reC14Date.FindStringSubmatch(t)
		if m == nil {
			continue
		}
		d, _ := strconv.Atoi(m[1])
		mo, _ := strconv.Atoi(m[2])
		y, _ := strconv.Atoi(m[3])
		if mo < 1 || mo > 12 || d < 1 || d > 31 {
			continue
		}
		title := strings.TrimSpace(t)
		if i := strings.IndexByte(title, '|'); i > 0 {
			title = strings.TrimSpace(title[:i]) // "הפטריוטים עם ינון מגל | 6.10 | התוכנית המלאה" → השם בלבד; התאריך מוקרא ממילא
		}
		if show == "" {
			show = title
		}
		add(time.Date(y, time.Month(mo), d, src.start[0], src.start[1], 0, 0, loc), title)
	}
	if show == "" {
		show = name
	}
	if src.anyDay() { // ימי שידור קבועים בלי סרטון ביוטיוב
		today := now.In(loc)
		for back := 0; back <= 2; back++ {
			day := today.AddDate(0, 0, -back)
			start := time.Date(day.Year(), day.Month(), day.Day(), src.start[0], src.start[1], 0, 0, loc)
			if !src.days[start.Weekday()] || seen[start.Format("2006-01-02")] || now.Sub(start.Add(src.length)) < src.wait {
				continue
			}
			add(start, show)
		}
	}
	sort.SliceStable(eps, func(i, j int) bool { return eps[i].ts < eps[j].ts })
	return name, eps, nil
}

// c14Titles: שם רשימת ההשמעה וכותרות הסרטונים בה (החדשים קודם) — מההזנה (RSS),
// ואם היא לא עובדת — מעמוד הרשימה.
func c14Titles(playlist string) (string, []string, error) {
	name, titles, err := c14FeedTitles(playlist)
	if err == nil {
		return name, titles, nil
	}
	name, titles, err2 := c14PageTitles(playlist)
	if err2 != nil {
		return "", nil, fmt.Errorf("ההזנה: %v; העמוד: %v", err, err2)
	}
	return name, titles, nil
}

func c14Get(u, ua string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", ua)
	req.Header.Set("Accept-Language", "he,en;q=0.5")
	req.Header.Set("Cookie", "CONSENT=YES+1") // בלי דף ההסכמה לעוגיות
	resp, err := feedHTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("סטטוס %d", resp.StatusCode)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 8<<20))
}

func c14FeedTitles(playlist string) (string, []string, error) {
	body, err := c14Get(c14FeedBase+playlist, "yemot-news-bridge (playlist feed reader)")
	if err != nil {
		return "", nil, err
	}
	var f ytFeed
	if err := xml.Unmarshal(body, &f); err != nil {
		return "", nil, fmt.Errorf("הזנה לא תקינה: %w", err)
	}
	var titles []string
	for _, e := range f.Entries {
		titles = append(titles, e.Title)
	}
	return strings.TrimSpace(f.Title), titles, nil
}

var (
	reYTLockup = regexp.MustCompile(`"lockupMetadataViewModel":\{"title":\{"content":("(?:[^"\\]|\\.)*")`)
	reYTOld    = regexp.MustCompile(`"playlistVideoRenderer":\{"videoId":"[^"]*".*?"title":\{"runs":\[\{"text":("(?:[^"\\]|\\.)*")`)
	reYTName   = regexp.MustCompile(`<meta property="og:title" content="([^"]*)"`)
)

func c14PageTitles(playlist string) (string, []string, error) {
	body, err := c14Get(c14PageBase+playlist, "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0 Safari/537.36")
	if err != nil {
		return "", nil, err
	}
	page := string(body)
	ms := reYTLockup.FindAllStringSubmatch(page, -1)
	if len(ms) == 0 {
		ms = reYTOld.FindAllStringSubmatch(page, -1)
	}
	var titles []string
	for _, m := range ms {
		var t string
		if json.Unmarshal([]byte(m[1]), &t) == nil {
			titles = append(titles, t)
		}
	}
	if len(titles) == 0 {
		return "", nil, errors.New("לא נמצאו סרטונים בעמוד (יוטיוב שינה את המבנה?)")
	}
	name := ""
	if m := reYTName.FindStringSubmatch(page); m != nil {
		name = strings.TrimSpace(html.UnescapeString(m[1]))
	}
	return name, titles, nil
}

// c14Quiet: משישי בצהריים עד מוצאי שבת (22:00) לא מחפשים תוכניות של ערוץ 14 —
// אין שידור, ומה שהיה בחמישי כבר נכנס.
func c14Quiet(now time.Time, loc *time.Location) bool {
	t := now.In(loc)
	switch t.Weekday() {
	case time.Friday:
		return t.Hour() >= 12
	case time.Saturday:
		return t.Hour() < 22
	}
	return false
}

// isC14DVR: כתובת הקלטה מהשידור החי (c14dvr:<מאיזה זמן>:<עד איזה זמן>).
func isC14DVR(src string) bool { return strings.HasPrefix(src, "c14dvr:") }

// c14FragTimes: זמני הקטעים (באלפיות שנייה מאז 2001) שמרכיבים את ההקלטה.
func c14FragTimes(startUnix, endUnix int64) []int64 {
	var out []int64
	for t := (startUnix - c14Epoch) / c14Frag * c14Frag; t < endUnix-c14Epoch; t += c14Frag {
		out = append(out, t*1000)
	}
	return out
}

// downloadC14DVR מוריד את הקול של השידור, קטע-קטע, לקובץ אחד.
// כמה קטעים חסרים — לא נורא (קפיצה קטנה); יותר מ-5% חסרים — ננסה שוב מאוחר יותר.
func downloadC14DVR(src, path string) error {
	var startUnix, endUnix int64
	if _, err := fmt.Sscanf(src, "c14dvr:%d:%d", &startUnix, &endUnix); err != nil {
		return &permanentError{fmt.Errorf("כתובת הקלטה לא מובנת: %q", src)}
	}
	frags := c14FragTimes(startUnix, endUnix)
	if len(frags) == 0 {
		return &permanentError{fmt.Errorf("הקלטה ריקה: %q", src)}
	}
	w, err := os.Create(path)
	if err != nil {
		return err
	}
	defer w.Close()
	ctx, cancel := context.WithTimeout(context.Background(), c14Total)
	defer cancel()
	const workers = 4
	missed := 0
	for i := 0; i < len(frags); i += workers {
		batch := frags[i:min(i+workers, len(frags))]
		data := make([][]byte, len(batch))
		errs := make([]error, len(batch))
		done := make(chan int, len(batch))
		for k, ms := range batch {
			go func(k int, ms int64) {
				data[k], errs[k] = c14Fragment(ctx, ms)
				done <- k
			}(k, ms)
		}
		for range batch {
			<-done
		}
		for k := range batch {
			if errs[k] != nil {
				if ctx.Err() != nil {
					return fmt.Errorf("ההורדה לא הסתיימה בזמן (%v): %v", c14Total, errs[k])
				}
				missed++
				if missed > len(frags)/20+1 {
					return &retryLaterError{fmt.Errorf("יותר מדי קטעים חסרים בשידור (%d): %v", missed, errs[k])}
				}
				continue
			}
			if _, err := w.Write(data[k]); err != nil {
				return err
			}
		}
	}
	if missed > 0 {
		log.Printf("הערה: בהקלטה מהשידור של ערוץ 14 חסרים %d קטעים של %d שניות — ההקלטה עלתה בלעדיהם.", missed, c14Frag)
	}
	return nil
}

// c14Fragment מוריד קטע אחד (רק קול), עם ניסיונות חוזרים.
func c14Fragment(ctx context.Context, ms int64) ([]byte, error) {
	u := fmt.Sprintf("%s/fragment.ts?audioId=1&startTime=%d", c14Stream, ms)
	var last error
	for try := 0; try < 3; try++ {
		if try > 0 {
			select {
			case <-time.After(2 * time.Second):
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0 Safari/537.36")
		resp, err := mediaClient.Do(req)
		if err != nil {
			last = err
			continue
		}
		if resp.StatusCode != http.StatusOK {
			resp.Body.Close()
			last = fmt.Errorf("סטטוס %d", resp.StatusCode)
			continue
		}
		b, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
		resp.Body.Close()
		if err != nil || len(b) == 0 {
			last = fmt.Errorf("קטע ריק או קטוע: %v", err)
			continue
		}
		return b, nil
	}
	return nil, last
}
