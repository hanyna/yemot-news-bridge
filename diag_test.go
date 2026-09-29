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

func TestDiagExt3(t *testing.T) {
	y := &yemot{client: &http.Client{Timeout: 30 * time.Second}, apiKey: cleanKey(os.Getenv("YEMOT_API_KEY"))}
	var b strings.Builder
	for _, e := range []string{"", "3", "3/1"} {
		info, _ := y.dir(e)
		m, _, _ := y.read(e, "M1000.tts")
		t9, _, _ := y.read(e, titleFile)
		var menu []string
		for _, f := range info.Files {
			if strings.HasPrefix(f, "M1000") || strings.HasPrefix(f, "99999") {
				menu = append(menu, f)
			}
		}
		fmt.Fprintf(&b, "[%s] %v M1000=%q title=%q\n", e, menu, m, t9)
	}
	fmt.Printf("::notice title=ext3::%s\n", strings.ReplaceAll(strings.ReplaceAll(b.String(), "%", "%25"), "\n", "%0A"))
}
