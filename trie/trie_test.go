package trie

import (
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
)

type textHandler string

func (s textHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusFound)
}

func TestSplitFields(t *testing.T) {
	tests := []struct {
		full, sep string
		pieces    []string
	}{
		{"", ".", nil},
		{"example.com.", ".", []string{"example", "com"}},
	}

	for _, test := range tests {
		p := splitFields(test.full, test.sep)
		if got, want := p, test.pieces; !reflect.DeepEqual(got, want) {
			t.Errorf("splitFields(%q, %q) = %q, want %q", test.full, test.sep, got, want)
		}
	}
}

func BenchmarkSplitFields(b *testing.B) {
	const domain = "sub.example.com."
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		splitFields(domain, ".")
	}
}

var (
	root = &pathTrie{
		child: [28]*pathTrie{
			'f' - 'a': {
				child: [28]*pathTrie{
					'o' - 'a': {
						child: [28]*pathTrie{
							'o' - 'a': {
								child: [28]*pathTrie{
									26: {
										name: "/foo",
										leaf: textHandler("root /foo/"),
									},
									27: {
										name: "/foo!",
										leaf: textHandler("root /foo!"),
									},
								},
								name: "/foo",
								leaf: textHandler("root /foo"),
							},
						},
					},
				},
			},
		},
		leaf: textHandler("root /"),
	}
	com            = &pathTrie{}
	kylelemons_net = &pathTrie{
		child: [28]*pathTrie{
			'b' - 'a': {
				child: [28]*pathTrie{
					'a' - 'a': {
						child: [28]*pathTrie{
							'r' - 'a': {
								name: "/bar",
								leaf: textHandler("kylelemons.net /bar"),
							},
						},
					},
					'o' - 'a': {
						child: [28]*pathTrie{
							'o' - 'a': {
								name: "/boo",
								leaf: textHandler("kylelemons.net /boo"),
							},
						},
					},
				},
			},
		},
		leaf: textHandler("kylelemons.net /"),
	}

	TestTrie = domainTrie{
		child: []*domainPiece{{
			piece: "com",
			child: domainTrie{
				leaf: com,
			},
		}, {
			piece: "net",
			child: domainTrie{
				child: []*domainPiece{{
					piece: "kylelemons",
					child: domainTrie{
						leaf: kylelemons_net,
					},
				}},
			},
		}},
		leaf: root,
	}
	DomainFindTests = []struct {
		domain string
		count  int
		found  *pathTrie
	}{
		{
			domain: "",
			count:  0,
			found:  root,
		},
		{
			domain: "example.com.",
			count:  1,
			found:  com,
		},
		{
			domain: "kylelemons.net",
			count:  2,
			found:  kylelemons_net,
		},
		{
			domain: "sub.kylelemons.net",
			count:  2,
			found:  kylelemons_net,
		},
	}
	PathFindTests = []struct {
		base  *pathTrie
		path  string
		count int
		dir   bool
		found string
	}{
		{
			base:  root,
			path:  "/",
			count: 0,
			dir:   true,
			found: "root /",
		},
		{
			base:  root,
			path:  "/foo",
			count: 4,
			found: "root /foo",
		},
		{
			base:  root,
			path:  "/foo/",
			count: 4,
			dir:   true,
			found: "root /foo/",
		},
		{
			base:  root,
			path:  "/foo!",
			count: 5,
			found: "root /foo!",
		},
		{
			base:  root,
			path:  "/foo/bar",
			count: 4,
			dir:   true,
			found: "root /foo/",
		},
		{
			base:  kylelemons_net,
			path:  "/",
			count: 0,
			dir:   true,
			found: "kylelemons.net /",
		},
		{
			base:  kylelemons_net,
			path:  "/ba",
			count: 0,
			dir:   true,
			found: "kylelemons.net /",
		},
		{
			base:  kylelemons_net,
			path:  "/bo",
			count: 0,
			dir:   true,
			found: "kylelemons.net /",
		},
		{
			base:  kylelemons_net,
			path:  "/bar/bar",
			count: 4,
		},
		{
			base:  kylelemons_net,
			path:  "/BAR",
			count: 4,
		},
	}
)

func TestDomainFind(t *testing.T) {
	for _, test := range DomainFindTests {
		count, found := TestTrie.find(splitFields(test.domain, "."))
		if got, want := count, test.count; got != want {
			t.Errorf("find(%q).count = %v, want %v", test.domain, got, want)
		}
		if got, want := found, test.found; got != want {
			t.Errorf("find(%q).trie = %p, want %p", test.domain, got, want)
		}
	}
}

func BenchmarkDomainFind(b *testing.B) {
	var domains [][]string
	for _, test := range DomainFindTests {
		domains = append(domains, splitFields(test.domain, "."))
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i += len(domains) {
		for _, s := range domains {
			TestTrie.find(s)
		}
	}
}

func TestPathFind(t *testing.T) {
	for _, test := range PathFindTests {
		count, dir, _, found := test.base.find(test.path)
		if got, want := count, test.count; got != want {
			t.Errorf("find(%q).count = %v, want %v", test.path, got, want)
		}
		if got, want := dir, test.dir; got != want {
			t.Errorf("find(%q).dir = %v, want %v", test.path, got, want)
		}
		if got, want := found, textHandler(test.found); want != "" && got != want {
			t.Errorf("find(%q).found = %q, want %q", test.path, got, want)
		}
	}
}

func BenchmarkPathFind(b *testing.B) {
	var bases []*pathTrie
	var paths []string
	for _, test := range PathFindTests {
		bases = append(bases, test.base)
		paths = append(paths, test.path)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i += len(bases) {
		for j := range bases {
			bases[j].find(paths[j])
		}
	}
}

func makeBenchmarkData() (hosts, paths []string, reqs []*http.Request) {
	for _, d := range DomainFindTests {
		if d.found == nil {
			continue
		}
		for _, p := range PathFindTests {
			if p.found == "" || p.base != d.found {
				continue
			}

			req, err := http.NewRequest("GET", p.path, nil)
			if err != nil {
				panic(err)
			}
			req.Header.Set("Host", d.domain)
			req.Host = d.domain

			hosts = append(hosts, d.domain)
			paths = append(paths, p.path)
			reqs = append(reqs, req)
		}
	}
	return
}

func TestBenchmarkData(t *testing.T) {
	hosts, paths, reqs := makeBenchmarkData()
	mux := http.NewServeMux()
	for i := range hosts {
		pattern := hosts[i] + paths[i]
		mux.Handle(pattern, textHandler(pattern))
	}
	trie := &ServeMux{
		domain: TestTrie,
	}
	muxes := []http.Handler{
		mux, trie,
	}
	for _, req := range reqs {
		for _, h := range muxes {
			rw := httptest.NewRecorder()
			h.ServeHTTP(rw, req)
			if rw.Code != http.StatusFound {
				t.Errorf("%T %q %q: HTTP %d", h, req.Host, req.URL.Path, rw.Code)
			}
		}
	}
}

func BenchmarkHTTPMux(b *testing.B) {
	hosts, paths, reqs := makeBenchmarkData()
	mux := http.NewServeMux()
	for i := range hosts {
		pattern := hosts[i] + paths[i]
		mux.Handle(pattern, textHandler(pattern))
	}
	rw := httptest.NewRecorder()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i += len(reqs) {
		for _, req := range reqs {
			mux.ServeHTTP(rw, req)
		}
	}
}

func BenchmarkServeMux(b *testing.B) {
	_, _, reqs := makeBenchmarkData()
	mux := &ServeMux{
		domain: TestTrie,
	}
	rw := httptest.NewRecorder()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i += len(reqs) {
		for _, req := range reqs {
			mux.ServeHTTP(rw, req)
		}
	}
}
