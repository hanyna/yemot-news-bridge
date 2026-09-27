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
	ini, _, err := y.read("0/148", "ext.ini")
	ini = strings.ReplaceAll(ini, "password=password_admin", "PW-ADMIN")
	fmt.Printf("::notice title=ext0::%s err=%v\n", strings.ReplaceAll(ini, "\n", " | "), err)
}
