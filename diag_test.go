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
		info, err := y.dir(e)
		ini, _, _ := y.read(e, "ext.ini")
		m, _, _ := y.read(e, "M1000.tts")
		fmt.Fprintf(&b, "[%s] exists=%v err=%v ini=%q M1000=%q files=%v dirs=%v\n", e, info.Exists, err, strings.ReplaceAll(ini, "\n", " | "), m, info.Files, info.Dirs)
	}
	idx, _, _ := y.read("3/1", podcastIndex)
	t9, _, _ := y.read("3/1", titleFile)
	fmt.Fprintf(&b, "title=%q\npodcast.txt: %q\n", t9, idx)
	fmt.Printf("::notice title=ext3::%s\n", strings.ReplaceAll(strings.ReplaceAll(b.String(), "%", "%25"), "\n", "%0A"))
}
