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

// Package frontend provides a mechanism by which a single Go binary
// can distribute incoming connections to a set of backends
// or serve them directly.
package frontend

import (
	"fmt"
	"io"
	"math/rand"
	"net"
	"net/http"
	urlpkg "net/url"
	"sync"
	"time"

	"kylelemons.net/go/daemon"
)

// A Backend handles routing requests to a backend.  The zero
// values of all unexported fields and of all map fields are
// safe to use.  No unexported fields may be modified after
// the Backend begins to serve traffic.
//
// The following are used to construct the backend request:
//   Method              - Unmodified
//   URL.Path            - Unmodified
//   URL.RawQuery        - Unmodified
//   Header              - Subject to whitelisting
//   Body                - Subject to size limits
//   ContentLength       - Subject to size limits
//
// The request contains the following standard headers:
//   Host                - Set to the Host from the client
//   X-Forwarded-For     - Set to the source IP of the client
//   X-Forwarded-Proto   - Set to "http" or "https"
//
// The request also contains the following nonstandard headers:
//   X-Gofr-Backend      - Set to the name of the bakend
//   X-Gofr-Backend-Root - Set to the backend's root path
//
// The response will have the following additional headers:
//   X-Frame-Options     - Set to "sameorigin"
//   X-XSS-Protection    - Set to "1; mode=block"
//
// The following headers are passed through by default:
//   Accept, Accept-Language, Content-Type
//   Authorization, Referer, User-Agent, Cookie
//   ETag, Etag, Cache-Control, If-Modified-Since
//   If-Unmodified-Since, If-Match, If-None-Match
//
// A number of standard headers are stripped by default:
//   Accept-Charset, Accept-Encoding, Accept-Datetime
//   Content-MD5, Via, Connection
//
// Any other headers will log a warning before being discarded.
type Backend struct {
	// Basic backend configuration
	Name string // name of this backend (shown in __backends)
	Root string

	// Additional limits
	AllowHeader   map[string]bool
	StripHeader   map[string]bool
	BodySizeLimit int64

	// Transport for making requests.  HandleBackend will set
	// this to http.DefaultTransport if it is nil.
	http.RoundTripper

	lock  sync.RWMutex
	hosts []*urlpkg.URL
}

// ServeHTTP proxies the request to the backend.
func (b *Backend) ServeHTTP(w http.ResponseWriter, original *http.Request) {
	start := time.Now()

	// Choose a backend
	b.lock.RLock()
	avail := len(b.hosts)
	if avail == 0 {
		daemon.Error.Printf("No backends available for %q", b.Name)
		http.Error(w, "Backend Unavailable", http.StatusServiceUnavailable)
	}
	// TODO(kevlar): consistent hash (CRC32?) user to backend
	url := *b.hosts[rand.Intn(avail)]
	b.lock.RUnlock()

	// Copy the URL
	url.Path = original.URL.Path
	url.RawQuery = original.URL.RawQuery

	// Compute for X- headers
	ip, _, _ := net.SplitHostPort(original.RemoteAddr)
	proto := "http"
	if original.TLS != nil {
		proto = "https"
	}

	// Set base headers
	headers := http.Header{
		"Host":                {original.Host},
		"X-Forwarded-For":     {ip},
		"X-Forwarded-Proto":   {proto},
		"X-Gofr-Backend":      {b.Name},
		"X-Gofr-Backend-Root": {b.Root},
	}

	// Copy headers (subject to whitelisting)
	for hdr, val := range original.Header {
		if b.StripHeader[hdr] {
			continue
		}
		if b.AllowHeader[hdr] {
			headers[hdr] = val
			continue
		}

		switch hdr {
		// Pass through
		case "Accept", "Accept-Language", "Content-Type":
			fallthrough
		case "Authorization", "Referer", "User-Agent", "Cookie":
			fallthrough
		case "ETag", "Etag", "Cache-Control":
			fallthrough
		case "If-Modified-Since", "If-Unmodified-Since", "If-Match", "If-None-Match":
			headers[hdr] = val

		// Silently ignore
		case "Accept-Charset", "Accept-Encoding", "Accept-Datetime":
			fallthrough
		case "Content-MD5":
			fallthrough
		case "Via", "Connection":
			// do nothing

		// Otherwise, log a warning
		default:
			daemon.Verbose.Printf("%s: Blocking header %q: %q", b.Name, hdr, val)
		}
	}

	// Copy the request
	req := &http.Request{
		Method:        original.Method,
		URL:           &url,
		Header:        headers,
		Body:          original.Body,
		ContentLength: original.ContentLength,
		// TODO(kevlar): Transfer encoding?
	}

	type limitCloser struct {
		io.Reader
		io.Closer
	}

	// Body size limits
	if max := b.BodySizeLimit; max > 0 {
		req.Body = limitCloser{io.LimitReader(req.Body, max), req.Body}
		if req.ContentLength > max {
			req.ContentLength = max
		}
	}

	// TODO(kevlar): prevent slow-send DoS

	// Issue the backend request
	resp, err := b.RoundTrip(req)
	if err != nil {
		daemon.Verbose.Printf("%s: routing %q to %q: backend error: %s", b.Name, original.URL, req.URL, err)

		// TODO(kevlar): Better error pages
		http.Error(w, "Backend Error", http.StatusInternalServerError)
		return
	}
	defer resp.Body.Close()

	// Set some base response headers
	w.Header().Set("X-Frame-Options", "sameorigin")
	w.Header().Set("X-XSS-Protection", "1; mode=block")

	// Copy the header
	for k, v := range resp.Header {
		w.Header()[k] = v
	}
	w.WriteHeader(resp.StatusCode)

	// Copy the response
	if n, err := io.Copy(w, resp.Body); err != nil {
		daemon.Verbose.Printf("%s: error writing response after %d bytes: %s", b.Name, n, err)
		return
	}

	daemon.Verbose.Printf("%s: Successfully routed request from %q to %q in %s", b.Name, original.URL, req.URL, time.Since(start))
}

// A ServeMux allows handlers to be registered and can distribute
// requests to them.
//
// This package has been designed in particular to work with the
// ServeMux provided by "net/http" and "kylelemons.net/go/gofr/trie".
type ServeMux interface {
	http.Handler
	Handle(pattern string, handler http.Handler)
	HandleFunc(pattern string, fn func(http.ResponseWriter, *http.Request))
}

// A Frontend manages backends and other handlers for this frontend.
// The zero Frontend is ready for use.
type Frontend struct {
	// Frontend configuration
	DebugIPs []*net.IPNet // IP networks allowed to access the debug handlers

	// Requests are handled by this ServeMux
	ServeMux

	lock     sync.RWMutex
	backends []*Backend
}

// HandleDebug registers the following handlers:
//   /__backends   - backend information (ListBackends)
func (f *Frontend) HandleDebug() {
	f.Handle("/__backends", f.Debug(http.HandlerFunc(f.ListBackends)))
}

// Debug serves 404 except for source IPs in the DebugIPs set.
func (f *Frontend) Debug(h http.Handler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		rawIP, _, err := net.SplitHostPort(r.RemoteAddr)
		if err != nil {
			http.NotFound(w, r)
			return
		}

		ip := net.ParseIP(rawIP)
		if ip == nil {
			http.NotFound(w, r)
			return
		}

		for _, net := range f.DebugIPs {
			if net.Contains(ip) {
				daemon.Verbose.Printf("Debug access to %q allowed: %s is present in %s", r.URL.Path, ip, net)
				h.ServeHTTP(w, r)
				return
			}
		}
		http.NotFound(w, r)
	}
}

// ListBackends serves a simple backend status list.
func (f *Frontend) ListBackends(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain;charset=utf-8")

	f.lock.RLock()
	defer f.lock.RUnlock()
	for _, b := range f.backends {
		fmt.Fprintf(w, "Backend %q at %q:\n", b.Name, b.Root)
		b.lock.RLock()
		for _, u := range b.hosts {
			fmt.Fprintf(w, " - %s\n", u)
		}
		b.lock.RUnlock()
	}
}

// HandleBackend registers the given backend at its specified Root.
func (f *Frontend) HandleBackend(b *Backend) {
	if b.RoundTripper == nil {
		b.RoundTripper = http.DefaultTransport
	}

	f.backends = append(f.backends, b)
	f.Handle(b.Root, b)
}

// MustCIDR is a helper function for parsing networks.
// See net.ParseCIDR for the format of the netmask.
//
// Unlike http.ParseCIDR, this does not return the IP address
// itself.
//
// This function is for use with constructing Frontend.DebugIPs
// with constant strings.
func MustCIDR(cidr string) *net.IPNet {
	_, net, err := net.ParseCIDR(cidr)
	if err != nil {
		panic(err)
	}
	return net
}

// Types for the inter-process communication between frontend and backend.
type (
	// RegisterBackend is sent by backend upon connection.
	RegisterBackend struct {
		Name string
		URL  *urlpkg.URL
	}

	// Status is sent from the frontend to the backend with a Nonce,
	// after which the Status is sent back to the frontend with the
	// same Nonce and an up-to-date Status.
	Status struct {
		Nonce  string // must match response
		Status string
	}
)

// LocalDebugIPs contains the standard "private" IPv4 and IPv6 networks.
// It can be used with Frontend.DebugIPs.
var LocalDebugIPs = []*net.IPNet{
	MustCIDR("127.0.0.0/8"),    // Localhost
	MustCIDR("::1/128"),        // Localhost
	MustCIDR("169.254.0.0/16"), // Link local
	MustCIDR("fe80::/10"),      // Link local
	MustCIDR("fc00::/7"),       // Unique local
	MustCIDR("10.0.0.0/8"),     // Private use
	MustCIDR("172.16.0.0/12"),  // Private use
	MustCIDR("192.168.0.0/16"), // Private use
}
