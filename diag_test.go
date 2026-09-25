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

	_ = gkey
	_ = feedKey
	_ = feedURL
	var _ = sort.Strings
	var _ = time.Now
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
			if i >= len(tts)-15 || strings.Contains(c, "תמונ") {
				r := []rune(c)
				if len(r) > 110 {
					r = r[:110]
				}
				fmt.Fprintf(&b, "%s (%d chars) has-desc=%v: %s\n", tts[i], len([]rune(c)), strings.Contains(c, "בתמונה:") || strings.Contains(c, "בתמונות:"), string(r))
			}
		}
		idx, _, err := y.read("1", archiveIndex)
		fmt.Fprintf(&b, "archive.txt len=%d err=%v\n", len(idx), err)
		note("5-index", idx[max(0, len(idx)-2500):])
	}
	note("4-line", b.String())
}
