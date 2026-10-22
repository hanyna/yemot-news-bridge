package main

import (
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// fakeStyle: שרת מזויף לשני הממשקים — Interactions (עם הנחיה) ו-generateContent.
type fakeStyle struct {
	mu        sync.Mutex
	mode      string // "ok", "reject" (400), "noaudio" (200 בלי קול)
	styleHits []string
	plainHits int
	texts     []string
}

func (f *fakeStyle) handler(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	raw, _ := io.ReadAll(r.Body)
	if strings.HasSuffix(r.URL.Path, "/interactions") {
		var req struct {
			Model string `json:"model"`
			Input []struct {
				Type    string `json:"type"`
				Content []struct {
					Text        string `json:"text"`
					Annotations []struct {
						Type  string `json:"type"`
						Style string `json:"style"`
					} `json:"annotations"`
				} `json:"content"`
			} `json:"input"`
		}
		json.Unmarshal(raw, &req)
		if r.Header.Get("Api-Revision") == "" || len(req.Input) == 0 || len(req.Input[0].Content) == 0 ||
			len(req.Input[0].Content[0].Annotations) == 0 || req.Input[0].Content[0].Annotations[0].Type != "speech_metadata" {
			w.WriteHeader(400)
			io.WriteString(w, `{"error":{"message":"bad request shape"}}`)
			return
		}
		c := req.Input[0].Content[0]
		f.styleHits = append(f.styleHits, req.Model)
		switch f.mode {
		case "reject":
			w.WriteHeader(400)
			io.WriteString(w, `{"error":{"message":"Unknown name \"annotations\""}}`)
			return
		case "noaudio":
			io.WriteString(w, `{"id":"x","status":"completed","steps":[]}`)
			return
		}
		f.texts = append(f.texts, c.Text)
		// WAV מלא (ברירת המחדל של Interactions)
		wav := asWav([]byte(c.Text), "")
		json.NewEncoder(w).Encode(map[string]any{"steps": []any{
			map[string]any{"type": "user_input"},
			map[string]any{"type": "model_output", "content": []any{map[string]any{"type": "audio", "mime_type": "audio/wav", "data": base64.StdEncoding.EncodeToString(wav)}}},
		}})
		return
	}
	f.plainHits++
	var req struct {
		Contents []struct {
			Parts []struct {
				Text string `json:"text"`
			} `json:"parts"`
		} `json:"contents"`
	}
	json.Unmarshal(raw, &req)
	text := req.Contents[0].Parts[0].Text
	f.texts = append(f.texts, text)
	json.NewEncoder(w).Encode(map[string]any{"candidates": []any{map[string]any{"content": map[string]any{"parts": []any{
		map[string]any{"inlineData": map[string]any{"mimeType": "audio/L16;codec=pcm;rate=24000", "data": base64.StdEncoding.EncodeToString([]byte(text))}}}}}}})
}

func withFakeStyle(t *testing.T, f *fakeStyle) {
	srv := httptest.NewServer(http.HandlerFunc(f.handler))
	oldE, oldS := speechEndpoint, speechStyleEndpoint
	speechEndpoint = srv.URL + "/%s:generateContent"
	speechStyleEndpoint = srv.URL + "/interactions"
	t.Cleanup(func() { srv.Close(); speechEndpoint, speechStyleEndpoint = oldE, oldS })
}

// עם הנחיית סגנון: הטקסט נשלח כמו שהוא (שמות מנוקדים), ההנחיה בשדה נפרד, והקול מתקבל.
func TestStyleOK(t *testing.T) {
	f := &fakeStyle{mode: "ok"}
	withFakeStyle(t, f)
	s := newSpeaker("GKEY", "", "on")
	for i := 0; i < 2; i++ {
		data, model, err := s.synthesize("עדכוני אלישע ירד.", "")
		if err != nil {
			t.Fatal(err)
		}
		if model != speechModels[0] || string(data[44:]) != "עדכוני אֱלִישָׁע יֵרֵד." {
			t.Fatalf("model %q data %q", model, data[44:])
		}
	}
	if len(f.styleHits) != 2 || f.plainHits != 0 || !s.styleOK[speechModels[0]] {
		t.Fatalf("style %v plain %d", f.styleHits, f.plainHits)
	}
}

// הממשק דוחה / עונה בלי קול: ממשיכים בדרך הרגילה, ולא מנסים שוב עם הנחיה.
func TestStyleFallback(t *testing.T) {
	for _, mode := range []string{"reject", "noaudio"} {
		f := &fakeStyle{mode: mode}
		withFakeStyle(t, f)
		s := newSpeaker("GKEY", "", "on")
		for i := 0; i < 2; i++ {
			data, model, err := s.synthesize("שלום", "")
			if err != nil || model != speechModels[0] || string(data[44:]) != "שלום" {
				t.Fatalf("%s: %v %q", mode, err, model)
			}
		}
		if len(f.styleHits) != 1 || f.plainHits != 2 || !s.noStyle[speechModels[0]] {
			t.Fatalf("%s: style %v plain %d", mode, f.styleHits, f.plainHits)
		}
	}
}

// מודל ישן (2.5) — לא בממשק החדש.
func TestStyleOnlyNewModels(t *testing.T) {
	if speechStyleModels["gemini-2.5-flash-preview-tts"] {
		t.Fatal("2.5 should use generateContent")
	}
	for _, m := range speechModels[:3] {
		if !speechStyleModels[m] {
			t.Fatalf("%s missing", m)
		}
	}
}
