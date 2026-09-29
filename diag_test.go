//go:build diag

package main

import (
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"
)

func TestDiagRaw(t *testing.T) {
	y := &yemot{client: &http.Client{Timeout: 30 * time.Second}, apiKey: cleanKey(os.Getenv("YEMOT_API_KEY"))}
	form := url.Values{}
	form.Set("path", "ivr2:/3")
	body, _, err := y.post("GetIVR2Dir", form)
	s := string(body)
	if i := strings.Index(s, `"files"`); i >= 0 {
		s = s[i:]
	}
	if len(s) > 1500 {
		s = s[:1500]
	}
	fmt.Printf("::notice title=raw::err=%v %s\n", err, strings.ReplaceAll(strings.ReplaceAll(s, "%", "%25"), "\n", " "))
}
