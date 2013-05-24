package main

import (
	"flag"
	"io"
	"io/ioutil"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

var (
	live = flag.String("live", "", `If specified, tests live server (e.g. "http://example.com/")`)
)

func TestURLs(t *testing.T) {
	data, err := ioutil.ReadFile("urls.txt")
	if err != nil {
		t.Fatalf("load urls: %s", err)
	}

	codes, good, total := map[int]int{200: 0}, 0, 0

	base := *live
	if base == "" {
		fe := setup()
		ts := httptest.NewServer(fe)
		base = ts.URL
		defer ts.Close()
	}
	t.Logf("Testing against %q...", base)

	lines := strings.Split(string(data), "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if len(line) == 0 {
			continue
		}

		pieces := strings.Fields(line)
		if len(pieces) != 2 {
			t.Errorf("bad URL line %q", line)
			continue
		}
		method, path := pieces[0], base+pieces[1]

		req, err := http.NewRequest(method, path, nil)
		if err != nil {
			t.Errorf("bad line %q: %s", line, err)
			continue
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Errorf("do(%q, %q): %s", method, path, err)
			continue
		}
		io.Copy(ioutil.Discard, resp.Body)
		resp.Body.Close()

		codes[resp.StatusCode]++
		total++

		if resp.StatusCode/100 >= 4 {
			t.Errorf("%d %s - %s", resp.StatusCode, http.StatusText(resp.StatusCode), line)
			continue
		}
		good++
	}

	t.Logf("Sucessfully requested %d of %d URLs", good, total)
	for code, count := range codes {
		t.Logf("   %3d x %3d %s", count, code, http.StatusText(code))
	}
}
