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
	s := newSpeaker(os.Getenv("GEMINI_API_KEY"), "Charon", "on")
	os.MkdirAll("samples", 0o755)
	var rep strings.Builder
	for i, f := range []string{"10215.tts", "10191.tts", "10205.tts"} {
		text, _, err := y.read("1", f)
		if err != nil || text == "" {
			fmt.Fprintf(&rep, "%s read: %v\n", f, err)
			continue
		}
		t0 := time.Now()
		wav, model, err := s.synthesize(text)
		if err == nil {
			wav, err = prepareSpeech(wav)
		}
		if err != nil {
			fmt.Fprintf(&rep, "%s: %v\n", f, err)
			continue
		}
		os.WriteFile(fmt.Sprintf("samples/line-%d.wav", i+1), wav, 0o644)
		fmt.Fprintf(&rep, "%s: %d chars, %s, %.1fs audio, took %v\n", f, len([]rune(text)), model, float64(len(wav)-44)/16000, time.Since(t0).Round(100*time.Millisecond))
	}
	fmt.Printf("::notice title=tts::%s\n", strings.ReplaceAll(rep.String(), "\n", "%0A"))
}
