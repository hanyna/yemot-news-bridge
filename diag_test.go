//go:build diag

package main

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"strings"
	"testing"
)

func TestDiagQ3(t *testing.T) {
	key := cleanKey(os.Getenv("GEMINI_API_KEY"))
	var b strings.Builder
	body := `{"contents":[{"parts":[{"text":"שלום."}]}],"generationConfig":{"responseModalities":["AUDIO"],"speechConfig":{"voiceConfig":{"prebuiltVoiceConfig":{"voiceName":"Charon"}}}}}`
	re := regexp.MustCompile(`"(quotaMetric|quotaId|quotaValue|retryDelay)":\s*"([^"]+)"`)
	for _, m := range append(speechModels, "gemini-flash-latest") {
		req, _ := http.NewRequest("POST", fmt.Sprintf(speechEndpoint, m), bytes.NewReader([]byte(body)))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("x-goog-api-key", key)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			continue
		}
		raw, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		var parts []string
		for _, x := range re.FindAllStringSubmatch(string(raw), -1) {
			parts = append(parts, x[1]+"="+x[2])
		}
		fmt.Fprintf(&b, "%s: %d %v\n", m, resp.StatusCode, parts)
	}
	fmt.Printf("::notice title=q::%s\n", strings.ReplaceAll(b.String(), "\n", "%0A"))
}
