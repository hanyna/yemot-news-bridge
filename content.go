package main

// סינון תוכן: פרסומות, מבזקים ותיאור מדיה (תמונה/סרטון/קולית/סקר).

import (
	"fmt"
	"html"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// adPhrases — ביטויים שמסמנים פרסומת/גיוס כסף/קידום ערוץ. הודעה שמכילה
// אחד מהם לא מוקראת. להוספה: שורה חדשה ברשימה.
var adPhrases = []string{
	"לרכישה", "לרכוש את", "להזמנות", "הזמינו עכשיו", "הזמינו כאן", "במחיר מיוחד", "במחיר השקה",
	"קוד קופון", "קוד הנחה", "אחוז הנחה", "לתרומות", "לתרומה", "תרמו עכשיו", "לחצו לתרומה",
	"הצטרפו לערוץ", "הצטרפו לקבוצה", "להצטרפות לערוץ", "להצטרפות לקבוצה", "הצטרפו עכשיו",
	"תוכן ממומן", "תוכן שיווקי", "בחסות", "פרסומת",
}

// isAd: האם ההודעה (אחרי ניקוי) היא פרסומת.
func isAd(text string) bool {
	for _, p := range adPhrases {
		if strings.Contains(text, p) {
			return true
		}
	}
	return false
}

// flashWords — מילים שהופכות הודעה ל"מבזק" לחצי שעה מרגע הפרסום:
// נשמעת ראשונה בשלוחה. ("מבזק"/"דחוף" לא ברשימה — ערוצים כותבים אותן כמעט תמיד.)
var flashWords = []string{
	"פיגוע", "אזעקה", "אזעקות", "צבע אדום", "חדירה", "חדירת", "ירי לעבר", "דקירה", "דריסה",
	"פצועים", "פצוע קשה", "נרצח", "נרצחה", "נרצחו", "מרחב מוגן", "המרחב המוגן", "יירוט",
	"שיגור", "שיגורים", "רקטה", "רקטות",
}

const flashFor = 30 * time.Minute

// flashEnabled — מבזקים כבויים (לבקשת המשתמש). true מחזיר אותם.
var flashEnabled = false

func isFlashText(text string) bool {
	if !flashEnabled {
		return false
	}
	for _, w := range flashWords {
		if strings.Contains(text, w) {
			return true
		}
	}
	return false
}

// flashActive: מבזק שעוד לא עברה חצי שעה מפרסומו.
func flashActive(it FeedItem, now time.Time) bool {
	age := now.Sub(time.Unix(it.TS, 0))
	return it.Flash && age >= -time.Minute && age < flashFor
}

var (
	reDurBadge = regexp.MustCompile(`class="durbadge[^"]*">([^<]*)<`) // גם "durbadge durtop" של סרטון רגיל
	rePollQ    = regexp.MustCompile(`class="pollq">(?:📊\s*)?([^<]*)<`)
	rePollOpt  = regexp.MustCompile(`class="pollopt">(.*?)</div>`)
	reTags     = regexp.MustCompile(`<[^>]+>`)
)

// mediaNote מתאר את המדיה שבהודעה לפי ה-HTML שהשרת מחזיר.
// onlyMedia=true כשאין בהודעה טקסט משלה — אז התיאור הוא כל ההודעה.
func mediaNote(h string) (note string, onlyMedia bool, skip bool) {
	if h == "" {
		return "", false, false
	}
	onlyMedia = !strings.Contains(h, `class="msgtext"`)
	dur := ""
	if m := reDurBadge.FindStringSubmatch(h); m != nil {
		dur = spokenDuration(html.UnescapeString(m[1]))
	}
	withDur := func(s string) string {
		if dur != "" {
			return s + " באורך " + dur
		}
		return s
	}
	switch {
	case strings.Contains(h, `class="pollbox"`):
		q := ""
		if m := rePollQ.FindStringSubmatch(h); m != nil {
			q = strings.TrimSpace(html.UnescapeString(m[1]))
		}
		var opts []string
		for _, m := range rePollOpt.FindAllStringSubmatch(h, 6) {
			t := strings.TrimSpace(html.UnescapeString(reTags.ReplaceAllString(m[1], " ")))
			if t != "" {
				opts = append(opts, t)
			}
		}
		note = "סקר: " + q
		if len(opts) > 0 {
			note += ". האפשרויות: " + strings.Join(opts, ", ")
		}
	case strings.Contains(h, `class="roundwrap"`):
		note = withDur("סרטון קצר")
	case strings.Contains(h, `class="vidwrap`):
		note = withDur("סרטון")
	case strings.Contains(h, `class="voicebox"`):
		note = "הודעה קולית"
	case strings.Contains(h, `class="stickerbox"`):
		return "", onlyMedia, onlyMedia // סטיקר בלבד — לא מקריאים
	case strings.Contains(h, `class="docbox"`):
		note = "קובץ מצורף"
	case strings.Contains(h, `class="photo"`):
		n := strings.Count(h, "<img ")
		if n > 1 {
			note = strconv.Itoa(n) + " תמונות"
		} else {
			note = "תמונה"
		}
	}
	return note, onlyMedia, false
}

// spokenDuration: "0:31" → "31 שניות", "2:05" → "2 דקות ו 5 שניות".
func spokenDuration(d string) string {
	parts := strings.Split(strings.TrimSpace(d), ":")
	var nums []int
	for _, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil {
			return ""
		}
		nums = append(nums, n)
	}
	secs := 0
	for _, n := range nums {
		secs = secs*60 + n
	}
	if secs <= 0 {
		return ""
	}
	h, m, s := secs/3600, secs/60%60, secs%60
	var out []string
	switch {
	case h == 1:
		out = append(out, "שעה")
	case h == 2:
		out = append(out, "שעתיים")
	case h > 2:
		out = append(out, fmt.Sprintf("%d שעות", h))
	}
	if m == 1 {
		out = append(out, "דקה")
	} else if m > 1 {
		out = append(out, fmt.Sprintf("%d דקות", m))
	}
	if s > 0 && h == 0 {
		out = append(out, fmt.Sprintf("%d שניות", s))
	}
	return strings.Join(out, " ו ")
}
