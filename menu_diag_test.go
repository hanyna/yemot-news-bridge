package main

import (
	"fmt"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"
)

// TestMenuDiag (זמני): תפריט בחירת הכתב והכותרות.
func TestMenuDiag(t *testing.T) {
	key := cleanKey(os.Getenv("YEMOT_API_KEY"))
	if os.Getenv("MENU_DIAG") == "" || key == "" {
		t.Skip()
	}
	y := &yemot{client: &http.Client{Timeout: 60 * time.Second}, apiKey: key}
	for _, f := range []string{"2/M1000.tts", "2/M1000-spoken.txt", "2/ext.ini", "3/M1000-spoken.txt"} {
		i := strings.LastIndex(f, "/")
		txt, ok, err := y.read(f[:i], f[i+1:])
		fmt.Printf("== %s (%v %v): %.500s\n", f, ok, err, txt)
	}
	for _, ext := range []string{"2", "3"} {
		d, err := y.dir(ext)
		fmt.Printf("== dir %s: %v dirs=%v files=%v\n", ext, err, d.Dirs, d.Files)
	}
	for i := 1; i <= 9; i++ {
		ext := fmt.Sprintf("2/%d", i)
		d, err := y.dir(ext)
		if !d.Exists {
			fmt.Printf("== %s: לא קיימת %v\n", ext, err)
			continue
		}
		idx, _, _ := y.read(ext, archiveIndex)
		ch := parseArchive(idx).channel
		sp, _, _ := y.read(ext, "99999-spoken.txt")
		ini, _, _ := y.read(ext, "ext.ini")
		var odd []string
		for _, n := range d.Files {
			if !isBridgeFile(n) && !isSystemFile(n) {
				odd = append(odd, n)
			}
		}
		fmt.Printf("== %s ערוץ=%q כותרת=%q wav=%v ini=%q זרים=%v קבצים=%d\n", ext, ch, sp, hasName(d.Files, "99999.wav"), strings.ReplaceAll(ini, "\n", " | "), odd, len(d.Files))
	}
}
