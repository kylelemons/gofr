package trie

import (
	"net/http"
	pathpkg "path"
	"strings"
)

func splitFields(full string, s string) []string {
	full = strings.TrimPrefix(full, s)
	full = strings.TrimSuffix(full, s)
	if full == "" {
		return nil
	}
	return strings.Split(full, s)
}

type domainPiece struct {
	piece string
	child domainTrie
}

type domainTrie struct {
	child []*domainPiece
	leaf  *pathTrie
}

// find finds the given segments in the domain trie and returns
// how many were used and the leaf *pathTrie.
func (t *domainTrie) find(segs []string) (int, *pathTrie) {
	if len(segs) == 0 {
		return 0, t.leaf
	}
	seg := segs[len(segs)-1]
	for _, c := range t.child {
		if seg == c.piece {
			n, leaf := c.child.find(segs[:len(segs)-1])
			return n + 1, leaf
		}
	}
	return 0, t.leaf
}

type pathTrie struct {
	child [28]*pathTrie
	leaf  http.Handler
}

func runeIdx(r rune) int {
	if r == '/' {
		return 26
	}
	if r >= 'A' && r <= 'Z' {
		return int(r - 'A')
	}
	if r >= 'a' && r <= 'z' {
		return int(r - 'a')
	}
	return 27
}

func (t *pathTrie) find(path string) (sub int, last http.Handler) {
	s := strings.TrimPrefix(path, "/")
	add := len(path) - len(s)

	last = t.leaf
	for i, r := range s {
		// TODO(kevlar): handle directories
		next := t.child[runeIdx(r)]
		if next == nil {
			break
		}
		t = next
		if t.leaf != nil {
			sub, last = i+1, t.leaf
		}
	}
	return add + sub, last
}

type ServeMux struct {
	domain domainTrie
}

func (s *ServeMux) Handle(pattern string, handler http.Handler) {
}

func (s *ServeMux) HandleFunc(pattern string, handler func(http.ResponseWriter, *http.Request)) {
	s.Handle(pattern, http.HandlerFunc(handler))
}

func (s *ServeMux) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	domain := splitFields(r.Host, ".")
	_, path := s.domain.find(domain)
	clean := pathpkg.Clean(r.URL.Path)
	_, handler := path.find(clean)
	handler.ServeHTTP(w, r)
}
