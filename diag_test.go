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

func TestDiagNew(t *testing.T) {
	y := &yemot{client: &http.Client{Timeout: 30 * time.Second}, apiKey: cleanKey(os.Getenv("YEMOT_API_KEY"))}
	info, _ := y.dir("1")
	have := map[string]bool{}
	for _, f := range info.Files {
		have[f] = true
	}
	idx, _, _ := y.read("1", archiveIndex)
	af := parseArchive(idx)
	n, v, fl := 0, 0, 0
	var miss []string
	for _, en := range af.entries {
		if en.base < 0 || time.Since(time.Unix(en.ts, 0)) > 24*time.Hour {
			continue
		}
		n++
		if have[speechFile(en.base)] {
			v++
		} else {
			miss = append(miss, time.Unix(en.ts, 0).In(time.FixedZone("IL", 3*3600)).Format("15:04"))
		}
		if en.voiced {
			fl++
		}
	}
	fmt.Printf("::notice title=new::24h: msgs=%d voice=%d flag=%d missing=%s\n", n, v, fl, strings.Join(miss, ","))
}
