//go:build diag

package main

import (
	"fmt"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"
)

func TestDiagBeep(t *testing.T) {
	y := &yemot{client: &http.Client{Timeout: 30 * time.Second}, apiKey: cleanKey(os.Getenv("YEMOT_API_KEY"))}
	var b strings.Builder
	for _, e := range []string{"1", "2/1", "2/2", "2/3", "2/4", "2/6", "2/7"} {
		ini, _, _ := y.read(e, "ext.ini")
		info, _ := y.dir(e)
		tts, wav := 0, 0
		var noWav []string
		have := map[string]bool{}
		for _, f := range info.Files {
			have[f] = true
		}
		for _, f := range info.Files {
			if v := fileNum(f); isArchiveNum(v) && v%2 == 1 && strings.HasSuffix(f, ".tts") {
				tts++
				if have[strings.TrimSuffix(f, ".tts")+".wav"] {
					wav++
				} else {
					noWav = append(noWav, f)
				}
			}
		}
		if len(noWav) > 6 {
			noWav = noWav[len(noWav)-6:]
		}
		fmt.Fprintf(&b, "[%s] %q | הודעות=%d עם קול מוכן=%d | החדשות בלי קול: %v\n", e, strings.ReplaceAll(ini, "\n", " | "), tts, wav, noWav)
	}
	fmt.Printf("::notice title=beep::%s\n", strings.ReplaceAll(b.String(), "\n", "%0A"))
}
