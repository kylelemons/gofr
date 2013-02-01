package main

import (
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	urlpkg "net/url"
	"os/user"
	pathpkg "path"
	"strconv"
	"strings"
	"syscall"
	"time"
)

var (
	httpAddr = flag.String("http", ":80", "Address on which to listen for HTTP")
	chuser   = flag.String("user", "", "User to whom to drop privileges after listening (if set)")
)

type Backend struct {
	Name string
	URL  *urlpkg.URL
}

// Route routes the original request to this backend.
//
// Route honors the following from original:
//   Method            - Copied to request
//   URL.Path          - Used to construct the backend path
//   Header            - Used as basis for backend headers (subject to whitelisting)
//   Body              - Copied to request (subject to size limits)
//   ContentLength     - Copied to request
//
// Route also provides the following headers:
//   X-Gofr-Forwarded-For       - Set to the RemoteAddr of the client
//   X-Gofr-Requested-Host      - Set to the Host from the client
//   X-Gofr-Backend             - Set to the name of the bakend the request is going to
func (b *Backend) Route(w http.ResponseWriter, original *http.Request) error {
	start := time.Now()

	// Copy the URL
	url := *b.URL
	url.Path = pathpkg.Join(url.Path, original.URL.Path)

	// Copy the headers
	headers := http.Header{
		"X-Gofr-Forwarded-For":  {original.RemoteAddr},
		"X-Gofr-Requested-Host": {original.Host},
		"X-Gofr-Backend":        {b.Name},
	}
	for hdr, val := range original.Header {
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
			// TODO(kevlar): configurable additional whitelisting

			log.Printf("%s: Blocking header %q: %q", b.Name, hdr, val)
		}
	}

	// Copy the request
	req := &http.Request{
		Method: original.Method,
		URL:    &url,
		Header: headers,

		// TODO(kevlar): LimitReader and max request size
		Body:          original.Body,
		ContentLength: original.ContentLength,

		// TODO(kevlar): Transfer encoding?
	}

	// Issue the backend request
	resp, err := http.DefaultClient.Do(req) // TODO(kevlar): custom client with custom transport that sets max idle conns
	if err != nil {
		log.Printf("%s: routing %q to %q: backend error: %s", b.Name, original.URL, req.URL, err)

		// TODO(kevlar): Better error pages
		http.Error(w, "Backend Unavailable", http.StatusServiceUnavailable)
		return nil
	}
	defer resp.Body.Close()

	// Copy the header
	for k, v := range resp.Header {
		w.Header()[k] = v
	}
	w.WriteHeader(resp.StatusCode)

	// Copy the response
	if n, err := io.Copy(w, resp.Body); err != nil {
		log.Printf("%s: error writing response after %d bytes: %s", b.Name, n, err)
		return nil
	}

	log.Printf("%s: Successfully routed request from %q to %q in %s", b.Name, original.URL, req.URL, time.Since(start))
	return nil
}

type Router interface {
	Route(w http.ResponseWriter, original *http.Request) error
}

type rewriter struct {
	Prefix, Path string
	Backend      *Backend
}

func (r *rewriter) Route(w http.ResponseWriter, original *http.Request) error {
	if !strings.HasPrefix(original.URL.Path, r.Prefix) {
		return fmt.Errorf("path %q does not have prefix %q", original.URL, r.Prefix)
	}
	defer func(path string) {
		original.URL.Path = path
	}(original.URL.Path)
	original.URL.Path = pathpkg.Join(r.Path, original.URL.Path[len(r.Prefix):])
	return r.Backend.Route(w, original)
}

type Frontend struct {
	Backends map[string]*Backend
	Routes   map[string]Router
}

func (fe *Frontend) AddBackend(name string, url string) {
	u, err := urlpkg.Parse(url)
	if err != nil {
		log.Panicf("invalid URL %q: %s", url, err)
	}
	if _, exist := fe.Backends[name]; exist {
		log.Panicf("backend %q already exists", name)
	}

	if fe.Backends == nil {
		fe.Backends = make(map[string]*Backend)
	}
	fe.Backends[name] = &Backend{
		Name: name,
		URL:  u,
	}
}

func (fe *Frontend) AddRoute(prefix string, backend, backendPath string) {
	// TODO(kevlar): don't inject a rewriter if prefix == backendPath
	// and optimize the prefix == "/" and backendPath == "/" cases>
	be, exist := fe.Backends[backend]
	if !exist {
		log.Panicf("unknown backend %q", backend)
	}
	if _, exist := fe.Routes[prefix]; exist {
		log.Panicf("duplicate route %q", prefix)
	}

	if fe.Routes == nil {
		fe.Routes = make(map[string]Router)
	}
	fe.Routes[prefix] = &rewriter{
		Prefix:  prefix,
		Backend: be,
		Path:    backendPath,
	}
}

func (fe *Frontend) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// Clean path
	path := pathpkg.Clean(r.URL.Path)
	r.URL.Path = path

	var longest string
	var route Router

	for prefix, r := range fe.Routes {
		if !strings.HasPrefix(path, prefix) {
			continue
		}
		if diff := len(prefix) - len(longest); diff > 0 || (diff == 0 && prefix < longest) {
			longest, route = prefix, r
		}
	}

	if route == nil {
		// TODO(kevlar): better error pages
		http.NotFound(w, r)
		return
	}

	if err := route.Route(w, r); err != nil {
		log.Printf("internal error: %s", err)
	}

}

func main() {
	flag.Parse()

	// DefaultMaxIdleConnsPerHost = 32

	fe := new(Frontend)
	fe.AddBackend("blog", "http://localhost:8001/")
	fe.AddRoute("/", "blog", "/")

	listener, err := net.Listen("tcp", *httpAddr)
	if err != nil {
		log.Fatalf("listen(%q): %s", *httpAddr, err)
	}

	if username := *chuser; len(username) > 0 {
		usr, err := user.Lookup(username)
		if err != nil {
			log.Fatalf("failed to find user %q: %s", username, err)
		}
		uid, err := strconv.Atoi(usr.Uid)
		if err != nil {
			log.Fatalf("bad user ID %q: %s", uid, err)
		}
		gid, err := strconv.Atoi(usr.Gid)
		if err != nil {
			log.Fatalf("bad user ID %q: %s", uid, err)
		}
		if err := syscall.Setgid(gid); err != nil {
			log.Fatalf("setgid(%d): %s", gid, err)
		}
		if err := syscall.Setuid(uid); err != nil {
			log.Fatalf("setuid(%d): %s", uid, err)
		}
	}

	log.Fatalf("serve: %s", http.Serve(listener, fe))
}
