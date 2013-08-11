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

package trie

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/kylelemons/godebug/pretty"
)

type textHandler string

func (s textHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	fmt.Fprintf(w, "%s", s)
}

func TestInsert(t *testing.T) {
	trie := NewDomain().Trie
	tests := []struct {
		paths []string
		leaf  textHandler
		after *Trie
		err   error
	}{{
		paths: []string{"foo", "baz"},
		leaf:  "/foo/baz handler",
		after: &Trie{
			Child: []*Trie{{
				Name: "foo",
				Child: []*Trie{{
					Name: "baz",
					Leaf: textHandler("/foo/baz handler"),
				}},
			}},
			Leaf: http.HandlerFunc(http.NotFound),
		},
	}, {
		paths: []string{"foo", "bar"},
		leaf:  "/foo/bar handler",
		after: &Trie{
			Child: []*Trie{{
				Name: "foo",
				Child: []*Trie{{
					Name: "bar",
					Leaf: textHandler("/foo/bar handler"),
				}, {
					Name: "baz",
					Leaf: textHandler("/foo/baz handler"),
				}},
			}},
			Leaf: http.HandlerFunc(http.NotFound),
		},
	}, {
		paths: []string{"foo", "baz"},
		leaf:  "/foo/baz handler 2",
		err:   fmt.Errorf("foo: baz: leaf already exists"),
	}}

	for idx, test := range tests {
		err := trie.Insert(test.paths, test.leaf)
		if err != nil || test.err != nil {
			if !reflect.DeepEqual(err, test.err) {
				t.Errorf("%d. insert: %v, want %v", idx, err, test.err)
			}
			continue
		}
		if cmp := pretty.Compare(trie, test.after); cmp != "" {
			t.Errorf("%d. after insert:\n%s", idx, cmp)
		}
	}
}

func TestFind(t *testing.T) {
	foobar := &Trie{
		Name: "bar",
		Leaf: textHandler("/foo/bar"),
	}
	foobaz := &Trie{
		Name: "baz",
		Leaf: textHandler("/foo/baz"),
	}
	foo := &Trie{
		Name:  "foo",
		Child: []*Trie{foobar, foobaz},
		Leaf:  textHandler("/foo"),
	}
	trie := &Trie{
		Child: []*Trie{foo},
		Leaf:  textHandler("/"),
	}

	tests := []struct {
		paths []string
		n     int
		leaf  textHandler
	}{{
		paths: []string{},
		n:     0,
		leaf:  "/",
	}, {
		paths: []string{"fox"},
		n:     0,
		leaf:  "/",
	}, {
		paths: []string{"foo"},
		n:     1,
		leaf:  "/foo",
	}, {
		paths: []string{"foo", "bar"},
		n:     2,
		leaf:  "/foo/bar",
	}, {
		paths: []string{"foo", "baz"},
		n:     2,
		leaf:  "/foo/baz",
	}}

	for _, test := range tests {
		n, found := trie.Find(test.paths)
		if got, want := n, test.n; got != want {
			t.Errorf("Find(%q).n = %v, want %v", test.paths, got, want)
		}
		if got, want := found.Leaf, test.leaf; got != want {
			t.Errorf("Find(%q) found %q, want %q", test.paths, got, want)
		}
	}
}

func TestHandle(t *testing.T) {
	mux := NewServeMux()
	tests := []struct {
		pattern string
		handler textHandler
		err     string // panic message
		after   *ServeMux
	}{
		{
			pattern: "",
			err:     `handle pattern "" is not in <domain>/<path> form`,
		},
		{
			pattern: "example.com",
			err:     `handle pattern "example.com" is not in <domain>/<path> form`,
		},
		{
			pattern: "/foo",
			handler: "/foo handler",
			after: &ServeMux{
				Trie: Trie{
					Leaf: &Domain{
						Trie: Trie{
							Child: []*Trie{{
								Name: "foo",
								Leaf: textHandler("/foo handler"),
							}},
							Leaf: http.HandlerFunc(http.NotFound),
						},
					},
				},
			},
		},
		{
			pattern: "/foo",
			err:     `foo: leaf already exists`,
		},
		{
			pattern: "/dir/",
			handler: "/dir/ index",
			after: &ServeMux{
				Trie: Trie{
					Leaf: &Domain{
						Trie: Trie{
							Child: []*Trie{{
								Name: "dir",
								Leaf: http.HandlerFunc(addSlash),
							}, {
								Name: "dir/",
								Leaf: textHandler("/dir/ index"),
							}, {
								Name: "foo",
								Leaf: textHandler("/foo handler"),
							}},
							Leaf: http.HandlerFunc(http.NotFound),
						},
					},
				},
			},
		},
		{
			pattern: "example.com/sub/dir/",
			handler: "example.com/sub/dir/ index",
			after: &ServeMux{
				Trie: Trie{
					Child: []*Trie{{
						Name: "com",
						Child: []*Trie{{
							Name: "example",
							Leaf: &Domain{
								Trie: Trie{
									Child: []*Trie{{
										Name: "sub/",
										Child: []*Trie{{
											Name: "dir",
											Leaf: http.HandlerFunc(addSlash),
										}, {
											Name: "dir/",
											Leaf: textHandler("example.com/sub/dir/ index"),
										}},
									}},
									Leaf: http.HandlerFunc(http.NotFound),
								},
							},
						}},
					}},
					Leaf: &Domain{
						Trie: Trie{
							Child: []*Trie{{
								Name: "dir",
								Leaf: http.HandlerFunc(addSlash),
							}, {
								Name: "dir/",
								Leaf: textHandler("/dir/ index"),
							}, {
								Name: "foo",
								Leaf: textHandler("/foo handler"),
							}},
							Leaf: http.HandlerFunc(http.NotFound),
						},
					},
				},
			},
		},
	}

	for idx, test := range tests {
		var err string
		func() {
			defer func() {
				if r := recover(); r != nil {
					err = fmt.Sprint(r)
				}
			}()
			mux.Handle(test.pattern, test.handler)
		}()
		if err != "" || test.err != "" {
			if got, want := err, test.err; got != want {
				t.Errorf("%d. Handle(%q): %q, want %q", idx, test.pattern, got, want)
			}
			continue
		}
		if cmp := pretty.Compare(mux, test.after); cmp != "" {
			t.Errorf("%d. after Handle(%q):\n%s", idx, test.pattern, cmp)
		}
	}
}

type fataler interface {
	Fatalf(string, ...interface{})
}

func request(t fataler, method, url string) *http.Request {
	r, err := http.NewRequest(method, url, nil)
	if err != nil {
		t.Fatalf("NewRequest(%q, %q): %s", method, url, err)
	}
	return r
}

func TestServeHTTP(t *testing.T) {
	tests := []struct {
		desc     string
		handlers []string
		req      *http.Request
		code     int
		redir    string
		body     string
	}{
		{
			desc:     "basic",
			handlers: []string{"/foo"},
			req:      request(t, "GET", "http://example.com/foo"),
			code:     200,
			body:     "/foo handler",
		},
		{
			desc:     "basic with domain",
			handlers: []string{"example.com/foo"},
			req:      request(t, "GET", "http://example.com/foo"),
			code:     200,
			body:     "example.com/foo handler",
		},
		{
			desc:     "other domain",
			handlers: []string{"example.com/foo"},
			req:      request(t, "GET", "http://example.net/foo"),
			code:     404,
		},
		{
			desc:     "sub domain",
			handlers: []string{"example.com/foo"},
			req:      request(t, "GET", "http://www.example.com/foo"),
			code:     302,
			redir:    "http://example.com/foo",
		},
		{
			desc:     "sub domain query",
			handlers: []string{"example.com/foo"},
			req:      request(t, "GET", "http://www.example.com/foo?q"),
			code:     302,
			redir:    "http://example.com/foo?q",
		},
		{
			desc:     "dir",
			handlers: []string{"/dir/"},
			req:      request(t, "GET", "/dir/"),
			code:     200,
			body:     "/dir/ handler",
		},
		{
			desc:     "file in non-dir",
			handlers: []string{"/foo"},
			req:      request(t, "GET", "/foo/sub"),
			code:     404,
		},
		{
			desc:     "file in dir",
			handlers: []string{"/dir/"},
			req:      request(t, "GET", "/dir/foo/bar"),
			code:     200,
			body:     "/dir/ handler",
		},
		{
			desc:     "dir redir",
			handlers: []string{"/dir/"},
			req:      request(t, "GET", "/dir"),
			code:     302,
			redir:    "/dir/",
		},
		{
			desc:     "dir redir query",
			handlers: []string{"/dir/"},
			req:      request(t, "GET", "/dir?foo"),
			code:     302,
			redir:    "/dir/?foo",
		},
		{
			desc:     "no dir redir",
			handlers: []string{"/dir", "/dir/"},
			req:      request(t, "GET", "/dir"),
			code:     200,
			body:     "/dir handler",
		},
		{
			desc:     "clean",
			handlers: []string{"/foo/bar"},
			req:      request(t, "GET", "/foo/baz/../bar"),
			code:     301,
			redir:    "/foo/bar",
		},
		{
			desc:     "clean query",
			handlers: []string{"/foo/bar"},
			req:      request(t, "GET", "/foo/baz/../bar?q"),
			code:     301,
			redir:    "/foo/bar?q",
		},
	}

	for _, test := range tests {
		mux := NewServeMux()
		for _, path := range test.handlers {
			mux.Handle(path, textHandler(path+" handler"))
		}
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, test.req)
		if got, want := w.Code, test.code; got != want {
			t.Errorf("%s: GET %q: %d %s, want %d %s", test.desc, test.req.URL,
				got, http.StatusText(got), want, http.StatusText(want))
		}
		switch w.Code / 100 {
		case 2:
			if got, want := w.Body.String(), test.body; got != want {
				t.Errorf("%s: GET %q: body = %q, want %q", test.desc, test.req.URL, got, want)
			}
		case 3:
			if got, want := w.HeaderMap.Get("Location"), test.redir; got != want {
				t.Errorf("%s: GET %q: redir = %q, want %q", test.desc, test.req.URL, got, want)
			}
		}
	}
}

func perms(length int, f func([]int)) {
	idx := make([]int, length)
	for i := range idx {
		idx[i] = i
	}
	for {
		f(idx)
		if len(idx) < 2 {
			break
		}
		k := len(idx) - 2
		for k >= 0 {
			if idx[k] < idx[k+1] {
				goto foundK
			}
			k--
		}
		break
	foundK:
		l := len(idx) - 1
		for l >= 0 {
			if idx[k] < idx[l] {
				goto foundL
			}
			l--
		}
	foundL:
		idx[k], idx[l] = idx[l], idx[k]
		i, j := k+1, len(idx)-1
		for ; i < j; i, j = i+1, j-1 {
			idx[i], idx[j] = idx[j], idx[i]
		}
	}
}

var benchPrefixes = []string{
	"",
	"golang.org",
	"example.com",
}

var benchPieces = []string{
	"/foo",
	"/dir",
	"/sub",
	"/go",
	"/python",
	"/spam",
	"/eggs!",
}

var benchSuffixes = []string{
	"",
	"/",
}

var benchRequests = []string{
	"/foo",
	"/foo/",
	"/foo/bar",
	"/foo/bar/",
	"/dir",
	"/dir/",
	"/dir/sub",
	"http://example.com/",
	"http://example.com/sub/",
	"http://example.com/sub/dir/",
	"http://example.com/sub/dir/file",
}

type serveMux interface {
	http.Handler
	Handle(string, http.Handler)
}

type nullHandler struct{}

func (nullHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func benchMux(b *testing.B, depth int, mux serveMux) {
	var count int
	for _, pre := range benchPrefixes {
		for _, suff := range benchSuffixes {
			for d := 1; d <= depth; d++ {
				s := make([]string, d)
				perms(d, func(idx []int) {
					for i, j := range idx {
						s[i] = benchPieces[j]
					}
					h := pre + strings.Join(s, "") + suff
					mux.Handle(h, nullHandler{})
					count++
				})
			}
		}
	}
	if b.N == 1 {
		b.Logf("Benchmarking with %d handlers", count)
	}
	var reqs []*http.Request
	for _, u := range benchRequests {
		reqs = append(reqs, request(b, "GET", u))
	}

	b.ReportAllocs()
	b.ResetTimer()

	rw := httptest.NewRecorder()
	for i := 0; i < b.N; i += len(reqs) {
		for _, req := range reqs {
			mux.ServeHTTP(rw, req)
		}
	}
}

func BenchmarkTrieServeMux1(b *testing.B) { benchMux(b, 1, NewServeMux()) }
func BenchmarkHTTPServeMux1(b *testing.B) { benchMux(b, 1, http.NewServeMux()) }
func BenchmarkTrieServeMux2(b *testing.B) { benchMux(b, 2, NewServeMux()) }
func BenchmarkHTTPServeMux2(b *testing.B) { benchMux(b, 2, http.NewServeMux()) }
func BenchmarkTrieServeMux3(b *testing.B) { benchMux(b, 3, NewServeMux()) }
func BenchmarkHTTPServeMux3(b *testing.B) { benchMux(b, 3, http.NewServeMux()) }
func BenchmarkTrieServeMux4(b *testing.B) { benchMux(b, 4, NewServeMux()) }
func BenchmarkHTTPServeMux4(b *testing.B) { benchMux(b, 4, http.NewServeMux()) }
func BenchmarkTrieServeMux5(b *testing.B) { benchMux(b, 5, NewServeMux()) }
func BenchmarkHTTPServeMux5(b *testing.B) { benchMux(b, 5, http.NewServeMux()) }

//func BenchmarkTrieServeMux6(b *testing.B) { benchMux(b, 6, NewServeMux()) }
//func BenchmarkHTTPServeMux6(b *testing.B) { benchMux(b, 6, http.NewServeMux()) }
//func BenchmarkTrieServeMux7(b *testing.B) { benchMux(b, 7, NewServeMux()) }
//func BenchmarkHTTPServeMux7(b *testing.B) { benchMux(b, 7, http.NewServeMux()) }
