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

func TestDiagTTS(t *testing.T) {
	y := &yemot{client: &http.Client{Timeout: 30 * time.Second}, apiKey: cleanKey(os.Getenv("YEMOT_API_KEY"))}
	var rep strings.Builder
	for _, ext := range []string{"1", "2/3", "2/7"} {
		info, err := y.dir(ext)
		if err != nil {
			fmt.Fprintf(&rep, "%s: %v\n", ext, err)
			continue
		}
		files := archiveFiles(info.Files)
		var wavs []string
		for _, f := range files {
			if fileNum(f)%2 == 1 && strings.HasSuffix(f, ".wav") {
				wavs = append(wavs, f)
			}
		}
		last := files
		if len(last) > 8 {
			last = last[len(last)-8:]
		}
		fmt.Fprintf(&rep, "ext %s: files=%d speech-wavs=%d %v | newest: %v\n", ext, len(files), len(wavs), wavs, last)
	}
	fmt.Printf("::notice title=line::%s\n", strings.ReplaceAll(rep.String(), "\n", "%0A"))
}
