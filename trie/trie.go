package trie

import (
	"net/http"
	pathpkg "path"
	"strings"
	"unicode/utf8"

	"fmt"
)

var _ = fmt.Print

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

	name string       // without trailing slashes
	leaf http.Handler // handler for this file/dir
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

func (t *pathTrie) find(path string) (sub int, dir bool, name string, last http.Handler) {
	s := strings.TrimPrefix(path, "/")
	add := len(path) - len(s)

	dir, last = add > 0, t.leaf
	for i, r := range s {
		next := t.child[runeIdx(r)]
		if next == nil {
			break
		}
		t = next
		if t.leaf != nil {
			sub, dir, name, last = i+add, r == '/', t.name, t.leaf
			if !dir {
				sub += utf8.RuneLen(r)
			}
		}
	}

	matched, extra := path[:sub], path[sub:]
	if matched != name {
		last = nil
	} else if len(extra) > 0 {
		if !dir || extra[0] != '/' {
			last = nil
		}
	}

	return sub, dir, name, last
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
	// TODO(kevlar): Direct trie? splitFields is the long tail
	domain := splitFields(r.Host, ".")
	_, path := s.domain.find(domain)

	clean := pathpkg.Clean(r.URL.Path)
	_, _, _, handler := path.find(clean)
	if handler == nil {
		// TODO(kevlar): User-defined error pages?
		http.NotFound(w, r)
		return
	}

	handler.ServeHTTP(w, r)
}
