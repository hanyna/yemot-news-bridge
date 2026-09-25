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

func TestDiagLine(t *testing.T) {
	y := &yemot{client: &http.Client{Timeout: 30 * time.Second}, apiKey: cleanKey(os.Getenv("YEMOT_API_KEY"))}
	var rep strings.Builder
	info, err := y.dir("1")
	if err != nil {
		t.Fatal(err)
	}
	files := archiveFiles(info.Files)
	have := map[string]bool{}
	for _, f := range files {
		have[f] = true
	}
	n := 0
	for i := len(files) - 1; i >= 0 && n < 14; i-- {
		f := files[i]
		if !strings.HasSuffix(f, ".tts") {
			continue
		}
		n++
		c, _, _ := y.read("1", f)
		r := []rune(c)
		head := string(r[:min(len(r), 70)])
		photo := ""
		if i := strings.Index(c, "בתמונ"); i >= 0 {
			photo = " PHOTO: " + string([]rune(c[i:])[:min(len([]rune(c[i:])), 110)])
		} else if strings.Contains(c, "תמונ") {
			photo = " (photo, no desc)"
		}
		wav := strings.TrimSuffix(f, ".tts") + ".wav"
		fmt.Fprintf(&rep, "%s voice=%v | %s%s\n", f, have[wav], head, photo)
	}
	fmt.Printf("::notice title=line::%s\n", strings.ReplaceAll(rep.String(), "\n", "%0A"))
}
