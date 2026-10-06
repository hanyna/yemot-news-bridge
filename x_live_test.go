package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"sort"
	"strings"
	"testing"
	"time"
)

// TestXLive: בדיקה אמיתית מול השירות (רק עם X_LIVE=1 — לא בבדיקות הרגילות).
// מדפיסה מה הקו היה מקריא מכל חשבון.
func TestXLive(t *testing.T) {
	list := os.Getenv("X_LIVE")
	if list == "" {
		t.Skip("X_LIVE לא מוגדר")
	}
	setNitterHosts(os.Getenv("X_NITTER"))
	s := newXSource(list, os.Getenv("X_API"))
	items, chans := s.poll(time.Now())
	for _, a := range s.accts {
		fmt.Printf("\n## %s — שם: %q, ציוצים שנכנסים לקו: %d, תקלה: %q\n", a.handle, a.title, len(a.items), a.lastErr)
	}
	sort.Slice(items, func(i, j int) bool { return items[i].TS > items[j].TS })
	for i, it := range items {
		if i >= 12 {
			break
		}
		media := ""
		for _, k := range []string{`<img`, `<video`} {
			if strings.Contains(it.HTML, k) {
				media += " [" + strings.Trim(k, "<") + "]"
			}
		}
		txt := []rune(strings.ReplaceAll(it.Text, "\n", " "))
		if len(txt) > 110 {
			txt = append(txt[:110], '…')
		}
		fmt.Printf("- %s | %s%s | %s\n", it.Channel, time.Unix(it.TS, 0).In(time.FixedZone("IL", 3*3600)).Format("02/01 15:04"), media, string(txt))
	}
	if len(chans) != len(s.accts) {
		t.Fatal("chans")
	}
	for _, a := range s.accts {
		if a.lastErr != "" || len(a.items) == 0 {
			t.Errorf("%s: %q, %d ציוצים", a.handle, a.lastErr, len(a.items))
		}
	}
}

// TestXLiveWhy: למה ציוצים סוננו (ריטוויט / תגובה / כותב אחר).
func TestXLiveWhy(t *testing.T) {
	h := os.Getenv("X_WHY")
	if h == "" {
		t.Skip()
	}
	s := newXSource(h, "")
	body, code, err := s.get("/2/profile/" + h + "/statuses")
	var r struct {
		Results []xStatus `json:"results"`
	}
	jerr := json.Unmarshal(body, &r)
	fmt.Printf("code %d err %v json %v results %d\n", code, err, jerr, len(r.Results))
	for _, x := range r.Results {
		txt := []rune(strings.ReplaceAll(x.Text, "\n", " "))
		if len(txt) > 60 {
			txt = txt[:60]
		}
		fmt.Printf("- author=%s reposted_by=%.60s replying_to=%.60s | %s | %s\n", x.Author.ScreenName, string(x.RepostedBy), string(x.ReplyingTo), x.CreatedAt, string(txt))
	}
}

// TestLineDiag: מה יש בפועל בקו מטוויטר (רק עם DIAG=1 ומפתח ימות — בהרצה ידנית).
func TestLineDiag(t *testing.T) {
	key := cleanKey(os.Getenv("YEMOT_API_KEY"))
	if os.Getenv("DIAG") == "" || key == "" {
		t.Skip()
	}
	y := &yemot{client: &http.Client{Timeout: 30 * time.Second}, apiKey: key}
	il := time.FixedZone("IL", 3*3600)
	show := func(ext string, onlyX bool) {
		txt, ok, err := y.read(ext, archiveIndex)
		if err != nil || !ok {
			fmt.Printf("\n### שלוחה %s: %v %v\n", ext, ok, err)
			return
		}
		f := parseArchive(txt)
		fmt.Printf("\n### שלוחה %s (ערוץ %q) next=%d last=%s\n", ext, f.channel, f.next, time.Unix(f.last, 0).In(il).Format("02/01 15:04"))
		var list []*archEntry
		for _, e := range f.entries {
			if !onlyX || isXChannel(channelOfKey(e.key)) {
				list = append(list, e)
			}
		}
		sort.Slice(list, func(i, j int) bool { return list[i].base > list[j].base })
		for i, e := range list {
			if i >= 25 {
				break
			}
			fmt.Printf("  %6d  %s  %s\n", e.base, time.Unix(e.ts, 0).In(il).Format("02/01 15:04"), e.key)
		}
	}
	show("1", false)
	for i := 1; i <= 9; i++ {
		txt, ok, _ := y.read(fmt.Sprintf("2/%d", i), archiveIndex)
		if ok && strings.Contains(txt, "channel=x-") {
			show(fmt.Sprintf("2/%d", i), false)
		}
	}
	s := newXSource("ariel__danino,meiretingr", "")
	for _, a := range s.accts {
		body, code, err := s.get("/2/profile/" + a.handle + "/statuses")
		var r struct {
			Results []xStatus `json:"results"`
		}
		json.Unmarshal(body, &r)
		fmt.Printf("\n### טוויטר %s: %d %v, %d ציוצים\n", a.handle, code, err, len(r.Results))
		sort.Slice(r.Results, func(i, j int) bool { return r.Results[i].when().After(r.Results[j].when()) })
		for i, x := range r.Results {
			if i >= 8 {
				break
			}
			txt := []rune(strings.ReplaceAll(x.Text, "\n", " "))
			if len(txt) > 50 {
				txt = txt[:50]
			}
			fmt.Printf("  %s id=%s by=%s rt=%v | %s\n", x.when().In(il).Format("02/01 15:04"), x.ID, x.Author.ScreenName, !isNullJSON(x.RepostedBy), string(txt))
		}
	}
}
