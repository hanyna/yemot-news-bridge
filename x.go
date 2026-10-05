package main

// חשבונות טוויטר בקו — בלי חשבון ובלי מפתח.
//
// טוויטר לא מראים ציוצים למי שלא מחובר, ואין דרך רשמית בחינם. הגשר קורא את
// הציוצים דרך שירות ציבורי פתוח (FxTwitter — api.fxtwitter.com), שמשמש בדרך
// כלל להצגת ציוצים יפה בצ'אטים. הוא מחזיר לכל חשבון את הציוצים האחרונים שלו.
//
// כל ציוץ נכנס לקו כמו הודעה מטלגרם: שלוחה 1 ושלוחת כתב משלו, עם תיאור תמונות
// והקול של סרטונים. ציוץ מחדש (ריטוויט) נכנס כ"שיתף ציוץ של ...", וציוץ שמצטט
// ציוץ אחר — עם הציטוט. תגובה לאחרים — לא נכנסת. שרשור של הכותב עצמו — כן.
//
// השירות לא שלנו ויכול ליפול: תקלה בו לא עוצרת כלום — הטלגרם ממשיך, והחשבון
// נבדק שוב אחרי הפסקה שהולכת וגדלה. הכתובת ניתנת לשינוי ב-X_API (שירות תואם).
//
// בקו, שם הערוץ של חשבון טוויטר הוא "x-<שם המשתמש>" (בטלגרם אין "-" בשמות,
// אז אין התנגשות).

import (
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"log"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	xPrefix   = "x-"
	xKeep     = 60              // כמה ציוצים אחרונים נשמרים בזיכרון לכל חשבון
	xGap      = 3 * time.Second // הפסקה מינימלית בין בקשות לשירות
	xMinEvery = 2 * time.Minute // כל חשבון נבדק לכל היותר פעם בזמן הזה
)

// xBase: השירות הציבורי. משתנה בבדיקות.
var xBase = "https://api.fxtwitter.com"

// xBackoff: אחרי תקלות רצופות — כמה לחכות עד הניסיון הבא.
var xBackoff = []time.Duration{time.Minute, 3 * time.Minute, 10 * time.Minute, 30 * time.Minute}

var errXNotFound = errors.New("החשבון לא נמצא בטוויטר (בדקו את השם ב-X_ACCOUNTS)")

type xAcct struct {
	handle  string // כמו שנכתב ב-X_ACCOUNTS
	title   string // השם שמוצג בטוויטר (העברי)
	items   map[int]FeedItem
	nextTry time.Time
	fails   int
	lastErr string
	lastOK  time.Time
	ok      bool
}

func (a *xAcct) channel() string { return xPrefix + strings.ToLower(a.handle) }

type xSource struct {
	client  *http.Client
	accts   []*xAcct
	every   time.Duration
	mu      sync.Mutex
	last    time.Time
	lastLog time.Time
}

// isXChannel: ערוץ בקו שהוא חשבון טוויטר.
func isXChannel(ch string) bool { return strings.HasPrefix(ch, xPrefix) }

// newXSource: nil כשאין חשבונות. מקבל "שם", "@שם" או קישור לפרופיל.
func newXSource(list, base string) *xSource {
	if b := strings.TrimRight(strings.TrimSpace(base), "/"); b != "" {
		xBase = b
	}
	s := &xSource{client: &http.Client{Timeout: 30 * time.Second}}
	seen := map[string]bool{}
	for _, f := range strings.FieldsFunc(list, func(r rune) bool { return r == ',' || r == ' ' || r == '\n' || r == '\t' }) {
		h := xHandle(f)
		if h == "" || seen[strings.ToLower(h)] {
			continue
		}
		seen[strings.ToLower(h)] = true
		s.accts = append(s.accts, &xAcct{handle: h, items: map[int]FeedItem{}})
	}
	if len(s.accts) == 0 {
		return nil
	}
	s.every = time.Duration(len(s.accts)) * 30 * time.Second
	if s.every < xMinEvery {
		s.every = xMinEvery
	}
	return s
}

// xHandle: "https://x.com/ariel__danino?s=21" / "@ariel__danino" → "ariel__danino".
func xHandle(f string) string {
	f = strings.TrimSpace(f)
	if u, err := url.Parse(f); err == nil && u.Host != "" {
		f = strings.Trim(u.Path, "/")
		if i := strings.Index(f, "/"); i >= 0 {
			f = f[:i]
		}
	} else {
		for _, p := range []string{"x.com/", "twitter.com/", "www.x.com/", "www.twitter.com/"} {
			f = strings.TrimPrefix(f, p)
		}
		if i := strings.IndexAny(f, "/?"); i >= 0 {
			f = f[:i]
		}
	}
	f = strings.TrimPrefix(f, "@")
	for _, r := range f {
		if !(r == '_' || r >= '0' && r <= '9' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z') {
			return ""
		}
	}
	if len(f) > 15 {
		return ""
	}
	return f
}

// ---------------------------------------------------------------------------
// התשובה של השירות.
// ---------------------------------------------------------------------------

type xStatus struct {
	ID               string          `json:"id"`
	Text             string          `json:"text"`
	CreatedAt        string          `json:"created_at"`
	CreatedTimestamp int64           `json:"created_timestamp"`
	Author           xAuthor         `json:"author"`
	RepostedBy       json.RawMessage `json:"reposted_by"`
	ReplyingTo       json.RawMessage `json:"replying_to"`
	ReplyingToStatus json.RawMessage `json:"replying_to_status"`
	Media            *xMedia         `json:"media"`
	Quote            *xStatus        `json:"quote"`
}

type xAuthor struct {
	ScreenName string `json:"screen_name"`
	Name       string `json:"name"`
}

type xMedia struct {
	Photos []struct {
		URL string `json:"url"`
	} `json:"photos"`
	Videos []struct {
		URL          string  `json:"url"`
		ThumbnailURL string  `json:"thumbnail_url"`
		Duration     float64 `json:"duration"`
		Type         string  `json:"type"`
	} `json:"videos"`
}

func isNullJSON(r json.RawMessage) bool {
	s := strings.TrimSpace(string(r))
	return s == "" || s == "null" || s == `""` || s == "false"
}

// replyToOther: תגובה לחשבון אחר (לא שרשור של הכותב עצמו).
func (t *xStatus) replyToOther(handle string) bool {
	if isNullJSON(t.ReplyingTo) {
		return false
	}
	var name string
	if json.Unmarshal(t.ReplyingTo, &name) != nil {
		var o struct {
			ScreenName string `json:"screen_name"`
		}
		if json.Unmarshal(t.ReplyingTo, &o) != nil {
			return true
		}
		name = o.ScreenName
	}
	return !strings.EqualFold(strings.TrimPrefix(name, "@"), handle)
}

func (t *xStatus) when() time.Time {
	if t.CreatedTimestamp > 0 {
		return time.Unix(t.CreatedTimestamp, 0)
	}
	if tm, err := time.Parse(time.RubyDate, t.CreatedAt); err == nil {
		return tm
	}
	return time.Time{}
}

// minSec: 89.4 → "1:29".
func minSec(secs float64) string {
	n := int(secs + 0.5)
	if n <= 0 {
		return ""
	}
	return strconv.Itoa(n/60) + ":" + fmt.Sprintf("%02d", n%60)
}

// xItem: ציוץ → FeedItem, עם אותו HTML כמו הודעה מטלגרם (תמונות, סרטון).
func xItem(t *xStatus, channel string) (FeedItem, bool) {
	id, err := strconv.ParseInt(t.ID, 10, 64)
	if err != nil || id <= 0 {
		return FeedItem{}, false
	}
	m := tgMessage{ID: int(id), Channel: channel, Text: strings.TrimSpace(t.Text)}
	if q := t.Quote; q != nil && strings.TrimSpace(q.Text) != "" {
		who := hebrewTitle(strings.TrimSpace(q.Author.Name))
		if who == "" {
			who = q.Author.ScreenName
		}
		quote := "ציטוט של " + who + ": " + strings.TrimSpace(q.Text)
		if m.Text == "" {
			m.Text = quote
		} else {
			m.Text += "\n\n" + quote
		}
	}
	if tm := t.when(); !tm.IsZero() {
		m.Time = tm.UTC().Format(time.RFC3339)
	}
	if md := t.Media; md != nil {
		for _, p := range md.Photos {
			if p.URL != "" {
				m.Photos = append(m.Photos, p.URL)
			}
		}
		if len(m.Photos) == 1 {
			m.Photo, m.Photos = m.Photos[0], nil
		}
		if len(md.Videos) > 0 && m.Photo == "" && len(m.Photos) == 0 {
			v := md.Videos[0]
			if v.Type == "gif" {
				m.Video = v.URL // בלי משך = GIF, בלי קול
			} else if v.URL != "" {
				m.Video, m.Duration = v.URL, minSec(v.Duration)
				if m.Duration == "" {
					m.Duration = "0:01"
				}
			} else {
				m.VideoThumb = v.ThumbnailURL
			}
		}
	}
	it := tgItem(m)
	if it.TS == 0 {
		return FeedItem{}, false
	}
	return it, true
}

// ---------------------------------------------------------------------------
// קריאה.
// ---------------------------------------------------------------------------

func (s *xSource) get(path string) ([]byte, int, error) {
	s.mu.Lock()
	if wait := xGap - time.Since(s.last); wait > 0 {
		time.Sleep(wait)
	}
	s.last = time.Now()
	s.mu.Unlock()
	req, err := http.NewRequest(http.MethodGet, xBase+path, nil)
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("User-Agent", "yemot-news-bridge (phone news line; https://github.com/hanyna/yemot-news-bridge)")
	req.Header.Set("Accept", "application/json")
	resp, err := s.client.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	return body, resp.StatusCode, err
}

// fetch: הציוצים האחרונים של חשבון אחד.
func (s *xSource) fetch(a *xAcct) error {
	body, code, err := s.get("/2/profile/" + url.PathEscape(a.handle) + "/statuses")
	if err != nil {
		return err
	}
	var r struct {
		Code    int       `json:"code"`
		Message string    `json:"message"`
		Results []xStatus `json:"results"`
	}
	jerr := json.Unmarshal(body, &r)
	switch {
	case code == http.StatusNotFound && (jerr != nil || len(r.Results) == 0):
		// השירות מחזיר 404 גם לחשבון בלי ציוצים וגם לחשבון שלא קיים — בודקים איזה
		if pb, pc, perr := s.get("/" + url.PathEscape(a.handle)); perr == nil && pc == http.StatusOK {
			var p struct {
				User xAuthor `json:"user"`
			}
			if json.Unmarshal(pb, &p) == nil && p.User.Name != "" {
				a.title = hebrewTitle(p.User.Name)
			}
			return nil // קיים, פשוט אין ציוצים כרגע
		}
		return errXNotFound
	case code != http.StatusOK:
		return fmt.Errorf("השירות החזיר %d", code)
	case jerr != nil:
		return fmt.Errorf("תשובה לא תקינה: %v", jerr)
	}
	for i := range r.Results {
		t := &r.Results[i]
		if a.title == "" && strings.EqualFold(t.Author.ScreenName, a.handle) && t.Author.Name != "" {
			a.title = hebrewTitle(t.Author.Name)
		}
		shared := !isNullJSON(t.RepostedBy) || !strings.EqualFold(t.Author.ScreenName, a.handle)
		if !shared && t.replyToOther(a.handle) {
			continue // תגובה לאחרים — לא נכנסת
		}
		if shared { // ריטוויט: "שיתף ציוץ של ..." ואחריו הציוץ
			who := hebrewTitle(strings.TrimSpace(t.Author.Name))
			if who == "" {
				who = t.Author.ScreenName
			}
			c := *t
			c.Text = "שיתף ציוץ של " + who + ": " + strings.TrimSpace(t.Text)
			t = &c
		}
		if it, ok := xItem(t, a.channel()); ok {
			a.items[it.ID] = it
		}
	}
	if len(a.items) > xKeep {
		ids := make([]int, 0, len(a.items))
		for id := range a.items {
			ids = append(ids, id)
		}
		sort.Ints(ids)
		for _, id := range ids[:len(ids)-xKeep] {
			delete(a.items, id)
		}
	}
	return nil
}

// poll: בודק את החשבונות שהגיע זמנם, ומחזיר את כל הציוצים שבזיכרון ואת
// רשימת החשבונות (אחרי ערוצי הטלגרם בשלוחה 2).
func (s *xSource) poll(now time.Time) (items []FeedItem, chans []Channel) {
	for _, a := range s.accts {
		if now.Before(a.nextTry) {
			continue
		}
		if err := s.fetch(a); err != nil {
			if a.fails < len(xBackoff) {
				a.fails++
			}
			// גם "לא נמצא" — המתנה רגילה שהולכת וגדלה (דקה, 3, 10, 30): השירות עונה
			// כך לפעמים בטעות על חשבון קיים. שם שגוי באמת — מגיע ל-30 דקות לבד.
			wait := xBackoff[a.fails-1]
			a.nextTry = now.Add(wait)
			if a.lastErr != err.Error() || a.fails == 1 {
				log.Printf("טוויטר: %s — %v. מנסה שוב בעוד %v.", a.handle, err, wait)
			}
			a.lastErr = err.Error()
			continue
		}
		if a.fails > 0 {
			log.Printf("טוויטר: %s חזר לעבוד.", a.handle)
		}
		if !a.ok {
			log.Printf("טוויטר: %s (%s) — נקרא, %d ציוצים.", a.handle, a.title, len(a.items))
		}
		a.ok, a.fails, a.lastErr, a.lastOK = true, 0, "", now
		a.nextTry = now.Add(s.every)
	}
	for _, a := range s.accts {
		chans = append(chans, Channel{Name: a.channel(), Title: a.title})
		for _, it := range a.items {
			items = append(items, it)
		}
	}
	return items, chans
}

// logStatus: פעם ב-10 דקות — מצב כל חשבון בלוג.
func (s *xSource) logStatus(now time.Time) {
	if now.Sub(s.lastLog) < 10*time.Minute {
		return
	}
	s.lastLog = now
	for _, a := range s.accts {
		state := "תקין"
		if a.fails > 0 {
			state = "נכשל: " + a.lastErr
		}
		last := "אף פעם"
		if !a.lastOK.IsZero() {
			last = "לפני " + now.Sub(a.lastOK).Round(time.Second).String()
		}
		log.Printf("טוויטר: %s — %s, נקרא לאחרונה %s, %d ציוצים בזיכרון.", a.handle, state, last, len(a.items))
	}
}

// videoURL: כתובת הסרטון של ציוץ שבזיכרון ("" = לא נמצא).
func (s *xSource) videoURL(channel string, id int) string {
	for _, a := range s.accts {
		if a.channel() != channel {
			continue
		}
		if it, ok := a.items[id]; ok {
			if m := tgReVideo.FindStringSubmatch(it.HTML); m != nil {
				return html.UnescapeString(m[1])
			}
		}
	}
	return ""
}
