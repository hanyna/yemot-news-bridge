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
	for _, e := range []string{"1", "2/1", "2/2", "2/3", "2/4", "2/6", "2/7"} {
		info, _ := y.dir(e)
		have := map[string]bool{}
		for _, f := range info.Files {
			have[f] = true
		}
		idx, _, _ := y.read(e, archiveIndex)
		af := parseArchive(idx)
		var list []*archEntry
		for _, en := range af.entries {
			if en.base >= 0 {
				list = append(list, en)
			}
		}
		sort.Slice(list, func(i, j int) bool { return list[i].base > list[j].base })
		n := 3
		if e == "1" {
			n = 5
		}
		for i, en := range list {
			if i >= n || time.Since(time.Unix(en.ts, 0)) > 3*time.Hour {
				break
			}
			c, _, _ := y.read(e, introFile(en.base))
			r := []rune(c)
			fmt.Fprintf(&b, "[%s] %s %s קול=%v סימון=%v | %s\n", e, introFile(en.base), time.Unix(en.ts, 0).In(loc).Format("15:04"), have[speechFile(en.base)], en.voiced, string(r[:min(70, len(r))]))
		}
	}
	fmt.Printf("::notice title=new::%s\n", strings.ReplaceAll(b.String(), "\n", "%0A"))
}
