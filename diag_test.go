//go:build diag

package main

// בדיקה עמוקה של הקו — קריאה בלבד (לא כותב ולא מוחק כלום).

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"
)

var nc int

func out(title, msg string) {
	r := strings.NewReplacer("%", "%25", "\r", "", "\n", "%0A")
	for len(msg) > 0 && nc < 9 {
		c := msg
		if len(c) > 3900 {
			c = c[:3900]
			if i := strings.LastIndex(c, "\n"); i > 2000 {
				c = c[:i+1]
			}
		}
		msg = msg[len(c):]
		nc++
		fmt.Printf("::notice title=%s-%d::%s\n", title, nc, r.Replace(c))
	}
}

var reLatin = regexp.MustCompile(`[A-Za-z]{2,}`)

func TestDiagQuota(t *testing.T) {
	s := newSpeaker(os.Getenv("GEMINI_API_KEY"), "Charon", "on")
	var b strings.Builder
	body := []byte(`{"contents":[{"parts":[{"text":"בדיקה"}]}],"generationConfig":{"responseModalities":["AUDIO"],"speechConfig":{"voiceConfig":{"prebuiltVoiceConfig":{"voiceName":"Puck"}}}}}`)
	for _, m := range speechModels {
		_, st, err := s.call(m, body)
		fmt.Fprintf(&b, "%s: %d %v\n", m, st, err)
	}
	y := &yemot{client: &http.Client{Timeout: 30 * time.Second}, apiKey: cleanKey(os.Getenv("YEMOT_API_KEY"))}
	for _, e := range []string{"", "8", "2/4", "1"} {
		info, _ := y.dir(e)
		var w []string
		for _, f := range info.Files {
			if strings.HasSuffix(f, ".wav") && fileNum(f)%2 != 0 {
				w = append(w, f)
			}
		}
		fmt.Fprintf(&b, "[%s] wav: %v\n", e, w)
	}
	out("quota", b.String())
}

func TestDiagFull(t *testing.T) {
	y := &yemot{client: &http.Client{Timeout: 30 * time.Second}, apiKey: cleanKey(os.Getenv("YEMOT_API_KEY"))}
	loc, _ := time.LoadLocation("Asia/Jerusalem")
	now := time.Now()
	var b strings.Builder
	p := func(f string, a ...any) { fmt.Fprintf(&b, f, a...) }

	// ---- מבנה
	exts := []string{"", "1", "2", "3", "3/1", "5", "6", "7", "8", "8/1", "8/2", "8/3", "4", "9"}
	for x := 1; x <= 9; x++ {
		exts = append(exts, fmt.Sprintf("2/%d", x))
	}
	dirs := map[string]dirInfo{}
	for _, e := range exts {
		info, err := y.dir(e)
		if err != nil {
			p("[%s] שגיאה: %v\n", e, err)
			continue
		}
		dirs[e] = info
		if !info.Exists {
			p("[%s] לא קיימת\n", e)
			continue
		}
		var arch, wavs, speech int
		var other []string
		for _, f := range info.Files {
			v := fileNum(f)
			switch {
			case isArchiveNum(v):
				arch++
				if strings.HasSuffix(f, ".wav") {
					wavs++
					if v%2 == 1 {
						speech++
					}
				}
			default:
				other = append(other, f)
			}
		}
		sort.Strings(other)
		p("[%s] type=%s voice=%s rate=%s | קבצי הודעות=%d (wav=%d, מהם קול מוכן=%d) | אחרים=%v\n", e, info.Ini["type"], info.Ini["voice"], info.Ini["rate"], arch, wavs, speech, other)
	}
	out("structure", b.String())
	b.Reset()

	// ---- תפריטים
	for _, e := range []string{"", "2", "3", "8"} {
		c, ok, err := y.read(e, "M1000.tts")
		p("תפריט [%s] קיים=%v err=%v קול=%v: %s\n", e, ok, err, hasName(dirs[e].Files, "M1000.wav"), c)
	}
	for x := 1; x <= 9; x++ {
		e := fmt.Sprintf("2/%d", x)
		if !dirs[e].Exists {
			continue
		}
		c, _, _ := y.read(e, titleFile)
		idx, _, _ := y.read(e, archiveIndex)
		p("כותרת [%s] ערוץ=%s קול=%v: %s\n", e, parseArchive(idx).channel, hasName(dirs[e].Files, "99999.wav"), c)
	}
	out("menus", b.String())
	b.Reset()

	// ---- ארכיונים
	var ext1 archiveFile
	type textIssue struct{ ext, file, kind, text string }
	var issues []textIssue
	for _, e := range []string{"1", "2/1", "2/2", "2/3", "2/4", "2/5", "2/6", "2/7", "2/8", "2/9"} {
		info := dirs[e]
		if !info.Exists {
			continue
		}
		txt, ok, err := y.read(e, archiveIndex)
		if !ok || err != nil {
			p("[%s] אין archive.txt (%v)\n", e, err)
			continue
		}
		af := parseArchive(txt)
		if e == "1" {
			ext1 = af
		}
		have := map[string]bool{}
		top := -1
		for _, f := range info.Files {
			have[strings.ToLower(f)] = true
			if v := fileNum(f); isArchiveNum(v) && v > top {
				top = v
			}
		}
		var missing, skipped, day, dayVoice, pendAudio, doneNoFile int
		var missList, noVoice []string
		for _, en := range af.entries {
			if en.base < 0 {
				skipped++
				continue
			}
			if !have[introFile(en.base)] {
				missing++
				if len(missList) < 5 {
					missList = append(missList, en.key+"→"+introFile(en.base))
				}
			}
			age := now.Sub(time.Unix(en.ts, 0))
			if age < 24*time.Hour {
				day++
				if have[speechFile(en.base)] {
					dayVoice++
				} else if len(noVoice) < 8 {
					noVoice = append(noVoice, fmt.Sprintf("%s(%s)", introFile(en.base), time.Unix(en.ts, 0).In(loc).Format("15:04")))
				}
			}
			if en.audio == audioPending && age > time.Hour {
				pendAudio++
			}
			if en.audio == audioDone && !have[audioFile(en.base)] {
				doneNoFile++
			}
			// טקסטים — רק 36 השעות האחרונות
			if age < 36*time.Hour && have[introFile(en.base)] {
				c, _, err := y.read(e, introFile(en.base))
				if err != nil {
					continue
				}
				n := len([]rune(c))
				switch {
				case n > 1300:
					issues = append(issues, textIssue{e, introFile(en.base), fmt.Sprintf("ארוך מדי (%d)", n), c})
				case strings.Contains(c, "המשך ההודעה לא הוקרא"):
					issues = append(issues, textIssue{e, introFile(en.base), "נחתך", ""})
				}
				if m := reLatin.FindAllString(c, -1); len(m) > 0 {
					issues = append(issues, textIssue{e, introFile(en.base), "אנגלית", strings.Join(m, " ")})
				}
				if (strings.Contains(c, "פורסמה תמונה") || strings.Contains(c, "מצורף להודעה: תמונה") || strings.Contains(c, "תמונות")) && !strings.Contains(c, "בתמונ") {
					issues = append(issues, textIssue{e, introFile(en.base), "תמונה בלי תיאור", time.Unix(en.ts, 0).In(loc).Format("02/01 15:04")})
				}
				if strings.Contains(c, "בתמונ") {
					i := strings.Index(c, "בתמונ")
					issues = append(issues, textIssue{e, introFile(en.base), "תמונה עם תיאור", string([]rune(c[i:])[:min(90, len([]rune(c[i:])))])})
				}
			}
		}
		p("[%s] ערוץ=%q next=%d top=%d רשומות=%d (דולגו/כפולות=%d) | חסר קובץ=%d %v | 24 שעות: %d הודעות, עם קול מוכן %d, בלי: %v | קול סרטון תקוע=%d, קול 'הועלה' שלא קיים=%d\n",
			e, af.channel, af.next, top, len(af.entries), skipped, missing, missList, day, dayVoice, noVoice, pendAudio, doneNoFile)
	}
	out("archives", b.String())
	b.Reset()
	sort.Slice(issues, func(i, j int) bool { return issues[i].kind+issues[i].ext+issues[i].file < issues[j].kind+issues[j].ext+issues[j].file })
	for _, is := range issues {
		p("%s | [%s] %s | %.200s\n", is.kind, is.ext, is.file, is.text)
	}
	out("texts", b.String())
	b.Reset()

	// ---- הקו מול השרת (שלוחה 1)
	items, err := fetchFeed(&http.Client{Timeout: 120 * time.Second}, envOr("TGPOPUP_URL", "https://telegram-popup.onrender.com/api/messages"), strings.TrimSpace(os.Getenv("TGPOPUP_KEY")))
	if err != nil {
		p("שרת ערוץ חי: שגיאה %v\n", err)
	} else {
		ex := parseExclude("SamariaUpdates")
		var raw []FeedItem
		for _, it := range items {
			if !ex[strings.ToLower(it.Channel)] {
				raw = append(raw, it)
			}
		}
		clean := prepareClean(raw)
		inClean := map[string]bool{}
		for _, it := range clean {
			inClean[itemKey(it)] = true
		}
		var newest int64
		per := map[string]int{}
		for _, it := range items {
			if it.TS > newest {
				newest = it.TS
			}
			if now.Sub(time.Unix(it.TS, 0)) < 24*time.Hour {
				per[it.Channel]++
			}
		}
		p("שרת ערוץ חי: %d הודעות, החדשה ביותר %s. ב-24 שעות לפי ערוץ: %v\n", len(items), time.Unix(newest, 0).In(loc).Format("15:04"), per)
		var miss, drop []string
		for _, it := range raw {
			if now.Sub(time.Unix(it.TS, 0)) > 24*time.Hour {
				continue
			}
			k := itemKey(it)
			if !inClean[k] {
				t := cleanForSpeech(it.Text)
				why := "ריק"
				if t != "" && isAd(t) {
					why = "פרסומת"
				}
				drop = append(drop, fmt.Sprintf("%s %s [%s] %.70s", time.Unix(it.TS, 0).In(loc).Format("15:04"), k, why, t))
				continue
			}
			if _, ok := ext1.entries[k]; !ok && it.TS > ext1.last-3600 && now.Sub(time.Unix(it.TS, 0)) > 3*time.Minute {
				miss = append(miss, fmt.Sprintf("%s %s %.60s", time.Unix(it.TS, 0).In(loc).Format("15:04"), k, cleanForSpeech(it.Text)))
			} else if _, ok := ext1.entries[k]; !ok && now.Sub(time.Unix(it.TS, 0)) > 3*time.Minute && it.TS >= promoSince {
				miss = append(miss, fmt.Sprintf("(ישנה) %s %s %.60s", time.Unix(it.TS, 0).In(loc).Format("15:04"), k, cleanForSpeech(it.Text)))
			}
		}
		p("לא נכנסו לקו (סוננו), 24 שעות: %d\n%s\n", len(drop), strings.Join(drop, "\n"))
		p("חסרות בשלוחה 1 (עברו סינון אבל לא בקו): %d\n%s\n", len(miss), strings.Join(miss, "\n"))
		_ = json.Marshal
	}
	out("feed", b.String())
}
