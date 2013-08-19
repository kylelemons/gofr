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
	"encoding/gob"
	"fmt"
	"io"
	"math/rand"
	"net"
	"net/http"
	urlpkg "net/url"
	"strconv"
	"sync"
	"time"

	"kylelemons.net/go/daemon"
)

// An Endpoint handles routing requests to a backend.  The zero
// values of all unexported fields and of all map fields are
// safe to use.  No unexported fields may be modified after
// the Endpoint begins to serve traffic.
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
type Endpoint struct {
	// Basic backend configuration
	Name string // name of this backend (shown in __backends)
	Root string

	// Additional limits
	AllowHeader   map[string]bool
	StripHeader   map[string]bool
	BodySizeLimit int64

	// Transport for making requests.  HandleEndpoint will set
	// this to http.DefaultTransport if it is nil.
	http.RoundTripper

	lock  sync.RWMutex
	hosts []*urlpkg.URL
}

// ServeHTTP proxies the request to the backend.
func (b *Endpoint) ServeHTTP(w http.ResponseWriter, original *http.Request) {
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
// The zero value of all unexported fields are already for use.
type Frontend struct {
	// Frontend configuration
	DebugIPs []*net.IPNet // IP networks allowed to access the debug handlers

	// Requests are handled by this ServeMux
	ServeMux

	lock      sync.RWMutex
	endpoints []*Endpoint
}

// New returns a frontend with a standard http.ServeMux and no DebugIPs.
func New() *Frontend {
	return &Frontend{
		ServeMux: http.NewServeMux(),
	}
}

// HandleDebug registers the following handlers:
//   /__backends   - backend information (ListBackends)
func (f *Frontend) HandleDebug() {
	f.Handle("/__backends", f.Debug(http.HandlerFunc(f.ListBackends)))
}

// Debug serves 404 except for source IPs in the DebugIPs set.
func (f *Frontend) Debug(h http.Handler) http.HandlerFunc {
	blocked := func(r *http.Request, format string, args ...interface{}) {
		err := fmt.Sprintf(format, args...)
		daemon.Warning.Printf("[%s] BLOCKED debug access to %s: %s", r.RemoteAddr, r.URL.Path, err)
	}
	allowed := func(r *http.Request, format string, args ...interface{}) {
		err := fmt.Sprintf(format, args...)
		daemon.Verbose.Printf("[%s] Allowed debug access to %s: %s", r.RemoteAddr, r.URL.Path, err)
	}
	return func(w http.ResponseWriter, r *http.Request) {
		rawIP, _, err := net.SplitHostPort(r.RemoteAddr)
		if err != nil {
			blocked(r, "failed to split addr: %s", err)
			http.NotFound(w, r)
			return
		}

		ip := net.ParseIP(rawIP)
		if ip == nil {
			blocked(r, "%q could not be parsed: %s", rawIP, err)
			http.NotFound(w, r)
			return
		}

		for _, net := range f.DebugIPs {
			if net.Contains(ip) {
				allowed(r, "debug network %s", net)
				h.ServeHTTP(w, r)
				return
			}
		}

		blocked(r, "%s is not in the debug IP range", ip)
		http.NotFound(w, r)
	}
}

// ListBackends serves a simple backend status list.
func (f *Frontend) ListBackends(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain;charset=utf-8")

	f.lock.RLock()
	defer f.lock.RUnlock()
	for _, b := range f.endpoints {
		fmt.Fprintf(w, "Backend %q at %q:\n", b.Name, b.Root)
		b.lock.RLock()
		for _, u := range b.hosts {
			fmt.Fprintf(w, " - %s\n", u)
		}
		b.lock.RUnlock()
	}
}

// HandleEndpoint registers the given endpoint at its specified Root.
func (f *Frontend) HandleEndpoint(b *Endpoint) {
	if b.RoundTripper == nil {
		b.RoundTripper = http.DefaultTransport
	}

	f.endpoints = append(f.endpoints, b)
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
	// RegisterBackend is sent by backend upon connection.  The backend is assumed
	// to be served from the source port of the incoming connection.
	RegisterBackend struct {
		Name string // name of endpoint to join
		Host string // source IP assumed if empty
		Port int    // port number (required)
	}

	// Status is sent from the frontend to the backend with a Nonce,
	// after which the Status is sent back to the frontend with the
	// same Nonce and an up-to-date Status.
	Status struct {
		Nonce int64 // must match response
	}
)

// ServeBackends serves backend handling connections accepted from the given Listener.
// This function should be run in its own goroutine.
func (f *Frontend) ServeBackends(l net.Listener, pingDelay time.Duration) error {
	for {
		conn, err := l.Accept()
		if err != nil {
			return err
		}
		go func() {
			if err := f.ServeBackend(conn, pingDelay); err != nil {
				daemon.Verbose.Printf("[%s] backend connection failed: %s", conn.RemoteAddr(), err)
			}
		}()
	}
}

func (f *Frontend) addBackend(name string, url *urlpkg.URL) error {
	f.lock.Lock()
	defer f.lock.Unlock()

	for _, b := range f.endpoints {
		if b.Name == name {
			b.lock.Lock()
			defer b.lock.Unlock()
			b.hosts = append(b.hosts, url)
			daemon.Info.Printf("New %q backend: %s", name, url)
			return nil
		}
	}
	return fmt.Errorf("unknown backend %q", name)
}

func (f *Frontend) delBackend(name string, url *urlpkg.URL) {
	f.lock.Lock()
	defer f.lock.Unlock()

	for _, b := range f.endpoints {
		if b.Name == name {
			b.lock.Lock()
			defer b.lock.Unlock()
			for i, u := range b.hosts {
				if u == url { // deliberate pointer compare
					b.hosts = append(b.hosts[:i], b.hosts[i+1:]...)
					daemon.Info.Printf("Closed %q backend: %s", name, url)
					return
				}
			}
			daemon.Warning.Printf("Could not find %q backend url %q to close", name, url)
			return
		}
	}
	daemon.Warning.Printf("Could not find %q backend to close", name)
}

// Sleepish sleeps for approximately the given duration.  It will sleep
// somewhere (pseudo-randomly, normally distributed) +/- 50% of the given sleep
// time.
//
// It is a variable to facilitate instant testing; it shoulg not generally need
// to be swapped out.
var Sleepish = func(dur time.Duration) {
	const StdDev = 0.15
	const Min, Max = 0.5, 1.5

	fuzz := 1 + rand.NormFloat64()*StdDev
	if fuzz > Max {
		fuzz = Max
	} else if fuzz < Min {
		fuzz = Min
	}

	sleep(time.Duration(float64(dur) * fuzz))
}

// sleep is replaced for internal testing only.
var sleep = time.Sleep

// ServeBackend handles the given backend connection.
func (f *Frontend) ServeBackend(conn net.Conn, pingDelay time.Duration) error {
	defer conn.Close()

	enc := gob.NewEncoder(conn)
	dec := gob.NewDecoder(conn)

	// Handshake: RegisterBackend
	var reg RegisterBackend
	if err := dec.Decode(&reg); err != nil {
		return fmt.Errorf("handshake failed: %s", err)
	}

	daemon.Info.Printf("Backend %q connecting from %s", reg.Name, conn.RemoteAddr())

	if reg.Host == "" {
		// This needs to be a TCPAddr
		addr, ok := conn.RemoteAddr().(*net.TCPAddr)
		if !ok {
			got := conn.RemoteAddr()
			return fmt.Errorf("cannot infer source address from %T: %#v", got, got)
		}
		reg.Host = addr.IP.String()
	}

	url := urlpkg.URL{
		Scheme: "http", // TODO(kevlar): allow backend to request HTTPS?
		Host:   net.JoinHostPort(reg.Host, strconv.Itoa(reg.Port)),
	}

	if err := f.addBackend(reg.Name, &url); err != nil {
		return err
	}
	defer f.delBackend(reg.Name, &url)

	for {
		Sleepish(pingDelay)

		ping := &Status{
			Nonce: rand.Int63(),
		}
		start := time.Now()
		if err := enc.Encode(ping); err != nil {
			if err == io.EOF || err == io.ErrClosedPipe {
				break
			}
			return fmt.Errorf("ping failed: %s", err)
		}

		var pong Status
		if err := dec.Decode(&pong); err != nil {
			if err == io.EOF || err == io.ErrClosedPipe {
				break
			}
			return fmt.Errorf("pong decode: %s", err)
		}
		daemon.Verbose.Printf("[%s] ping time: %s", conn.RemoteAddr(), time.Since(start))

		if got, want := pong.Nonce, ping.Nonce; got != want {
			return fmt.Errorf("ping/pong mismatch: nonce = %d, want %d", got, want)
		}
	}
	return nil
}

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
