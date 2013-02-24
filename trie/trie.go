package trie

import (
	"net/http"
	"strings"
)

func pieces(full string, cutset string) []string {
	return strings.FieldsFunc(full, func(r rune) bool {
		return strings.IndexRune(cutset, r) >= 0
	})
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

type pathTrie struct{}

type ServeMux struct {
}

func (s *ServeMux) Handle(pattern string, handler http.Handler) {
}

func (s *ServeMux) HandleFunc(pattern string, handler func(http.ResponseWriter, *http.Request)) {
	s.Handle(pattern, http.HandlerFunc(handler))
}

func (s *ServeMux) ServeHTTP(w http.ResponseWriter, r *http.Request) {}
