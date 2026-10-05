package main

import (
	"fmt"
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
	s := newXSource(list, "")
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
