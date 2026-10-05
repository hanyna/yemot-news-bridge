package main

// קריאה ישירה מטלגרם: הגשר קורא בעצמו את הדפים הציבוריים t.me/s/<ערוץ>,
// בלי לעבור דרך השרת של ערוץ חי (Render). כך הקו לא תלוי בשרת שנרדם,
// וההודעות מגיעות מהר יותר.
//
//   - הערוצים: CHANNELS ב-bridge.yml (לפי הסדר — זה סדר הכתבים בשלוחה 2).
//   - קצב מנומס: הפסקה של לפחות 2 שניות בין בקשות לטלגרם, כל ערוץ נבדק
//     בערך פעם ב-(מספר הערוצים × 6) שניות, ואחרי חסימה — המתנה מדורגת
//     (30 שניות → 20 דקות). אותו מנגנון שבערוץ חי.
//   - הודעות שנשמרו בזיכרון נשארות לכל ההפעלה; אם פורסמו הרבה הודעות בין
//     בדיקה לבדיקה — נקרא גם הדף הקודם, כדי לא לפספס.
//   - גיבוי: אם טלגרם חוסמים את כל הערוצים יותר מ-10 דקות ויש TGPOPUP_KEY,
//     הגשר לוקח את ההודעות משרת ערוץ חי עד שטלגרם חוזרים.
//   - SOURCE: "server" ב-bridge.yml מחזיר את הדרך הישנה (רק דרך ערוץ חי).

import (
	"errors"
	"fmt"
	"html"
	"io"
	"log"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// משתנים — כדי שבדיקות יוכלו לקצר אותם ולהפנות לשרת מזויף.
var (
	tgBase       = "https://t.me"
	tgGap        = 2 * time.Second  // הפסקה מינימלית בין שתי בקשות לטלגרם
	tgPerChannel = 6 * time.Second  // כל ערוץ נבדק פעם ב-(ערוצים × זה)
	tgMinEvery   = 30 * time.Second // ולא יותר מפעם בזה
	tgFallback   = 10 * time.Minute // כל הערוצים חסומים יותר מזה → שרת ערוץ חי
	tgBackoff    = []time.Duration{30 * time.Second, 2 * time.Minute, 5 * time.Minute, 10 * time.Minute, 20 * time.Minute}
)

const (
	tgKeep     = 80 // כמה הודעות אחרונות נשמרות בזיכרון מכל ערוץ
	tgGapPages = 3  // כשפורסמו הרבה הודעות מאז הבדיקה הקודמת — עד כמה דפים קודמים לקרוא
	tgUA       = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0 Safari/537.36"
	tgMobileUA = "Mozilla/5.0 (iPhone; CPU iPhone OS 17_5 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.5 Mobile/15E148 Safari/604.1"
)

// ---------------------------------------------------------------------------
// ניתוח הדף של טלגרם (הועתק מערוץ חי — parse.go — ונבדק שם מול הדפים האמיתיים).
// ---------------------------------------------------------------------------

type tgMessage struct {
	ID         int
	Channel    string
	Text       string
	Time       string
	Photo      string
	Photos     []string
	Video      string
	VideoThumb string
	Duration   string
	Round      string
	Voice      string
	VoiceDur   string
	Sticker    string
	Poll       string
	PollOpts   [][2]string // טקסט, אחוז
	LinkTitle  string
	Doc        string
}

var (
	tgReDataPost  = regexp.MustCompile(`data-post="([^"/]+)/(\d+)"`)
	tgReTime      = regexp.MustCompile(`<time[^>]*datetime="([^"]+)"`)
	tgRePhoto     = regexp.MustCompile(`tgme_widget_message_photo_wrap[^>]*?url\('([^']+)'\)`)
	tgReVideo     = regexp.MustCompile(`<video[^>]*?src="([^"]+)"`)
	tgReVideoTh   = regexp.MustCompile(`tgme_widget_message_video_thumb[^>]*?url\('([^']+)'\)`)
	tgReDuration  = regexp.MustCompile(`video_duration[^>]*>([^<]+)<`)
	tgReRoundTag  = regexp.MustCompile(`<video[^>]*roundvideo[^>]*>`)
	tgReVoiceSrc  = regexp.MustCompile(`<audio[^>]*src="([^"]+)"`)
	tgReVoiceDur  = regexp.MustCompile(`voice_duration[^>]*>([^<]+)<`)
	tgReSticker   = regexp.MustCompile(`tgme_widget_message_sticker[^>]*url\('([^']+)'\)`)
	tgRePollQ     = regexp.MustCompile(`tgme_widget_message_poll_question[^>]*>([^<]+)<`)
	tgRePollPct   = regexp.MustCompile(`tgme_widget_message_poll_option_percent[^>]*>([^<]+)<`)
	tgRePollText  = regexp.MustCompile(`tgme_widget_message_poll_option_text[^>]*>([^<]*)<`)
	tgReLinkTitle = regexp.MustCompile(`link_preview_title[^>]*>([^<]+)<`)
	tgReDocTitle  = regexp.MustCompile(`document_title[^>]*>([^<]+)<`)
	tgReBr        = regexp.MustCompile(`(?i)<br\s*/?>`)
	tgReTag       = regexp.MustCompile(`(?s)<[^>]*>`)
	tgReSpaces    = regexp.MustCompile(`[ \t]+`)
	tgReNewlines  = regexp.MustCompile(`\n{3,}`)

	tgReOgTitle   = regexp.MustCompile(`property="og:title"\s+content="([^"]*)"`)
	tgReHeadTitle = regexp.MustCompile(`tgme_channel_info_header_title[^>]*>(?:<span[^>]*>)?([^<]+)<`)

	tgReOgVideo  = regexp.MustCompile(`property="og:video(?::secure_url)?"\s+content="([^"]+)"`)
	tgReTwStream = regexp.MustCompile(`name="twitter:player:stream"\s+content="([^"]+)"`)
	tgReMp4CDN   = regexp.MustCompile(`https://[A-Za-z0-9.\-]*telesco\.pe/[^"'\s<>]+\.mp4[^"'\s<>]*`)
	tgReMp4Any   = regexp.MustCompile(`https://[^"'\s<>]+\.mp4[^"'\s<>]*`)
)

// tgParse: כל ההודעות שבדף t.me/s/<ערוץ>, מהישנה לחדשה.
func tgParse(page string) []tgMessage {
	locs := tgReDataPost.FindAllStringSubmatchIndex(page, -1)
	out := make([]tgMessage, 0, len(locs))
	for i, loc := range locs {
		id, err := strconv.Atoi(page[loc[4]:loc[5]])
		if err != nil {
			continue
		}
		end := len(page)
		if i+1 < len(locs) {
			end = locs[i+1][0]
		}
		block := page[loc[0]:end]
		m := tgMessage{ID: id, Channel: page[loc[2]:loc[3]], Text: tgText(block)}
		if s := tgReTime.FindStringSubmatch(block); s != nil {
			m.Time = s[1]
		}
		if all := tgRePhoto.FindAllStringSubmatch(block, -1); len(all) > 0 {
			m.Photo = html.UnescapeString(all[0][1])
			if len(all) > 1 {
				for _, p := range all {
					m.Photos = append(m.Photos, html.UnescapeString(p[1]))
				}
			}
		}
		if s := tgReVideo.FindStringSubmatch(block); s != nil {
			if src := html.UnescapeString(s[1]); tgReRoundTag.MatchString(block) {
				m.Round = src
			} else {
				m.Video = src
			}
		}
		if s := tgReVideoTh.FindStringSubmatch(block); s != nil {
			m.VideoThumb = html.UnescapeString(s[1])
		}
		if s := tgReDuration.FindStringSubmatch(block); s != nil {
			m.Duration = strings.TrimSpace(s[1])
		}
		if s := tgReVoiceSrc.FindStringSubmatch(block); s != nil {
			m.Voice = html.UnescapeString(s[1])
			if d := tgReVoiceDur.FindStringSubmatch(block); d != nil {
				m.VoiceDur = strings.TrimSpace(d[1])
			}
		}
		if s := tgReSticker.FindStringSubmatch(block); s != nil {
			m.Sticker = html.UnescapeString(s[1])
		}
		if s := tgRePollQ.FindStringSubmatch(block); s != nil {
			m.Poll = strings.TrimSpace(html.UnescapeString(s[1]))
			pcts := tgRePollPct.FindAllStringSubmatch(block, -1)
			texts := tgRePollText.FindAllStringSubmatch(block, -1)
			for i := 0; i < len(pcts) && i < len(texts); i++ {
				m.PollOpts = append(m.PollOpts, [2]string{strings.TrimSpace(html.UnescapeString(texts[i][1])), strings.TrimSpace(pcts[i][1])})
			}
		}
		if s := tgReLinkTitle.FindStringSubmatch(block); s != nil {
			m.LinkTitle = strings.TrimSpace(html.UnescapeString(s[1]))
		}
		if s := tgReDocTitle.FindStringSubmatch(block); s != nil {
			m.Doc = strings.TrimSpace(html.UnescapeString(s[1]))
		}
		out = append(out, m)
	}
	return out
}

// tgText: גוף ההודעה. טלגרם מקננים תגיות בתוך הטקסט, אז סופרים עומק של div.
func tgText(block string) string {
	start := strings.Index(block, `class="tgme_widget_message_text`)
	if start < 0 {
		return ""
	}
	open := strings.Index(block[start:], ">")
	if open < 0 {
		return ""
	}
	cursor := start + open + 1
	body, depth := cursor, 1
	for cursor < len(block) && depth > 0 {
		nextOpen := strings.Index(block[cursor:], "<div")
		nextClose := strings.Index(block[cursor:], "</div")
		if nextClose < 0 {
			break
		}
		if nextOpen >= 0 && nextOpen < nextClose {
			depth++
			cursor += nextOpen + 4
			continue
		}
		depth--
		if depth == 0 {
			return tgCleanText(block[body : cursor+nextClose])
		}
		cursor += nextClose + 5
	}
	return ""
}

func tgCleanText(s string) string {
	const br = "\x00BR\x00"
	s = tgReBr.ReplaceAllString(s, br)
	s = tgReTag.ReplaceAllString(s, "")
	s = html.UnescapeString(s)
	s = strings.NewReplacer("\r", " ", "\n", " ").Replace(s)
	s = tgReSpaces.ReplaceAllString(s, " ")
	s = strings.ReplaceAll(s, br, "\n")
	s = tgReNewlines.ReplaceAllString(s, "\n\n")
	lines := strings.Split(s, "\n")
	for i := range lines {
		lines[i] = strings.TrimSpace(lines[i])
	}
	return strings.TrimSpace(strings.Join(lines, "\n"))
}

// tgTitle: שם הערוץ מהדף (og:title, ואם אין — הכותרת שבראש הדף).
func tgTitle(page string) string {
	if m := tgReOgTitle.FindStringSubmatch(page); m != nil {
		if t := strings.TrimSpace(html.UnescapeString(m[1])); t != "" {
			return hebrewTitle(t)
		}
	}
	if m := tgReHeadTitle.FindStringSubmatch(page); m != nil {
		return hebrewTitle(strings.TrimSpace(html.UnescapeString(m[1])))
	}
	return ""
}

var tgReLatinWords = regexp.MustCompile(`[A-Za-z][A-Za-z.'_-]*`)

// hebrewTitle: שם שכתוב גם בעברית וגם באנגלית ("אריאל דנינו Ariel Danino") —
// רק החלק העברי, כדי שההקראה לא תקרא את השם פעמיים. שם בלי עברית — כמו שהוא.
func hebrewTitle(t string) string {
	if !regexp.MustCompile(`\p{Hebrew}`).MatchString(t) {
		return t
	}
	if len(strings.Join(tgReLatinWords.FindAllString(t, -1), "")) < 3 {
		return t // אות או שתיים ("הערוץ a") — חלק מהשם
	}
	h := strings.Join(strings.Fields(tgReLatinWords.ReplaceAllString(t, " ")), " ")
	h = strings.Trim(h, " -|·,")
	if !regexp.MustCompile(`\p{Hebrew}`).MatchString(h) {
		return t
	}
	return h
}

// tgVideoSrc: כתובת ה-mp4 הישירה שבדף של פוסט.
func tgVideoSrc(page string) string {
	for _, re := range []*regexp.Regexp{tgReVideo, tgReOgVideo, tgReTwStream} {
		if m := re.FindStringSubmatch(page); m != nil {
			return html.UnescapeString(m[1])
		}
	}
	for _, re := range []*regexp.Regexp{tgReMp4CDN, tgReMp4Any} {
		if m := re.FindString(page); m != "" {
			return html.UnescapeString(m)
		}
	}
	return ""
}

// tgItem: הודעה מטלגרם → FeedItem, עם אותו HTML שהשרת של ערוץ חי מחזיר
// (content.go, vision.go ו-audio.go מזהים לפיו תמונה/סרטון/קולית/סקר).
func tgItem(m tgMessage) FeedItem {
	var ts int64
	if t, err := time.Parse(time.RFC3339, m.Time); err == nil {
		ts = t.Unix()
	}
	text := strings.TrimSpace(m.Text)
	if text == "" { // כמו בערוץ חי: הודעה בלי טקסט מקבלת את סוג המדיה
		switch {
		case len(m.Photos) > 1:
			text = "אלבום · " + strconv.Itoa(len(m.Photos)) + " תמונות"
		case m.Video != "" || m.VideoThumb != "" || m.Round != "":
			text = "סרטון"
		case m.Voice != "":
			text = "הודעה קולית"
		case m.Sticker != "":
			text = "סטיקר"
		case m.Poll != "":
			text = m.Poll
		case m.Doc != "":
			text = m.Doc
		case m.Photo != "":
			text = "תמונה"
		}
	}
	return FeedItem{ID: m.ID, Channel: m.Channel, TS: ts, Text: text, HTML: tgHTML(m)}
}

func tgHTML(m tgMessage) string {
	e := html.EscapeString
	var b strings.Builder
	switch {
	case len(m.Photos) > 1:
		b.WriteString(`<div class="photo"><div class="album">`)
		for _, p := range m.Photos {
			b.WriteString(`<img src="` + e(p) + `" alt="">`)
		}
		b.WriteString(`</div></div>`)
	case m.Round != "":
		b.WriteString(`<div class="photo"><div class="roundwrap"><video class="roundvid" src="` + e(m.Round) + `"></video></div></div>`)
	case m.Video != "" && m.Duration == "": // GIF — בלי קול
		b.WriteString(`<div class="photo"><div class="vidwrap"><video class="gifvid" src="` + e(m.Video) + `"></video></div></div>`)
	case m.Video != "":
		b.WriteString(`<div class="photo"><div class="vidwrap"><video src="` + e(m.Video) + `"></video>` +
			`<span class="durbadge durtop">` + e(m.Duration) + `</span></div></div>`)
	case m.VideoThumb != "": // סרטון ארוך שטלגרם מציגים בלי חשבון רק כתמונה
		dur := ""
		if m.Duration != "" {
			dur = `<span class="durbadge">` + e(m.Duration) + `</span>`
		}
		b.WriteString(`<div class="photo"><div class="vidwrap embedwrap"><img src="` + e(m.VideoThumb) + `" alt="">` + dur + `</div></div>`)
	case m.Sticker != "":
		b.WriteString(`<div class="stickerbox"><img src="` + e(m.Sticker) + `" alt=""></div>`)
	case m.Photo != "":
		b.WriteString(`<div class="photo"><img src="` + e(m.Photo) + `" alt=""></div>`)
	}
	if m.Voice != "" {
		dur := ""
		if m.VoiceDur != "" {
			dur = `<span class="voicedur">` + e(m.VoiceDur) + `</span>`
		}
		b.WriteString(`<div class="voicebox"><audio src="` + e(m.Voice) + `"></audio>` + dur + `</div>`)
	}
	if m.Poll != "" {
		b.WriteString(`<div class="pollbox"><div class="pollq">` + e(m.Poll) + `</div>`)
		for _, o := range m.PollOpts {
			b.WriteString(`<div class="pollopt"><div class="pollmeta"><span>` + e(o[0]) + `</span><b>` + e(o[1]) + `</b></div></div>`)
		}
		b.WriteString(`</div>`)
	}
	if m.Doc != "" {
		b.WriteString(`<div class="docbox"><div class="docname">` + e(m.Doc) + `</div></div>`)
	}
	if m.LinkTitle != "" {
		b.WriteString(`<div class="linkcard"><div class="linktitle">` + e(m.LinkTitle) + `</div></div>`)
	}
	if strings.TrimSpace(m.Text) != "" {
		b.WriteString(`<div class="msgtext">` + e(m.Text) + `</div>`)
	}
	return b.String()
}

// ---------------------------------------------------------------------------
// קריאת הערוצים, בקצב מנומס.
// ---------------------------------------------------------------------------

// tgStatusError: טלגרם החזירו סטטוס שהוא לא 200.
type tgStatusError struct{ code int }

func (e *tgStatusError) Error() string {
	switch {
	case e.code == http.StatusTooManyRequests:
		return "טלגרם מגבילים את קצב הבקשות (429)"
	case e.code == http.StatusForbidden:
		return "טלגרם חסמו זמנית את הבקשות (403)"
	case e.code == http.StatusNotFound:
		return "הערוץ לא נמצא (404) — אולי השם שגוי או שהערוץ נסגר"
	}
	return "טלגרם החזירו סטטוס " + strconv.Itoa(e.code)
}

// errTgTooBig: סרטון גדול שטלגרם מציגים בלי חשבון רק כתמונה ("Media is too big").
var errTgTooBig = errors.New("הסרטון גדול מדי — טלגרם לא נותנים אותו בלי חשבון")

var errTgEmpty = errors.New("הדף נטען בלי הודעות (כנראה הגבלת קצב זמנית של טלגרם)")

type tgChan struct {
	name    string
	title   string
	items   map[int]FeedItem
	videos  map[int]string // כתובות mp4 שנמצאו בדף (טריות — הן פגות אחרי זמן)
	ok      bool           // נקרא בהצלחה לפחות פעם אחת בהפעלה הזו
	lastOK  time.Time
	nextTry time.Time
	fails   int
	lastErr string
}

type tgSource struct {
	client *http.Client
	chans  []*tgChan

	mu   sync.Mutex // בקשות לטלגרם — גם מה-worker של הקול (סרטונים)
	last time.Time

	started  time.Time
	allOKAt  time.Time // מתי לאחרונה ערוץ כלשהו נקרא בהצלחה
	usingSrv bool
	lastLog  time.Time
	dataMu   sync.Mutex // chans[*].videos — נקרא גם מה-worker של הקול
}

// newTgSource: nil כש-SOURCE=server או שאין ערוצים.
func newTgSource(channels, mode string) *tgSource {
	if strings.EqualFold(strings.TrimSpace(mode), "server") {
		return nil
	}
	s := &tgSource{client: &http.Client{Timeout: 25 * time.Second}, started: time.Now()}
	seen := map[string]bool{}
	for _, f := range strings.FieldsFunc(channels, func(r rune) bool { return r == ',' || r == ' ' || r == '\n' || r == '\t' }) {
		name := strings.TrimPrefix(strings.TrimPrefix(strings.TrimPrefix(f, "https://t.me/s/"), "https://t.me/"), "@")
		if name == "" || seen[strings.ToLower(name)] {
			continue
		}
		seen[strings.ToLower(name)] = true
		s.chans = append(s.chans, &tgChan{name: name, items: map[int]FeedItem{}, videos: map[int]string{}})
	}
	if len(s.chans) == 0 {
		return nil
	}
	return s
}

// every: כל כמה זמן כל ערוץ נבדק.
func (s *tgSource) every() time.Duration {
	d := time.Duration(len(s.chans)) * tgPerChannel
	if d < tgMinEvery {
		d = tgMinEvery
	}
	return d
}

// get: בקשה אחת לטלגרם, אחרי ההפסקה המינימלית מהבקשה הקודמת.
func (s *tgSource) get(path, ua string) (string, error) {
	s.mu.Lock()
	if wait := tgGap - time.Since(s.last); wait > 0 {
		time.Sleep(wait)
	}
	s.last = time.Now()
	s.mu.Unlock()
	req, err := http.NewRequest(http.MethodGet, tgBase+path, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", ua)
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")
	req.Header.Set("Accept-Language", "he,en;q=0.8")
	resp, err := s.client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", &tgStatusError{resp.StatusCode}
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	return string(body), err
}

// fetch: קריאה של ערוץ אחד (הדף האחרון, ואם צריך — גם דפים קודמים).
func (s *tgSource) fetch(c *tgChan) error {
	page, err := s.get("/s/"+c.name, tgUA)
	if err != nil {
		return err
	}
	msgs := tgParse(page)
	if len(msgs) == 0 {
		return errTgEmpty
	}
	if t := tgTitle(page); t != "" {
		c.title = t
	}
	known := 0
	for id := range c.items {
		if id > known {
			known = id
		}
	}
	first := !c.ok
	s.add(c, msgs)
	// פורסמו יותר הודעות ממה שיש בדף מאז הבדיקה הקודמת (או הפעלה ראשונה) —
	// קוראים גם את הדפים הקודמים, כדי שלא יהיה חור.
	oldest := msgs[0].ID
	for p := 0; p < tgGapPages && oldest > 1 && (first || oldest > known+1); p++ {
		prev, err := s.get("/s/"+c.name+"?before="+strconv.Itoa(oldest), tgUA)
		if err != nil {
			break
		}
		older := tgParse(prev)
		if len(older) == 0 || older[0].ID >= oldest {
			break
		}
		s.add(c, older)
		oldest = older[0].ID
		if first {
			break // בהפעלה ראשונה — דף אחד נוסף מספיק (הארכיון בקו כבר שמור)
		}
	}
	return nil
}

func (s *tgSource) add(c *tgChan, msgs []tgMessage) {
	s.dataMu.Lock()
	defer s.dataMu.Unlock()
	for _, m := range msgs {
		if !strings.EqualFold(m.Channel, c.name) {
			continue // הודעה שהועברה מערוץ אחר נשארת תחת הערוץ שלנו בדף — אבל data-post הוא של המקור
		}
		m.Channel = c.name
		c.items[m.ID] = tgItem(m)
		if src := nonEmptyStr(m.Video, m.Round); src != "" {
			c.videos[m.ID] = src
		}
	}
	if len(c.items) > tgKeep {
		ids := make([]int, 0, len(c.items))
		for id := range c.items {
			ids = append(ids, id)
		}
		sort.Ints(ids)
		for _, id := range ids[:len(ids)-tgKeep] {
			delete(c.items, id)
			delete(c.videos, id)
		}
	}
}

func nonEmptyStr(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// poll: בודק את הערוצים שהגיע זמנם, ומחזיר את כל ההודעות שבזיכרון ואת
// רשימת הערוצים. fallback=true: טלגרם חוסמים את הכול כבר זמן רב — עדיף
// לקחת את ההודעות משרת ערוץ חי (אם יש מפתח).
func (s *tgSource) poll(now time.Time) (items []FeedItem, chans []Channel, fallback bool) {
	every := s.every()
	for _, c := range s.chans {
		if now.Before(c.nextTry) {
			continue
		}
		if err := s.fetch(c); err != nil {
			if c.fails < len(tgBackoff) {
				c.fails++
			}
			wait := tgBackoff[c.fails-1]
			c.nextTry = now.Add(wait)
			if c.lastErr != err.Error() || c.fails == 1 {
				log.Printf("טלגרם: ערוץ %s — %v. מנסה שוב בעוד %v.", c.name, err, wait)
			}
			c.lastErr = err.Error()
			continue
		}
		if c.fails > 0 {
			log.Printf("טלגרם: ערוץ %s חזר לעבוד.", c.name)
		}
		c.ok, c.fails, c.lastErr, c.lastOK = true, 0, "", now
		c.nextTry = now.Add(every)
		s.allOKAt = now
	}
	for _, c := range s.chans {
		chans = append(chans, Channel{Name: c.name, Title: c.title})
		for _, it := range c.items {
			items = append(items, it)
		}
	}
	since := s.allOKAt
	if since.IsZero() {
		since = s.started
	}
	return items, chans, now.Sub(since) > tgFallback
}

// anyOK: ערוץ כלשהו כבר נקרא בהצלחה בהפעלה הזו.
func (s *tgSource) anyOK() bool {
	for _, c := range s.chans {
		if c.ok {
			return true
		}
	}
	return false
}

// logStatus: פעם ב-10 דקות — מצב כל ערוץ בלוג.
func (s *tgSource) logStatus(now time.Time) {
	if now.Sub(s.lastLog) < 10*time.Minute {
		return
	}
	s.lastLog = now
	for _, c := range s.chans {
		state := "תקין"
		if c.fails > 0 {
			state = "נכשל: " + c.lastErr
		}
		last := "אף פעם"
		if !c.lastOK.IsZero() {
			last = "לפני " + now.Sub(c.lastOK).Round(time.Second).String()
		}
		log.Printf("טלגרם: ערוץ %s — %s, נקרא לאחרונה %s, %d הודעות בזיכרון.", c.name, state, last, len(c.items))
	}
}

// videoURL: הכתובת הישירה של סרטון בטלגרם. קודם מה שנמצא בדף (טרי), ואם
// אין או שכבר פג — מבקשים מחדש מכמה דפים של הפוסט (כמו בערוץ חי).
func (s *tgSource) videoURL(channel string, id int, fresh bool) (string, error) {
	if !fresh {
		s.dataMu.Lock()
		for _, c := range s.chans {
			if strings.EqualFold(c.name, channel) {
				if src := c.videos[id]; src != "" {
					s.dataMu.Unlock()
					return src, nil
				}
			}
		}
		s.dataMu.Unlock()
	}
	post := "/" + channel + "/" + strconv.Itoa(id)
	tries := []struct{ path, ua string }{
		{post + "?embed=1&mode=tme", tgUA},
		{post + "?embed=1&mode=tme&single=1", tgUA},
		{post, tgUA},
		{post, tgMobileUA},
	}
	for i, t := range tries {
		page, err := s.get(t.path, t.ua)
		if err == nil {
			if src := tgVideoSrc(page); src != "" {
				return src, nil
			}
			if i == 0 && strings.Contains(page, "Media is too big") {
				return "", errTgTooBig
			}
		}
		if i == 0 { // הדף של הערוץ, ממוקד בפוסט — לפעמים הסרטון רק שם
			if page, err := s.get("/s/"+channel+"?before="+strconv.Itoa(id+1), tgUA); err == nil {
				for _, m := range tgParse(page) {
					if m.ID == id && nonEmptyStr(m.Video, m.Round) != "" {
						return nonEmptyStr(m.Video, m.Round), nil
					}
				}
			}
		}
	}
	return "", fmt.Errorf("טלגרם לא נותנים את הסרטון בלי חשבון (עדיין)")
}
