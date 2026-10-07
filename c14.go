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
// PODCAST_KEEP). תוכנית ששודרה לפני יותר מ-c14DVRMax כבר לא ב-CDN — מדלגים.

import (
	"context"
	"encoding/xml"
	"fmt"
	"hash/fnv"
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
	c14DVRMax = 42 * time.Hour      // עומק חלון החזרה שסומכים עליו (נבדק: יומיים)
	c14Epoch  = 978307200           // זמני הקטעים ב-CDN נספרים מ-1.1.2001 (אלפיות שנייה)
	c14Frag   = 4                   // אורך קטע בשניות
	c14Total  = 45 * time.Minute    // מגבלת זמן להורדת תוכנית שלמה
	c14Full   = "התוכנית המלאה"     // הסימן בכותרת ביוטיוב שזו תוכנית שלמה
)

// c14Stream: כתובת הבסיס של השידור החי. משתנה — לבדיקות ולעקיפה (C14_STREAM).
var c14Stream = "https://r.il.cdn-redge.media/livehls/oil/ch14/live/ch14/live.livx"

// c14FeedBase: הזנת רשימת השמעה ביוטיוב. משתנה — לבדיקות.
var c14FeedBase = "https://www.youtube.com/feeds/videos.xml?playlist_id="

type c14Source struct {
	playlist string
	start    [2]int // שעה, דקה (שעון ישראל)
	length   time.Duration
	keep     int
}

func isC14(link string) bool { return strings.HasPrefix(link, c14Scheme) }

// parseC14 מפרק קישור c14://playlist/<id>?start=21:00&len=93&keep=7.
func parseC14(link string) (c14Source, error) {
	s := c14Source{start: [2]int{21, 0}, length: 93 * time.Minute}
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
		}
	}
	return s, nil
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

// fetchC14 קורא את ההזנה של רשימת ההשמעה ביוטיוב ומחזיר פרק לכל תוכנית מלאה
// שעדיין בחלון החזרה של ה-CDN, מהישנה לחדשה.
func fetchC14(link string, loc *time.Location, now time.Time) (string, []*podEpisode, error) {
	src, err := parseC14(link)
	if err != nil {
		return "", nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c14FeedBase+src.playlist, nil)
	if err != nil {
		return "", nil, err
	}
	req.Header.Set("User-Agent", "yemot-news-bridge (playlist feed reader)")
	resp, err := feedHTTP.Do(req)
	if err != nil {
		return "", nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", nil, fmt.Errorf("ההזנה של רשימת ההשמעה ביוטיוב: סטטוס %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return "", nil, err
	}
	var f ytFeed
	if err := xml.Unmarshal(body, &f); err != nil {
		return "", nil, fmt.Errorf("הזנה לא תקינה: %w", err)
	}
	seen := map[string]bool{}
	var eps []*podEpisode
	for _, e := range f.Entries {
		if !strings.Contains(e.Title, c14Full) {
			continue
		}
		m := reC14Date.FindStringSubmatch(e.Title)
		if m == nil {
			continue
		}
		d, _ := strconv.Atoi(m[1])
		mo, _ := strconv.Atoi(m[2])
		y, _ := strconv.Atoi(m[3])
		if mo < 1 || mo > 12 || d < 1 || d > 31 {
			continue
		}
		start := time.Date(y, time.Month(mo), d, src.start[0], src.start[1], 0, 0, loc)
		end := start.Add(src.length)
		if seen[m[0]] || now.Sub(start) > c14DVRMax || now.Sub(end) < 5*time.Minute {
			continue // כבר יש, יצא מחלון החזרה, או שהשידור עוד לא הסתיים
		}
		seen[m[0]] = true
		title := strings.TrimSpace(e.Title)
		if i := strings.IndexByte(title, '|'); i > 0 {
			title = strings.TrimSpace(title[:i]) // "הפטריוטים עם ינון מגל | 6.10 | התוכנית המלאה" → השם בלבד; התאריך מוקרא ממילא
		}
		h := fnv.New64a()
		fmt.Fprintf(h, "c14:%s:%s", src.playlist, start.Format("2006-01-02"))
		eps = append(eps, &podEpisode{
			id:    fmt.Sprintf("%016x", h.Sum64()),
			ts:    start.Unix(),
			title: title,
			secs:  int(src.length.Seconds()),
			src:   fmt.Sprintf("c14dvr:%d:%d", start.Unix(), end.Unix()),
		})
	}
	sort.SliceStable(eps, func(i, j int) bool { return eps[i].ts < eps[j].ts })
	return f.Title, eps, nil
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
