//go:build diag

package main

import (
	"fmt"
	"net/http"
	"os"
	"sort"
	"strings"
	"testing"
	"time"
)

func TestDiagNew(t *testing.T) {
	y := &yemot{client: &http.Client{Timeout: 30 * time.Second}, apiKey: cleanKey(os.Getenv("YEMOT_API_KEY"))}
	loc, _ := time.LoadLocation("Asia/Jerusalem")
	var b strings.Builder
	info, _ := y.dir("1")
	have := map[string]bool{}
	for _, f := range info.Files {
		have[f] = true
	}
	idx, _, _ := y.read("1", archiveIndex)
	af := parseArchive(idx)
	var list []*archEntry
	for _, en := range af.entries {
		if en.base >= 0 && time.Since(time.Unix(en.ts, 0)) < 5*time.Hour {
			list = append(list, en)
		}
	}
	sort.Slice(list, func(i, j int) bool { return list[i].base > list[j].base })
	for _, en := range list {
		c, _, _ := y.read("1", introFile(en.base))
		r := []rune(c)
		fmt.Fprintf(&b, "%s %s voice=%v flag=%v | %s\n", introFile(en.base), time.Unix(en.ts, 0).In(loc).Format("15:04"), have[speechFile(en.base)], en.voiced, string(r[:min(60, len(r))]))
	}
	fmt.Printf("::notice title=new::%s\n", strings.ReplaceAll(b.String(), "\n", "%0A"))
}
