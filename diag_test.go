//go:build diag

package main

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"
)

func TestDiagQ2(t *testing.T) {
	key := cleanKey(os.Getenv("GEMINI_API_KEY"))
	var b strings.Builder
	body := `{"contents":[{"parts":[{"text":"שלום לכולם, זו הודעת בדיקה קצרה של הקו."}]}],"generationConfig":{"responseModalities":["AUDIO"],"speechConfig":{"voiceConfig":{"prebuiltVoiceConfig":{"voiceName":"Charon"}}}}}`
	for _, m := range speechModels {
		req, _ := http.NewRequest("POST", fmt.Sprintf(speechEndpoint, m), bytes.NewReader([]byte(body)))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("x-goog-api-key", key)
		t0 := time.Now()
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			fmt.Fprintf(&b, "%s: %v\n", m, err)
			continue
		}
		raw, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		s := string(raw)
		if resp.StatusCode == 200 {
			s = fmt.Sprintf("OK %d bytes", len(raw))
		}
		if len(s) > 900 {
			s = s[:900]
		}
		fmt.Fprintf(&b, "%s: %d (%v) %s\n", m, resp.StatusCode, time.Since(t0).Round(100*time.Millisecond), s)
	}
	fmt.Printf("::notice title=q::%s\n", strings.ReplaceAll(strings.ReplaceAll(b.String(), "%", "%25"), "\n", "%0A"))
}
