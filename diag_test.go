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

func TestDiagLong(t *testing.T) {
	y := &yemot{client: &http.Client{Timeout: 30 * time.Second}, apiKey: cleanKey(os.Getenv("YEMOT_API_KEY"))}
	var b strings.Builder
	for _, c := range [][2]string{{"1", "10179"}, {"1", "10191"}, {"1", "10195"}, {"2/1", "10049"}, {"2/7", "10053"}} {
		info, _ := y.dir(c[0])
		txt, _, _ := y.read(c[0], c[1]+".txt")
		fmt.Fprintf(&b, "[%s] %s: txt=%d תווים, קול=%v\n", c[0], c[1], len([]rune(txt)), hasName(info.Files, c[1]+".wav"))
	}
	for _, e := range []string{"1", "2/1", "2/2", "2/3", "2/4", "2/7"} {
		info, _ := y.dir(e)
		tts, wav := 0, 0
		for _, f := range info.Files {
			if v := fileNum(f); isArchiveNum(v) && v%2 == 1 {
				if strings.HasSuffix(f, ".tts") {
					tts++
				} else if strings.HasSuffix(f, ".wav") {
					wav++
				}
			}
		}
		fmt.Fprintf(&b, "[%s] הודעות=%d, עם קול מוכן=%d\n", e, tts, wav)
	}
	fmt.Printf("::notice title=long::%s\n", strings.ReplaceAll(b.String(), "\n", "%0A"))
}
