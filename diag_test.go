//go:build diag

package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"
)

func TestDiagTr(t *testing.T) {
	key := cleanKey(os.Getenv("GEMINI_API_KEY"))
	ykey := cleanKey(os.Getenv("YEMOT_API_KEY"))
	var b strings.Builder
	for _, p := range []string{"3/M1000.wav", "3/1/99999.wav", "M1000.wav"} {
		req, _ := http.NewRequest("GET", yemotBase+"DownloadFile?path="+url.QueryEscape("ivr2:/"+p), nil)
		req.Header.Set("Authorization", ykey)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			fmt.Fprintf(&b, "%s: %v\n", p, err)
			continue
		}
		data, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		mime := http.DetectContentType(data)
		if len(data) > 12 && string(data[8:12]) == "WAVE" {
			mime = "audio/wav"
		}
		body, _ := json.Marshal(map[string]any{"contents": []any{map[string]any{"parts": []any{
			map[string]any{"text": "תמלל מילה במילה את מה שנאמר בהקלטה, בעברית. אם יש רק מוזיקה, כתוב [מוזיקה]."},
			map[string]any{"inlineData": map[string]any{"mimeType": mime, "data": base64.StdEncoding.EncodeToString(data)}}}}}})
		gr, _ := http.NewRequest("POST", "https://generativelanguage.googleapis.com/v1beta/models/gemini-flash-latest:generateContent", bytes.NewReader(body))
		gr.Header.Set("Content-Type", "application/json")
		gr.Header.Set("x-goog-api-key", key)
		c := &http.Client{Timeout: 90 * time.Second}
		r2, err := c.Do(gr)
		if err != nil {
			fmt.Fprintf(&b, "%s: gemini %v\n", p, err)
			continue
		}
		raw, _ := io.ReadAll(r2.Body)
		r2.Body.Close()
		var g struct {
			Candidates []struct {
				Content struct {
					Parts []struct{ Text string } `json:"parts"`
				} `json:"content"`
			} `json:"candidates"`
		}
		json.Unmarshal(raw, &g)
		txt := string(raw)
		if len(g.Candidates) > 0 && len(g.Candidates[0].Content.Parts) > 0 {
			txt = g.Candidates[0].Content.Parts[0].Text
		}
		if len(txt) > 600 {
			txt = txt[:600]
		}
		fmt.Fprintf(&b, "%s (%d bytes, %s): %s\n", p, len(data), mime, strings.TrimSpace(txt))
	}
	fmt.Printf("::notice title=tr::%s\n", strings.ReplaceAll(strings.ReplaceAll(b.String(), "%", "%25"), "\n", "%0A"))
}
