package web

import (
	"io"
	"net/http"
	"strings"
	"testing"

	"ticketd/internal/buildinfo"
)

// The footer shows the stamped version, and shows nothing extra for
// unstamped ("dev") builds.
func TestFooterVersion(t *testing.T) {
	srv, _ := newTestServer(t)
	defer srv.Close()

	body := getPage(t, srv.URL+"/login")
	if strings.Contains(body, ">ticketd - ") {
		t.Fatal("dev build footer must not show a version")
	}

	buildinfo.Version = "v9.9.9-test"
	defer func() { buildinfo.Version = "dev" }()

	body = getPage(t, srv.URL+"/login")
	if !strings.Contains(body, ">ticketd - v9.9.9-test</footer>") {
		t.Fatal("footer must show the stamped version")
	}
}

func getPage(t *testing.T, url string) string {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
