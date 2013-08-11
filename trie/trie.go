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

// Package trie implements a variant of http.ServeMux that uses a trie
// instead of a map.
package trie

import (
	"net/http"
	pathpkg "path"
	"sort"
	"strings"

	"fmt"
)

func reverse(s []string) {
	for i := 0; i < len(s)/2; i++ {
		j := len(s) - i - 1
		s[i], s[j] = s[j], s[i]
	}
}

func vaccuum(s []string) []string {
	for len(s) > 0 && s[0] == "" {
		s = s[1:]
	}
	for len(s) > 0 && s[len(s)-1] == "" {
		s = s[:len(s)-1]
	}
	return s
}

// A Trie can store a prefix tree of paths or a suffix tree of domains.
// It is the basis for the Domain and ServeMux type.
type Trie struct {
	Name  string       // path piece
	Child []*Trie      // child tries
	Leaf  http.Handler // handler for this file/dir or nil for 404
}

type byName []*Trie

func (v byName) Len() int           { return len(v) }
func (v byName) Swap(i, j int)      { v[i], v[j] = v[j], v[i] }
func (v byName) Less(i, j int) bool { return v[i].Name < v[j].Name }

// Find attempts to find the deepest matching child of this Trie with a non-nil
// Leaf and return the number of path segments required to reach it and the
// Trie present at that location.
func (t *Trie) Find(paths []string) (int, *Trie) {
	if len(paths) == 0 {
		return 0, t
	}

	search, piece := t.Child, paths[0]
	for len(search) > 0 {
		i := len(search) / 2
		cur := search[i]
		if piece == cur.Name {
			n, found := cur.Find(paths[1:])
			if found.Leaf != nil {
				return n + 1, found
			}
			break
		} else if piece < cur.Name {
			search = search[:i]
		} else {
			search = search[i+1:]
		}
	}
	return 0, t
}

// Insert inserts the given handler in the trie at the given path and returns
// an error if it could not be inserted (usually because it already existed).
func (t *Trie) Insert(paths []string, leaf http.Handler) error {
	if len(paths) == 0 {
		if t.Leaf != nil {
			return fmt.Errorf("%s: leaf already exists", t.Name)
		}
		t.Leaf = leaf
		return nil
	}

	next := paths[0]

	// for the insert case, we don't really care as much about efficiency,
	// so we won't use a binary search for now.
	var found *Trie
	for _, child := range t.Child {
		if child.Name == next {
			found = child
			break
		}
	}

	// Create the node if it wasn't found
	if found == nil {
		found = &Trie{
			Name: next,
		}
		t.Child = append(t.Child, found)
		sort.Sort(byName(t.Child))
	}

	// Insert the leaf node
	if err := found.Insert(paths[1:], leaf); err != nil {
		if t.Name == "" {
			return err
		}
		return fmt.Errorf("%s: %s", t.Name, err)
	}

	return nil
}

// Domain serves the trie for a specific domain.
type Domain struct {
	Trie
}

// NewDomain creates a new Domain with no handlers registered.
func NewDomain() *Domain {
	return &Domain{
		Trie: Trie{
			Leaf: http.HandlerFunc(http.NotFound),
		},
	}
}

// ServeHTTP finds and serves the appropriate handler for the path.
func (d *Domain) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	cleaned := pathpkg.Clean(r.URL.Path)
	if strings.HasSuffix(r.URL.Path, "/") {
		cleaned += "/"
	}

	// Redirect if the cleaned path differs
	if r.URL.Path != cleaned {
		rewrite(w, r, cleaned)
		return
	}

	// Find the best handler
	paths := vaccuum(strings.SplitAfter(r.URL.Path, "/")[1:])
	n, found := d.Find(paths)

	if n != len(paths) && !strings.HasSuffix(found.Name, "/") {
		http.NotFound(w, r)
		return
	}

	found.Leaf.ServeHTTP(w, r)
}

// ServeMux serves the tries for all configured domains.
type ServeMux struct {
	Trie
}

// Handle registers the given handler to be called on requests matching the
// given pattern.  In general, the pattern takes the following form:
//   <domain>/<path>
//
// Both the domain and the path portions are optional
func (s *ServeMux) Handle(pattern string, handler http.Handler) {
	// Split the pattern
	pieces := strings.SplitAfter(pattern, "/")
	if len(pieces) < 2 {
		panic(fmt.Sprintf("handle pattern %q is not in <domain>/<path> form", pattern))
	}

	// Break down the domain and strip empties from the ends
	pieces[0] = strings.TrimSuffix(pieces[0], "/")
	domain := vaccuum(strings.Split(pieces[0], "."))
	reverse(domain)

	// Grab the rest of the pieces as the path and strip empties
	path := vaccuum(pieces[1:])

	// Helper for inserting at the path and its index if applicable
	insert := func(t *Trie) {
		if err := t.Insert(path, handler); err != nil {
			panic(err)
		}
		if strings.HasSuffix(pattern, "/") {
			// we don't care if this already exists
			path[len(path)-1] = strings.TrimSuffix(path[len(path)-1], "/")
			_ = t.Insert(path, http.HandlerFunc(addSlash))
		}
	}

	// Find domain
	n, found := s.Find(domain)
	if n != len(domain) {
		d := NewDomain()
		insert(&d.Trie)
		s.Insert(domain, d)
		return
	}
	insert(&found.Leaf.(*Domain).Trie)
}

// NewServeMux creates a new ServeMux with no handlers registered.
func NewServeMux() *ServeMux {
	return &ServeMux{
		Trie: Trie{
			Leaf: NewDomain(),
		},
	}
}

// HandleFunc is like Handle, but it takes a function compatible with http.HandlerFunc.
func (s *ServeMux) HandleFunc(pattern string, handler func(http.ResponseWriter, *http.Request)) {
	s.Handle(pattern, http.HandlerFunc(handler))
}

// ServeHTTP finds the most appropriate domain handler and serves it.
func (s *ServeMux) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// Find the best handler
	domain := strings.Split(r.Host, ".")
	reverse(domain)
	n, found := s.Find(domain)
	if n > 0 && n != len(domain) {
		domain = domain[:n]
		reverse(domain)
		changeHost(w, r, strings.Join(domain, "."))
		return
	}
	found.Leaf.ServeHTTP(w, r)
}

// changeHost emits a redirect to the same path but with the given host.
func changeHost(w http.ResponseWriter, r *http.Request, host string) {
	u := *r.URL
	u.Host = host
	u.Scheme = "http"
	if r.TLS != nil {
		u.Scheme = "https"
	}
	http.Redirect(w, r, u.String(), http.StatusFound)
}

// addSlash emits a redirect to the same path but with a trailing slash.
func addSlash(w http.ResponseWriter, r *http.Request) {
	u := *r.URL
	u.Path += "/"
	http.Redirect(w, r, u.String(), http.StatusFound)
}

// rewrite emits a redirect to the given path with 301 Moved Permanently.
func rewrite(w http.ResponseWriter, r *http.Request, path string) {
	u := *r.URL
	u.Path = path
	http.Redirect(w, r, u.String(), http.StatusMovedPermanently)
}
