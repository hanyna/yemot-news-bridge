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

func TestDiagLong3(t *testing.T) {
	y := &yemot{client: &http.Client{Timeout: 30 * time.Second}, apiKey: cleanKey(os.Getenv("YEMOT_API_KEY"))}
	var b strings.Builder
	for _, c := range [][2]string{{"1", "10179"}, {"1", "10191"}, {"1", "10195"}, {"2/1", "10049"}, {"2/7", "10053"}} {
		info, _ := y.dir(c[0])
		txt, _, _ := y.read(c[0], c[1]+"-full.txt")
		r := []rune(txt)
		fmt.Fprintf(&b, "[%s] %s: טקסט מלא=%d תווים (סוף: %q), קול=%v\n", c[0], c[1], len(r), string(r[max(0, len(r)-40):]), hasName(info.Files, c[1]+".wav"))
	}
	fmt.Printf("::notice title=long::%s\n", strings.ReplaceAll(b.String(), "\n", "%0A"))
}
