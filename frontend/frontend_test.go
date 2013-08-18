// Copyright 2013 Google Inc. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package frontend

import (
	"crypto/tls"
	"io"
	"io/ioutil"
	"net/http"
	"net/http/httptest"
	urlpkg "net/url"
	"reflect"
	"strings"
	"testing"
)

type FuncTripper func(*http.Request) (*http.Response, error)

func (f FuncTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func TestBackendRequest(t *testing.T) {
	b := &Backend{
		Name:          "test",
		Root:          "/test",
		AllowHeader:   map[string]bool{"AllowThis": true},
		StripHeader:   map[string]bool{"StripThis": false},
		BodySizeLimit: 32,
		hosts: []*urlpkg.URL{
			{
				Scheme: "fake",
				Host:   "hostname",
				Path:   "/some/path",
			},
		},
	}

	tests := []struct {
		desc string

		// Input
		header http.Header
		body   io.Reader

		// Handler checks
		wantHeader http.Header // nil for "must not be present"
		wantBody   string

		// After checks
		afterHeader http.Header
	}{
		{
			desc:     "basic headers",
			body:     strings.NewReader("body"),
			wantBody: "body",
			wantHeader: http.Header{
				"Host":                {"fakehost"},
				"X-Forwarded-For":     {"1.2.3.4"},
				"X-Forwarded-Proto":   {"https"},
				"X-Gofr-Backend":      {"test"},
				"X-Gofr-Backend-Root": {"/test"},
			},
			afterHeader: http.Header{
				"X-Frame-Options":  {"sameorigin"},
				"X-Xss-Protection": {"1; mode=block"},
			},
		},
		{
			desc:     "body length",
			body:     strings.NewReader("<------------------------------>|delete me"),
			wantBody: "<------------------------------>",
		},
		{
			desc: "allowed headers",
			header: http.Header{
				"Accept":    {"implicit"},
				"AllowThis": {"explicit"},
			},
			body:     strings.NewReader("body"),
			wantBody: "body",
			wantHeader: http.Header{
				"Accept":    {"implicit"},
				"AllowThis": {"explicit"},
			},
		},
		{
			desc: "stripped headers",
			header: http.Header{
				"Via":       {"implicit"},
				"StripThis": {"explicit"},
			},
			body:     strings.NewReader("body"),
			wantBody: "body",
			wantHeader: http.Header{
				"Via":       nil,
				"StripThis": nil,
			},
		},
	}

	for _, test := range tests {
		b.RoundTripper = FuncTripper(func(inc *http.Request) (*http.Response, error) {
			if got, want := inc.Method, "GET"; got != want {
				t.Errorf("%s: method = %q, want %q", test.desc, got, want)
			}
			if got, want := inc.URL.Path, "/foo"; got != want {
				t.Errorf("%s: path = %q, want %q", test.desc, got, want)
			}
			if got, want := inc.URL.RawQuery, "q"; got != want {
				t.Errorf("%s: query = %q, want %q", test.desc, got, want)
			}
			for k, v := range test.wantHeader {
				if got, want := inc.Header[k], v; !reflect.DeepEqual(got, want) {
					t.Errorf("%s: header[%q] = %#v, want %#v", test.desc, k, got, want)
				}
			}
			body, err := ioutil.ReadAll(inc.Body)
			if err != nil {
				t.Fatalf("%s: reading body: %s", test.desc, err)
			}
			if got, want := string(body), test.wantBody; got != want {
				t.Errorf("%s: body = %q, want %q", test.desc, got, want)
			}
			if got, want := inc.ContentLength, int64(len(body)); got != want {
				t.Errorf("%s: content length = %d, want %d", test.desc, got, want)
			}
			return &http.Response{
				Status:     "200 OK",
				StatusCode: 200,
				Body:       ioutil.NopCloser(strings.NewReader("body")),
			}, nil
		})
		req, err := http.NewRequest("GET", "/foo?q", test.body)
		req.Host = "fakehost"
		req.RemoteAddr = "1.2.3.4:5678"
		req.Header = test.header
		req.TLS = &tls.ConnectionState{}
		if err != nil {
			t.Fatalf("%s: NewRequest(%q, %q, %#v): %s", test.desc, "GET", "/foo", test.body, err)
		}
		rec := httptest.NewRecorder()
		b.ServeHTTP(rec, req)
		for k, v := range test.afterHeader {
			if got, want := rec.HeaderMap[k], v; !reflect.DeepEqual(got, want) {
				t.Errorf("%s: after header[%q] = %#v, want %#v", test.desc, k, got, want)
			}
		}
	}
}
