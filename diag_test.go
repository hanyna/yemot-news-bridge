//go:build diag

package main

// בדיקה חד-פעמית (רק בענף diag-vision): למה תמונות לא מקבלות תיאור בקו.
// מדווח דרך הערות (annotations) של GitHub — בלי לחשוף מפתחות.

import (
	"fmt"
	"net/http"
	"os"
	"sort"
	"strings"
	"testing"
	"time"
)

func note(title, msg string) {
	r := strings.NewReplacer("%", "%25", "\r", "", "\n", "%0A")
	if len(msg) > 3500 {
		msg = msg[:3500]
	}
	fmt.Printf("::notice title=%s::%s\n", title, r.Replace(msg))
}

func TestDiagVision(t *testing.T) {
	feedURL := envOr("TGPOPUP_URL", "https://telegram-popup.onrender.com/api/messages")
	feedKey := strings.TrimSpace(os.Getenv("TGPOPUP_KEY"))
	gkey := cleanKey(os.Getenv("GEMINI_API_KEY"))
	var b strings.Builder

	// 1. המפתח
	pre := ""
	if len(gkey) >= 4 {
		pre = gkey[:4]
	}
	fmt.Fprintf(&b, "GEMINI key: len=%d prefix=%q\n", len(gkey), pre)

	// 2. הפיד
	client := &http.Client{Timeout: 120 * time.Second}
	items, err := fetchFeed(client, feedURL, feedKey)
	if err != nil {
		fmt.Fprintf(&b, "feed error: %v\n", err)
		note("1-setup", b.String())
		return
	}
	now := time.Now()
	var photos []FeedItem
	for _, it := range items {
		if len(photoURLs(it.HTML, feedURL)) > 0 {
			photos = append(photos, it)
		}
	}
	sort.Slice(photos, func(i, j int) bool { return photos[i].TS > photos[j].TS })
	fmt.Fprintf(&b, "feed items=%d, with photo=%d\n", len(items), len(photos))
	nPhotoClass := 0
	for _, it := range items {
		if strings.Contains(it.HTML, `class="photo"`) {
			nPhotoClass++
		}
	}
	fmt.Fprintf(&b, "items containing class=photo: %d\n", nPhotoClass)
	for i, it := range photos {
		if i >= 5 {
			break
		}
		urls := photoURLs(it.HTML, feedURL)
		fmt.Fprintf(&b, "photo item %s age=%v urls=%d first=%s\n", itemKey(it), now.Sub(time.Unix(it.TS, 0)).Round(time.Minute), len(urls), shortURL(urls[0]))
	}
	note("1-setup", b.String())
	if len(photos) == 0 {
		// מראים דוגמה של HTML עם class=photo, אם יש
		for _, it := range items {
			if i := strings.Index(it.HTML, `class="photo"`); i >= 0 {
				note("1b-html", it.HTML[max(0, i-50):min(len(it.HTML), i+400)])
				break
			}
		}
		return
	}

	// 3. הורדת התמונה + כל מודל/כתובת
	v := newVisionClient(gkey, os.Getenv("GEMINI_MODEL"), "on")
	if v == nil {
		note("2-gemini", "vision client is nil (no key)")
		return
	}
	b.Reset()
	it := photos[0]
	urls := photoURLs(it.HTML, feedURL)
	for _, u := range urls {
		data, mime, err := v.fetchImage(u)
		fmt.Fprintf(&b, "fetch %s: bytes=%d mime=%s err=%v\n", shortURL(u), len(data), mime, err)
	}
	for e := range visionEndpoints {
		for m := range v.models {
			body := []byte(`{"contents":[{"role":"user","parts":[{"text":"say ok"}]}]}`)
			out, status, err := v.call(e, m, body)
			if len(out) > 80 {
				out = out[:80]
			}
			fmt.Fprintf(&b, "%s %s: status=%d err=%v out=%q\n", hostOf(visionEndpoints[e]), v.models[m], status, err, out)
		}
	}
	note("2-gemini", b.String())

	// 4. המסלול המלא על 3 התמונות האחרונות
	b.Reset()
	for i, it := range photos {
		if i >= 3 {
			break
		}
		desc, text, err := v.analyze(photoURLs(it.HTML, feedURL), cleanForSpeech(it.Text))
		fmt.Fprintf(&b, "%s: err=%v\n desc=%q\n text=%.150q\n", itemKey(it), err, desc, text)
		time.Sleep(5 * time.Second)
	}
	note("3-analyze", b.String())

	// 5. מה באמת יושב בשלוחה 1
	b.Reset()
	y := &yemot{client: &http.Client{Timeout: 30 * time.Second}, apiKey: cleanKey(os.Getenv("YEMOT_API_KEY"))}
	info, err := y.dir("1")
	if err != nil {
		fmt.Fprintf(&b, "dir 1 error: %v\n", err)
	} else {
		files := archiveFiles(info.Files)
		fmt.Fprintf(&b, "ext 1 files=%d\n", len(files))
		var tts []string
		for _, f := range files {
			if strings.HasSuffix(f, ".tts") {
				tts = append(tts, f)
			}
		}
		if len(tts) > 40 {
			tts = tts[len(tts)-40:]
		}
		for i := len(tts) - 1; i >= 0; i-- {
			c, _, err := y.read("1", tts[i])
			if err != nil {
				fmt.Fprintf(&b, "%s read err %v\n", tts[i], err)
				continue
			}
			if strings.Contains(c, "תמונ") {
				r := []rune(c)
				if len(r) > 220 {
					r = r[:220]
				}
				fmt.Fprintf(&b, "%s (%d chars) has-desc=%v: %s\n", tts[i], len([]rune(c)), strings.Contains(c, "בתמונה:") || strings.Contains(c, "בתמונות:"), string(r))
			}
		}
		idx, _, err := y.read("1", archiveIndex)
		fmt.Fprintf(&b, "archive.txt len=%d err=%v\n", len(idx), err)
	}
	note("4-line", b.String())
}
