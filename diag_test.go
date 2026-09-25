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

func TestDiagCov(t *testing.T) {
	y := &yemot{client: &http.Client{Timeout: 30 * time.Second}, apiKey: cleanKey(os.Getenv("YEMOT_API_KEY"))}
	var b strings.Builder
	for _, e := range []string{"1", "2/1", "2/2", "2/3", "2/4", "2/6", "2/7"} {
		info, _ := y.dir(e)
		have := map[string]bool{}
		for _, f := range info.Files {
			have[f] = true
		}
		tts, wav, recent, recentWav := 0, 0, 0, 0
		idx, _, _ := y.read(e, archiveIndex)
		af := parseArchive(idx)
		for _, en := range af.entries {
			if en.base < 0 || !have[introFile(en.base)] {
				continue
			}
			tts++
			w := have[speechFile(en.base)]
			if w {
				wav++
			}
			if time.Since(time.Unix(en.ts, 0)) < 3*24*time.Hour {
				recent++
				if w {
					recentWav++
				}
			}
		}
		fmt.Fprintf(&b, "[%s] הודעות=%d עם קול=%d | 3 ימים אחרונים: %d, עם קול %d\n", e, tts, wav, recent, recentWav)
	}
	fmt.Printf("::notice title=cov::%s\n", strings.ReplaceAll(b.String(), "\n", "%0A"))
}
