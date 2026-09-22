package main

// הכנת טקסט להקראה: ניקוי, מילון הגייה, ניסוח שעה/יום, וסינון כפילויות.

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode"
)

// pronunciation — מילון הגייה: קיצורים שמנוע ההקראה נכשל בהם, ומה להקריא
// במקומם. עובד גם עם אותיות שימוש לפניהם (ביו"ש, לצה"ל, וח"כ...).
// ערך ריק = להשמיט (למשל בס"ד בראש הודעה).
// כדי להוסיף מילה: שורה חדשה "מה כתוב": "מה להקריא".
var pronunciation = map[string]string{
	`יו"ש`:   "יהודה ושומרון",
	`צה"ל`:   "צהל",
	`רה"מ`:   "ראש הממשלה",
	`שב"כ`:   "שבכ",
	`מג"ב`:   "מגב",
	`מד"א`:   "מדא",
	`בג"ץ`:   "בגץ",
	`ח"כ`:    "חבר הכנסת",
	`חה"כ`:   "חבר הכנסת",
	`ע"י`:    "על ידי",
	`ק"מ`:    "קילומטר",
	`ד"ר`:    "דוקטור",
	`עו"ד`:   "עורך דין",
	`ת"א`:    "תל אביב",
	`י-ם`:    "ירושלים",
	`חו"ל`:   "חוץ לארץ",
	`אח"כ`:   "אחר כך",
	`בע"ה`:   "בעזרת השם",
	`בעזה"י`: "בעזרת השם",
	`ב"ה`:    "ברוך השם",
	`בס"ד`:   "",
	`בסד"ה`:  "",
	`ז"ל`:    "זכרונו לברכה",
	`זצ"ל`:   "זכר צדיק לברכה",
	`הי"ד`:   "השם ייקום דמו",
	`שליט"א`: "שליטא",
	`וכו'`:   "וכולי",
	`ש"ח`:    "שקלים",
	`רמטכ"ל`: "רמטכל",
	`מנכ"ל`:  "מנכל",
	`יו"ר`:   "יושב ראש",
	`סמ"ר`:   "סמר",
	`רס"ן`:   "רסן",
	`סא"ל`:   "סגן אלוף",
	`תא"ל`:   "תת אלוף",
	`אל"מ`:   "אלוף משנה",
	`רב"ט`:   "רבט",
	`אה"ק`:   "ארץ הקודש",
	`א"י`:    "ארץ ישראל",
	`פצ"ר`:   "פצר",
	`יס"מ`:   "יסם",
	`מ"פ`:    "מפקד פלוגה",
	`מח"ט`:   "מחט",
	`מג"ד`:   "מגד",
}

// אותיות שימוש שיכולות להופיע לפני קיצור (ו, ב, ל, ה, מ, ש, כ), עד שתיים.
const prefixLetters = "ובלהמשכ"

var pronRules []pronRule

type pronRule struct {
	re   *regexp.Regexp
	with string
}

func init() {
	// קיצורים ארוכים קודם, כדי ש"חה"כ" לא ייתפס כ"ח"כ".
	keys := make([]string, 0, len(pronunciation))
	for k := range pronunciation {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return len([]rune(keys[i])) > len([]rune(keys[j])) })
	for _, k := range keys {
		pat := `(^|[^\p{L}])([` + prefixLetters + `]{0,2})` + regexp.QuoteMeta(k) + `($|[^\p{L}])`
		pronRules = append(pronRules, pronRule{regexp.MustCompile(pat), pronunciation[k]})
	}
}

// applyPronunciation מחליף קיצורים לפי המילון.
func applyPronunciation(s string) string {
	for _, r := range pronRules {
		// פעמיים — כדי לתפוס גם שני קיצורים צמודים שחולקים רווח ביניהם.
		for i := 0; i < 2; i++ {
			s = r.re.ReplaceAllStringFunc(s, func(m string) string {
				sub := r.re.FindStringSubmatch(m)
				prefix := sub[2]
				if r.with == "" {
					return sub[1] + sub[3]
				}
				// "ה" + "צהל" → "הצהל"; אבל "ל" + "יהודה ושומרון" → "ליהודה ושומרון".
				return sub[1] + prefix + r.with + sub[3]
			})
		}
	}
	return s
}

var (
	reURL     = regexp.MustCompile(`https?://\S+|t\.me/\S+|www\.\S+|@\w+`)
	reSpaces  = regexp.MustCompile(`\s+`)
	reDots    = regexp.MustCompile(`([.!?,])[\s.!?,]*[.,]`)
	reNumPct  = regexp.MustCompile(`(\d)\s*%`)
	reNumNIS  = regexp.MustCompile(`(\d)\s*₪|₪\s*(\d[\d,.]*)`)
	quoteLike = strings.NewReplacer("״", `"`, "”", `"`, "“", `"`, "„", `"`, "׳", "'", "’", "'", "‘", "'")
)

// cleanForSpeech מכין טקסט להקראה: מאחד סוגי גרשיים, מסיר קישורים ותיוגים,
// אימוג'ים וסמלים שהמנוע מקריא כמילים (כוכבית, סולמית...), מחליף קיצורים
// לפי מילון ההגייה, והופך ירידות שורה לנקודה — קובץ TTS צריך להיות רצף
// טקסט אחד פשוט.
func cleanForSpeech(s string) string {
	s = quoteLike.Replace(s)
	s = reURL.ReplaceAllString(s, " ")
	s = reNumPct.ReplaceAllString(s, "$1 אחוז")
	s = reNumNIS.ReplaceAllStringFunc(s, func(m string) string {
		sub := reNumNIS.FindStringSubmatch(m)
		if sub[1] != "" {
			return sub[1] + " שקלים"
		}
		return sub[2] + " שקלים"
	})
	var b strings.Builder
	for _, r := range s {
		switch {
		case r == '\n' || r == '\r':
			b.WriteString(". ")
		case unicode.IsLetter(r), unicode.IsDigit(r):
			b.WriteRune(r)
		case unicode.IsSpace(r):
			b.WriteRune(' ')
		case strings.ContainsRune(".,!?:;()'\"-–—/", r):
			if r == '–' || r == '—' {
				r = '-'
			}
			b.WriteRune(r)
		default:
			// אימוג'ים, * # _ | ~ ^ ועוד — מוחלפים ברווח.
			b.WriteRune(' ')
		}
	}
	s = reSpaces.ReplaceAllString(b.String(), " ")
	s = applyPronunciation(s)
	s = reSpaces.ReplaceAllString(s, " ")
	s = strings.ReplaceAll(s, " .", ".")
	s = strings.ReplaceAll(s, " ,", ",")
	s = reDots.ReplaceAllString(s, "$1")
	s = strings.Trim(s, " .,-:")
	return s
}

var weekdays = [...]string{"ראשון", "שני", "שלישי", "רביעי", "חמישי", "שישי", "שבת"}
var months = [...]string{"", "בינואר", "בפברואר", "במרץ", "באפריל", "במאי", "ביוני", "ביולי", "באוגוסט", "בספטמבר", "באוקטובר", "בנובמבר", "בדצמבר"}

// spokenWhen מנסח מתי נשלחה ההודעה, יחסית לעכשיו (בשעון ישראל):
// "בשעה 20 ו 5 דקות" / "אתמול בשעה ..." / "ביום שני בשעה ..." / "ב 3 בספטמבר בשעה ...".
func spokenWhen(t, now time.Time) string {
	clock := fmt.Sprintf("בשעה %d ו %d דקות", t.Hour(), t.Minute())
	if t.Minute() == 0 {
		clock = fmt.Sprintf("בשעה %d בדיוק", t.Hour())
	}
	day := func(x time.Time) time.Time { return time.Date(x.Year(), x.Month(), x.Day(), 0, 0, 0, 0, x.Location()) }
	days := int(day(now).Sub(day(t)).Hours()/24 + 0.5)
	switch {
	case days <= 0:
		return clock
	case days == 1:
		return "אתמול " + clock
	case days < 7:
		return "ביום " + weekdays[t.Weekday()] + " " + clock
	default:
		return fmt.Sprintf("ב %d %s %s", t.Day(), months[t.Month()], clock)
	}
}

// dedupe מסיר הודעות כפולות (אותה הודעה שהועברה בכמה ערוצים). נשארת
// ההודעה הראשונה שפורסמה — המקור. הקלט: הודעות עם טקסט נקי.
func dedupe(items []FeedItem) []FeedItem {
	sorted := append([]FeedItem(nil), items...)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].TS < sorted[j].TS })
	var keys []string
	var out []FeedItem
	for _, it := range sorted {
		k := dedupeKey(it.Text)
		dup := false
		for _, prev := range keys {
			if similar(k, prev) {
				dup = true
				break
			}
		}
		if !dup {
			keys = append(keys, k)
			out = append(out, it)
		}
	}
	return out
}

// dedupeKey: רק אותיות וספרות, כדי שהבדלי רווחים/סימנים לא ישנו.
func dedupeKey(s string) string {
	var b strings.Builder
	for _, r := range s {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// similar: זהות מלאה, או שאחת מכילה את השנייה והתוספת קטנה (עד רבע)
// — למשל "הועבר מ..." או חתימת ערוץ שנוספה בסוף.
func similar(a, b string) bool {
	if a == b {
		return true
	}
	la, lb := len([]rune(a)), len([]rune(b))
	if la < 30 || lb < 30 {
		return false
	}
	short, long := a, b
	ls, ll := la, lb
	if la > lb {
		short, long, ls, ll = b, a, lb, la
	}
	return strings.Contains(long, short) && float64(ls) >= 0.75*float64(ll)
}
