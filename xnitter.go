package main

// מקור שני לטוויטר: Nitter — אתרים פתוחים שמציגים את טוויטר בלי חשבון, ונותנים
// לכל חשבון הזנת RSS עם הציוצים האחרונים (כולל ריטוויטים, בלי תגובות לאחרים).
//
// למה שני מקורות: FxTwitter מחזיר לפעמים "לא נמצא" על חשבון קיים (בבדיקה ב-6.10
// — 3 פעמים מתוך 6), ואז ציוצים מתעכבים. עכשיו כל בדיקה קוראת משניהם, וכל
// ציוץ שנמצא באחד מהם נכנס. ציוץ שנמצא רק ב-Nitter מושלם מ-FxTwitter (בקשה
// לציוץ הבודד, שעובדת גם כשרשימת החשבון לא) — כדי לקבל את הסרטון והשם המלא.
// אם גם זה לא עובד — נכנס כמו שהוא מ-Nitter (טקסט ותמונות, סרטון בלי קול).
//
// אתרי Nitter קמים ונופלים: מנסים לפי הסדר ונשארים עם מה שעבד. אתר שנכשל
// לא נבדק שוב חצי שעה. הרשימה ניתנת לשינוי ב-X_NITTER (מופרדים בפסיק; off = בלי).

import (
	"encoding/json"
	"encoding/xml"
	"html"
	"log"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// xNitterDefault: נבדקו מ-GitHub ב-6.10.2026 — הראשון עבד בכל הבדיקות, השני לפעמים.
var xNitterDefault = []string{
	"nitter.meowing.monster", "twiiit.com", "nitter.jaydenha.uk", "nitter.tiekoetter.com",
	"nitter.privacyredirect.com", "nitter.poast.org", "xcancel.com", "nitter.net",
}

// xNitterHosts: האתרים שבשימוש (X_NITTER). משתנה בבדיקות.
var xNitterHosts = xNitterDefault

const (
	nitterBadFor   = 30 * time.Minute
	nitterEnrich   = 6              // כמה ציוצים חדשים מושלמים מ-FxTwitter בכל בדיקה
	nitterEnrichIn = 48 * time.Hour // רק ציוצים מהיומיים האחרונים
	nitterUA       = "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/129.0 Safari/537.36"
)

// setNitterHosts: מ-X_NITTER. ריק — ברירת המחדל; "off" — בלי Nitter.
func setNitterHosts(env string) {
	env = strings.TrimSpace(env)
	switch {
	case env == "":
		xNitterHosts = xNitterDefault
	case strings.EqualFold(env, "off"):
		xNitterHosts = nil
	default:
		var hosts []string
		for _, h := range strings.FieldsFunc(env, func(r rune) bool { return r == ',' || r == ' ' || r == '\n' }) {
			if u, err := url.Parse(h); err == nil && u.Host != "" {
				h = u.Host
			}
			if h = strings.Trim(h, "/ "); h != "" {
				hosts = append(hosts, h)
			}
		}
		xNitterHosts = hosts
	}
}

type nitterRSS struct {
	Channel struct {
		Title string `xml:"title"`
		Items []struct {
			Title       string `xml:"title"`
			Creator     string `xml:"http://purl.org/dc/elements/1.1/ creator"`
			Description string `xml:"description"`
			PubDate     string `xml:"pubDate"`
			GUID        string `xml:"guid"`
		} `xml:"item"`
	} `xml:"channel"`
}

// nitterURL: כתובת ההזנה. בבדיקות xNitterHosts מכיל כתובת מלאה (http://127.0.0.1:...).
func nitterURL(host, handle string) string {
	if strings.Contains(host, "://") {
		return strings.TrimRight(host, "/") + "/" + url.PathEscape(handle) + "/rss"
	}
	return "https://" + host + "/" + url.PathEscape(handle) + "/rss"
}

// nitterRead: ההזנה של חשבון — מהאתר הראשון שעובד.
func (s *xSource) nitterRead(handle string) (*nitterRSS, bool) {
	hosts := xNitterHosts
	now := time.Now()
	for i := range hosts {
		k := (s.nitterCur + i) % len(hosts)
		h := hosts[k]
		if now.Before(s.nitterBad[h]) {
			continue
		}
		body, code, err := s.getURL(nitterURL(h, handle), nitterUA, "application/rss+xml, application/xml;q=0.9, */*;q=0.5", false)
		var r nitterRSS
		if err == nil && code == http.StatusOK && strings.Contains(string(body[:min(len(body), 500)]), "<rss") && xml.Unmarshal(body, &r) == nil {
			if k != s.nitterCur || !s.nitterOK {
				log.Printf("טוויטר: מקור שני (Nitter) — עובד דרך %s.", h)
			}
			s.nitterCur, s.nitterOK = k, true
			return &r, true
		}
		s.nitterBad[h] = now.Add(nitterBadFor) // חסום / למטה / דף "אתה רובוט?"
	}
	if s.nitterOK {
		log.Printf("טוויטר: אף אתר Nitter לא עונה כרגע — ממשיך עם FxTwitter בלבד.")
		s.nitterOK = false
	}
	return nil, false
}

// fetchNitter: מוסיף לחשבון את הציוצים שנמצאו ב-Nitter. false — אף אתר לא עבד.
func (s *xSource) fetchNitter(a *xAcct) bool {
	if len(xNitterHosts) == 0 {
		return false
	}
	r, ok := s.nitterRead(a.handle)
	if !ok {
		return false
	}
	if a.title == "" {
		if t, _, found := strings.Cut(r.Channel.Title, " / @"); found && strings.TrimSpace(t) != "" {
			a.title = hebrewTitle(strings.TrimSpace(t))
		}
	}
	enrich := nitterEnrich
	for _, n := range r.Channel.Items {
		id, err := strconv.ParseInt(strings.TrimSpace(n.GUID), 10, 64)
		if err != nil || id <= 0 {
			continue
		}
		if _, have := a.items[int(id)]; have || a.skip[int(id)] {
			continue
		}
		ts, err := http.ParseTime(strings.TrimSpace(n.PubDate))
		if err != nil {
			continue
		}
		if time.Since(ts) < nitterEnrichIn {
			if enrich == 0 {
				continue // בבדיקה הבאה (עוד 2 דקות)
			}
			enrich--
			if t, ok := s.fxStatus(n.GUID); ok {
				s.addStatus(a, t)
				if _, added := a.items[int(id)]; !added {
					if len(a.skip) > 500 {
						a.skip = map[int]bool{}
					}
					a.skip[int(id)] = true // תגובה לאחרים — לא לבדוק שוב
				}
				continue
			}
		}
		if it, ok := nitterItem(n.Creator, n.Description, ts, id, a); ok {
			a.items[it.ID] = it
		}
	}
	a.trimItems()
	return true
}

// fxStatus: ציוץ בודד מ-FxTwitter.
func (s *xSource) fxStatus(id string) (*xStatus, bool) {
	body, code, err := s.get("/2/status/" + url.PathEscape(id))
	if err != nil || code != http.StatusOK {
		return nil, false
	}
	var r struct {
		Status *xStatus `json:"status"`
	}
	if json.Unmarshal(body, &r) != nil || r.Status == nil || r.Status.ID == "" {
		return nil, false
	}
	return r.Status, true
}

var (
	nitterReImg    = regexp.MustCompile(`<img src="(https://pbs\.twimg\.com/media/[^"]+)"`)
	nitterReVidImg = regexp.MustCompile(`<img src="(https://pbs\.twimg\.com/(?:amplify_video_thumb|ext_tw_video_thumb|tweet_video_thumb)/[^"]+)"`)
	nitterReFooter = regexp.MustCompile(`(?s)<footer>.*?</footer>`)
	nitterReQuoter = regexp.MustCompile(`(?s)<b>(.*?) \(@[^)]*\)</b>`)
	nitterReVideo  = regexp.MustCompile(`(?i)<br>\s*(Video|GIF)\s*<br>`)
)

// nitterItem: פריט מההזנה → FeedItem, כשאין את הציוץ מ-FxTwitter.
func nitterItem(creator, desc string, ts time.Time, id int64, a *xAcct) (FeedItem, bool) {
	body, quote := desc, ""
	if i := strings.Index(desc, "<hr/>"); i >= 0 {
		body, quote = desc[:i], desc[i:]
	}
	m := tgMessage{ID: int(id), Channel: a.channel(), Time: ts.UTC().Format(time.RFC3339)}
	for _, im := range nitterReImg.FindAllStringSubmatch(body, -1) {
		m.Photos = append(m.Photos, html.UnescapeString(im[1]))
	}
	if v := nitterReVidImg.FindStringSubmatch(body); v != nil && len(m.Photos) == 0 {
		m.VideoThumb = html.UnescapeString(v[1])
	}
	if len(m.Photos) == 1 {
		m.Photo, m.Photos = m.Photos[0], nil
	}
	m.Text = tgCleanText(nitterReVideo.ReplaceAllString(body, " "))
	if quote != "" {
		q := nitterReFooter.ReplaceAllString(quote, "")
		who := ""
		if w := nitterReQuoter.FindStringSubmatch(q); w != nil {
			who = hebrewTitle(tgCleanText(w[1]))
			q = q[strings.Index(q, w[0])+len(w[0]):]
		}
		if qt := tgCleanText(nitterReVideo.ReplaceAllString(q, " ")); qt != "" {
			if who == "" {
				who = "ציוץ אחר"
			}
			line := "ציטוט של " + who + ": " + qt
			if m.Text == "" {
				m.Text = line
			} else {
				m.Text += "\n\n" + line
			}
		}
	}
	by := strings.TrimPrefix(strings.TrimSpace(creator), "@")
	if by != "" && !strings.EqualFold(by, a.handle) {
		m.Text = "שיתף ציוץ של " + by + ": " + m.Text
	}
	if strings.TrimSpace(m.Text) == "" && m.Photo == "" && len(m.Photos) == 0 && m.VideoThumb == "" {
		return FeedItem{}, false
	}
	it := tgItem(m)
	if it.TS == 0 {
		return FeedItem{}, false
	}
	return it, true
}
