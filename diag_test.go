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

func TestDiagExt0(t *testing.T) {
	y := &yemot{client: &http.Client{Timeout: 30 * time.Second}, apiKey: cleanKey(os.Getenv("YEMOT_API_KEY"))}
	var b strings.Builder
	for _, e := range []string{"0", "4"} {
		ini, _, err := y.read(e, "ext.ini")
		ini = strings.ReplaceAll(ini, "password=password_admin", "PW-ADMIN")
		fmt.Fprintf(&b, "[%s] %s err=%v\n", e, strings.ReplaceAll(ini, "\n", " | "), err)
	}
	fmt.Printf("::notice title=ext0::%s\n", strings.ReplaceAll(b.String(), "\n", "%0A"))
}
