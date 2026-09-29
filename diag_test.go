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

func TestDiagExt3(t *testing.T) {
	y := &yemot{client: &http.Client{Timeout: 30 * time.Second}, apiKey: cleanKey(os.Getenv("YEMOT_API_KEY"))}
	var b strings.Builder
	for _, e := range []string{"", "2", "3", "8", "3/1"} {
		info, _ := y.dir(e)
		name := "M1000"
		if e == "3/1" {
			name = "99999"
		}
		sp, _, _ := y.read(e, name+"-spoken.txt")
		fmt.Fprintf(&b, "[%s] wav=%v spoken=%q\n", e, hasName(info.Files, name+".wav"), sp)
	}
	fmt.Printf("::notice title=ext3::%s\n", strings.ReplaceAll(strings.ReplaceAll(b.String(), "%", "%25"), "\n", "%0A"))
}
