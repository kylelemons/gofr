package trie

import (
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
)

type textHandler string

func (textHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {}

func TestPieces(t *testing.T) {
	tests := []struct {
		full, sep string
		pieces    []string
	}{
		{"", ".", []string{}},
		{".", ".", []string{}},
		{"...", ".", []string{}},
		{"example.com.", ".", []string{"example", "com"}},
	}

	for _, test := range tests {
		p := pieces(test.full, test.sep)
		if got, want := p, test.pieces; !reflect.DeepEqual(got, want) {
			t.Errorf("pieces(%q, %q) = %q, want %q", test.full, test.sep, got, want)
		}
	}
}

func BenchmarkSplitPieces(b *testing.B) {
	const domain = "sub.example.com."
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		pieces(domain, ".")
	}
}

var (
	root           = &pathTrie{}
	com            = &pathTrie{}
	kylelemons_net = &pathTrie{}

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
)

var serveMux = http.NewServeMux()

func init() {
	serveMux.Handle("/", textHandler("default"))
	serveMux.Handle("example.com/", textHandler("example.com root"))
	serveMux.Handle("kylelemons.net/", textHandler("kylelemons.net root"))
}

func TestDomainFind(t *testing.T) {
	for _, test := range DomainFindTests {
		count, found := TestTrie.find(pieces(test.domain, "."))
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
		domains = append(domains, pieces(test.domain, "."))
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i += len(domains) {
		for _, s := range domains {
			TestTrie.find(s)
		}
	}
}

func BenchmarkServeMux(b *testing.B) {
	var domains []*http.Request
	for _, test := range DomainFindTests {
		req, err := http.NewRequest("GET", "http://"+test.domain, nil)
		if err != nil {
			panic(err)
		}
		domains = append(domains, req)
	}
	rw := httptest.NewRecorder()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i += len(domains) {
		for _, req := range domains {
			serveMux.ServeHTTP(rw, req)
		}
	}
}
