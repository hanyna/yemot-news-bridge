//go:build diag

package main

// בדיקה חד-פעמית (רק בענף diag-tts): האם אפשר להפיק קול עברי מוכן מראש
// עם המפתח של Google שכבר קיים. שומר דוגמאות בתיקייה samples/.

import (
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"
)

func note(title, msg string) {
	r := strings.NewReplacer("%", "%25", "\r", "", "\n", "%0A")
	if len(msg) > 3800 {
		msg = msg[:3800]
	}
	fmt.Printf("::notice title=%s::%s\n", title, r.Replace(msg))
}

const sample = `אלחנן גרונר, בשעה 8 ו 26 דקות בבוקר. הותרו לפרסום שמותיהם של שני חללי צהל שנהרגו אתמול בתאונה מבצעית ברצועת עזה. השם יקום דמם.`

func post(u, key string, body any) (int, []byte, time.Duration) {
	b, _ := json.Marshal(body)
	req, _ := http.NewRequest(http.MethodPost, u, bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-goog-api-key", key)
	t := time.Now()
	resp, err := (&http.Client{Timeout: 120 * time.Second}).Do(req)
	if err != nil {
		return 0, []byte(err.Error()), time.Since(t)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, raw, time.Since(t)
}

func pcmToWav(pcm []byte, rate int) []byte {
	var b bytes.Buffer
	w := func(v any) { binary.Write(&b, binary.LittleEndian, v) }
	b.WriteString("RIFF")
	w(uint32(36 + len(pcm)))
	b.WriteString("WAVEfmt ")
	w(uint32(16))
	w(uint16(1))
	w(uint16(1))
	w(uint32(rate))
	w(uint32(rate * 2))
	w(uint16(2))
	w(uint16(16))
	b.WriteString("data")
	w(uint32(len(pcm)))
	b.Write(pcm)
	return b.Bytes()
}

func short(b []byte) string {
	s := string(b)
	if len(s) > 300 {
		s = s[:300]
	}
	return s
}

func TestDiagTTS(t *testing.T) {
	key := cleanKey(os.Getenv("GEMINI_API_KEY"))
	os.MkdirAll("samples", 0o755)
	var rep strings.Builder

	// 1. אילו מודלי קול זמינים במפתח
	req, _ := http.NewRequest(http.MethodGet, "https://generativelanguage.googleapis.com/v1beta/models?pageSize=200", nil)
	req.Header.Set("x-goog-api-key", key)
	var ttsModels []string
	if resp, err := http.DefaultClient.Do(req); err == nil {
		var l struct {
			Models []struct {
				Name string `json:"name"`
			} `json:"models"`
		}
		json.NewDecoder(resp.Body).Decode(&l)
		resp.Body.Close()
		for _, m := range l.Models {
			if strings.Contains(m.Name, "tts") {
				ttsModels = append(ttsModels, strings.TrimPrefix(m.Name, "models/"))
			}
		}
	}
	fmt.Fprintf(&rep, "gemini tts models: %v\n", ttsModels)

	// 2. Gemini TTS
	for _, m := range []string{"gemini-3.8-flash-tts"} {
		for _, voice := range []string{"bare", "instr", "bare2"} {
			st, raw, d := post("https://generativelanguage.googleapis.com/v1beta/models/"+m+":generateContent", key, map[string]any{
				"contents": []any{map[string]any{"parts": []any{map[string]any{"text": map[string]string{"instr": "הקרא בעברית, בקול של קריין חדשות רגוע וברור: " + sample, "bare": sample, "bare2": sample + " " + sample}[voice]}}}},
				"generationConfig": map[string]any{
					"responseModalities": []string{"AUDIO"},
					"speechConfig": map[string]any{"voiceConfig": map[string]any{"prebuiltVoiceConfig": map[string]any{"voiceName": "Charon"}}},
				},
			})
			if st != 200 {
				fmt.Fprintf(&rep, "gemini %s %s: HTTP %d (%v) %s\n", m, voice, st, d.Round(time.Millisecond), short(raw))
				continue
			}
			var r struct {
				Candidates []struct {
					Content struct {
						Parts []struct {
							InlineData struct {
								MimeType string `json:"mimeType"`
								Data     string `json:"data"`
							} `json:"inlineData"`
						} `json:"parts"`
					} `json:"content"`
				} `json:"candidates"`
			}
			json.Unmarshal(raw, &r)
			if len(r.Candidates) == 0 || len(r.Candidates[0].Content.Parts) == 0 {
				fmt.Fprintf(&rep, "gemini %s %s: no audio %s\n", m, voice, short(raw))
				continue
			}
			p := r.Candidates[0].Content.Parts[0].InlineData
			pcm, _ := base64.StdEncoding.DecodeString(p.Data)
			name := fmt.Sprintf("samples/gemini-%s-%s.wav", m, voice)
			os.WriteFile(name, pcmToWav(pcm, 24000), 0o644)
			fmt.Fprintf(&rep, "gemini %s %s: OK %s %d bytes, %.1fs audio, took %v\n", m, voice, p.MimeType, len(pcm), float64(len(pcm))/48000, d.Round(time.Millisecond))
		}
		time.Sleep(3 * time.Second)
	}

	note("tts", rep.String())
}
