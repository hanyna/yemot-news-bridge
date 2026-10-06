package main

import (
	"encoding/binary"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"
	"testing"
	"time"
)

// TestVoiceDiag (זמני): לכל שלוחה — ההודעות האחרונות: אורך הטקסט מול אורך הקול.
func TestVoiceDiag(t *testing.T) {
	key := cleanKey(os.Getenv("YEMOT_API_KEY"))
	if os.Getenv("VOICE_DIAG") == "" || key == "" {
		t.Skip()
	}
	y := &yemot{client: &http.Client{Timeout: 60 * time.Second}, apiKey: key}
	il := time.FixedZone("IL", 3*3600)
	dl := func(ext, file string) []byte {
		req, _ := http.NewRequest("GET", yemotBase+"DownloadFile?path="+url.QueryEscape(ivrPath(ext, file)), nil)
		req.Header.Set("Authorization", key)
		resp, err := y.client.Do(req)
		if err != nil {
			return nil
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return b
	}
	dur := func(b []byte) (float64, string) {
		if len(b) < 44 || string(b[:4]) != "RIFF" {
			return 0, fmt.Sprintf("לא wav (%d בתים: %.40q)", len(b), b)
		}
		ch := binary.LittleEndian.Uint16(b[22:24])
		rate := binary.LittleEndian.Uint32(b[24:28])
		br := binary.LittleEndian.Uint32(b[28:32])
		if br == 0 {
			return 0, "?"
		}
		return float64(len(b)-44) / float64(br), fmt.Sprintf("%dHz/%dch", rate, ch)
	}
	exts := []string{"1"}
	for i := 1; i <= 9; i++ {
		exts = append(exts, fmt.Sprintf("2/%d", i))
	}
	exts = append(exts, "3/2")
	for _, ext := range exts {
		txt, ok, err := y.read(ext, archiveIndex)
		if err != nil || !ok {
			continue
		}
		f := parseArchive(txt)
		var list []*archEntry
		for _, e := range f.entries {
			if e.base >= 0 {
				list = append(list, e)
			}
		}
		sort.Slice(list, func(i, j int) bool { return list[i].base > list[j].base })
		d, _ := y.dir(ext)
		have := map[string]bool{}
		for _, n := range d.Files {
			have[n] = true
		}
		nt, nw := 0, 0
		for _, e := range list {
			if have[introFile(e.base)] {
				nt++
				if have[speechFile(e.base)] {
					nw++
				}
			}
		}
		fmt.Printf("\n### שלוחה %s (ערוץ %q) הודעות=%d, עם קול=%d\n", ext, f.channel, nt, nw)
		for i, e := range list {
			if i >= 8 {
				break
			}
			text, _, _ := y.read(ext, introFile(e.base))
			r := []rune(strings.TrimSpace(text))
			line := fmt.Sprintf("  %s %s %s | %d תווים", introFile(e.base), time.Unix(e.ts, 0).In(il).Format("02/01 15:04"), e.key, len(r))
			if have[speechFile(e.base)] {
				sec, info := dur(dl(ext, speechFile(e.base)))
				cps := 0.0
				if sec > 0 {
					cps = float64(len(r)) / sec
				}
				line += fmt.Sprintf(" | קול %.1f שניות (%s) | %.1f תווים לשנייה", sec, info, cps)
			} else {
				line += " | בלי קול"
			}
			if len(r) > 90 {
				r = r[:90]
			}
			fmt.Println(line + " | " + string(r))
		}
	}
}
