package main

import (
	"os"
	"testing"
)

// TestMain: הבדיקות הישנות נכתבו למספור רצוף (10001, 10003, ...). הן בודקות דברים
// אחרים, אז רצות עם צעד 2; המספור עם הרווחים נבדק בבדיקות שמגדירות archStride בעצמן.
func TestMain(m *testing.M) {
	speechLengthCheck = func(string, []byte) error { return nil } // הקול המזויף בבדיקות קצר מאוד
	archStride = 2
	os.Exit(m.Run())
}

func TestBridgeFileTilde(t *testing.T) {
	for _, n := range []string{"~10179.wav", "~10179.tts", "10179.tts"} {
		if !isBridgeFile(n) {
			t.Errorf("%s should be a bridge file", n)
		}
	}
	if isBridgeFile("~song.mp3") {
		t.Error("foreign file")
	}
}
