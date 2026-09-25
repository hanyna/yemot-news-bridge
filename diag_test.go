//go:build diag

package main

import (
	"fmt"
	"os"
	"strings"
	"testing"
)

func TestDiagQ(t *testing.T) {
	s := newSpeaker(os.Getenv("GEMINI_API_KEY"), "Charon", "on")
	var b strings.Builder
	body := []byte(`{"contents":[{"parts":[{"text":"שלום לכולם, זו הודעת בדיקה קצרה."}]}],"generationConfig":{"responseModalities":["AUDIO"],"speechConfig":{"voiceConfig":{"prebuiltVoiceConfig":{"voiceName":"Charon"}}}}}`)
	for _, m := range speechModels {
		_, st, err := s.call(m, body)
		fmt.Fprintf(&b, "%s: %d %v\n", m, st, err)
	}
	fmt.Printf("::notice title=q::%s\n", strings.ReplaceAll(b.String(), "\n", "%0A"))
}
