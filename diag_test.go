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

func TestDiagLong2(t *testing.T) {
	y := &yemot{client: &http.Client{Timeout: 30 * time.Second}, apiKey: cleanKey(os.Getenv("YEMOT_API_KEY"))}
	var b strings.Builder
	for _, c := range [][2]string{{"1", "10191"}, {"1", "10195"}, {"1", "10179"}} {
		for _, ext := range []string{".txt", ".tts"} {
			txt, _, err := y.read(c[0], c[1]+ext)
			r := []rune(txt)
			fmt.Fprintf(&b, "[%s] %s%s len=%d err=%v head=%q tail=%q\n", c[0], c[1], ext, len(r), err, string(r[:min(100, len(r))]), string(r[max(0, len(r)-60):]))
		}
	}
	idx, _, _ := y.read("1", archiveIndex)
	for _, l := range strings.Split(idx, "\n") {
		if strings.Contains(l, " 10190 ") || strings.Contains(l, " 10194 ") || strings.Contains(l, " 10178 ") {
			fmt.Fprintf(&b, "%s\n", l)
		}
	}
	fmt.Printf("::notice title=long::%s\n", strings.ReplaceAll(b.String(), "\n", "%0A"))
}
