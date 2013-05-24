package main

import (
	"crypto/tls"
	"flag"
	"fmt"
	"io"
	logpkg "log"
	"net/http"
	urlpkg "net/url"
	"os"
	pathpkg "path"
	"strings"
	"time"

	"kylelemons.net/go/daemon"
	"kylelemons.net/go/gofr/static"
)

var (
	lameDuck = flag.Duration("lame-duck", 5*time.Second, "Amount of time to wait for lingering connections to close")

	accessFile = flag.String("access", "access.log", "Path to the access log")

	certFile = flag.String("cert", "/d/ssl/kylelemons.net.cert", "File containing SSL certificate(s)")
	keyFile  = flag.String("key", "/d/ssl/kylelemons.net.key", "File containing SSL key")

	logFile = daemon.LogFileFlag("log", 0644)
	web     = daemon.ListenFlag("http", "tcp", ":80", "HTTP")
	ssl     = daemon.ListenFlag("https", "tcp", ":443", "HTTPS")
	privs   = daemon.PrivilegesFlag("user", "")
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
//   URL.RawQuery      - Used to construct the backend path
//   Header            - Used as basis for backend headers (subject to whitelisting)
//   Body              - Copied to request (subject to size limits)
//   ContentLength     - Copied to request
//
// Route also provides the following headers:
//   X-Gofr-Forwarded-For       - Set to the RemoteAddr of the client
//   X-Gofr-Requested-Host      - Set to the Host from the client
//   X-Gofr-Backend             - Set to the name of the bakend the request is going to
//   X-Gofr-Stripped-Prefix     - Set to the directory corresponding to /
func (b *Backend) Route(w http.ResponseWriter, original *http.Request, stripped string) error {
	start := time.Now()

	// Copy the URL
	url := *b.URL
	url.Path = pathpkg.Join(url.Path, original.URL.Path)
	url.RawQuery = original.URL.RawQuery

	// Copy the headers
	headers := http.Header{
		"X-Gofr-Forwarded-For":   {original.RemoteAddr},
		"X-Gofr-Requested-Host":  {original.Host},
		"X-Gofr-Backend":         {b.Name},
		"X-Gofr-Stripped-Prefix": {stripped},
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

			daemon.Verbose.Printf("%s: Blocking header %q: %q", b.Name, hdr, val)
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
	resp, err := http.DefaultTransport.RoundTrip(req) // TODO(kevlar): custom client with custom transport that sets max idle conns
	if err != nil {
		daemon.Verbose.Printf("%s: routing %q to %q: backend error: %s", b.Name, original.URL, req.URL, err)

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
		daemon.Verbose.Printf("%s: error writing response after %d bytes: %s", b.Name, n, err)
		return nil
	}

	daemon.Verbose.Printf("%s: Successfully routed request from %q to %q in %s", b.Name, original.URL, req.URL, time.Since(start))
	return nil
}

type Router interface {
	Route(w http.ResponseWriter, original *http.Request, stripped string) error
}

type rewriter struct {
	Prefix, Path string
	Backend      *Backend
}

func (r *rewriter) Route(w http.ResponseWriter, original *http.Request, stripped string) error {
	if !strings.HasPrefix(original.URL.Path, r.Prefix) {
		return fmt.Errorf("path %q does not have prefix %q", original.URL, r.Prefix)
	}
	defer func(path string) {
		original.URL.Path = path
	}(original.URL.Path)
	original.URL.Path = pathpkg.Join(r.Path, original.URL.Path[len(r.Prefix):])
	return r.Backend.Route(w, original, pathpkg.Clean(stripped+r.Prefix))
}

type redirector struct {
	Strip, Replace string
}

func (r *redirector) Route(w http.ResponseWriter, original *http.Request, stripped string) error {
	loc := pathpkg.Join(r.Replace, strings.TrimPrefix(original.URL.Path, r.Strip))
	w.Header().Set("Location", loc)
	w.WriteHeader(http.StatusTemporaryRedirect)
	fmt.Fprintf(w, "<html><body>click <a href=%q>here</a> if your browser doesn't redirect you</a></body></html", loc)
	return nil
}

type handler struct {
	Prefix string
	http.Handler
}

func (h *handler) Route(w http.ResponseWriter, r *http.Request, stripped string) error {
	h.ServeHTTP(w, r)
	return nil
}

type Frontend struct {
	Backends map[string]*Backend
	Routes   map[string]Router
}

func (fe *Frontend) Handle(prefix string, h http.Handler) {
	if _, exist := fe.Routes[prefix]; exist {
		daemon.Fatal.Printf("a handler for %q already exists", prefix)
	}

	if fe.Routes == nil {
		fe.Routes = make(map[string]Router)
	}
	fe.Routes[prefix] = &handler{
		Handler: h,
	}
}

func (fe *Frontend) AddRedirect(prefix, replace string) {
	if _, exist := fe.Routes[prefix]; exist {
		daemon.Fatal.Printf("a handler for %q already exists", prefix)
	}

	if fe.Routes == nil {
		fe.Routes = make(map[string]Router)
	}
	fe.Routes[prefix] = &redirector{
		Strip:   prefix,
		Replace: replace,
	}
}

func (fe *Frontend) AddBackend(name string, url string) {
	u, err := urlpkg.Parse(url)
	if err != nil {
		daemon.Fatal.Printf("invalid URL %q: %s", url, err)
	}
	if _, exist := fe.Backends[name]; exist {
		daemon.Fatal.Printf("backend %q already exists", name)
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
		daemon.Fatal.Printf("unknown backend %q", backend)
	}
	if _, exist := fe.Routes[prefix]; exist {
		daemon.Fatal.Printf("duplicate route %q", prefix)
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

type rwlogger struct {
	code  int
	bytes int
	http.ResponseWriter
}

func (w *rwlogger) WriteHeader(code int) {
	w.code = code
	w.ResponseWriter.WriteHeader(code)
}

func (w *rwlogger) Write(b []byte) (int, error) {
	n, err := w.ResponseWriter.Write(b)
	w.bytes += n
	return n, err
}

func (fe *Frontend) ServeHTTP(rw http.ResponseWriter, r *http.Request) {
	w := &rwlogger{200, 0, rw}
	start := time.Now()
	defer func() {
		now := start.Format("[02/Jan/2006:15:04:05 -0700]")

		// access log format: "%h %l %u %t \"%r\" %>s %b"
		addr := r.RemoteAddr
		if colon := strings.Index(addr, ":"); colon >= 0 {
			addr = addr[:colon]
		}
		user := "-"
		if r.URL.User != nil {
			user = r.URL.User.Username()
		}
		firstLine := fmt.Sprintf("%s %s %s", r.Method, r.URL, r.Proto)
		bytes := "-"
		if w.bytes > 0 {
			bytes = fmt.Sprintf("%d", w.bytes)
		}
		full := r.URL.Path
		if r.Host != "" {
			u := *r.URL
			u.Host = r.Host
			u.Scheme = "http"
			if r.TLS != nil {
				u.Scheme = "https"
			}
			full = u.String()
		}
		useragent := r.Header.Get("User-Agent")
		access.Printf("%s - %s %s %q %d %s %q %q", addr, user, now, firstLine, w.code, bytes, full, useragent)
	}()

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

	if err := route.Route(w, r, ""); err != nil {
		daemon.Error.Printf("internal error: %s", err)
	}
}

func setup() *Frontend {
	fe := new(Frontend)
	fe.AddRedirect("/", "/blog")
	fe.Handle("/robots.txt", static.File("/d/www/static/robots.txt"))
	fe.Handle("/favicon.ico", static.File("/d/www/static/favicon.ico"))
	fe.Handle("/static", static.Dir("/d/www/static").Strip("/static"))
	fe.Handle("/download", static.Dir("/d/www/download").Strip("/download"))
	fe.AddBackend("blog", "http://localhost:8001/")
	fe.AddBackend("vanitypkg", "http://localhost:8002/")
	fe.AddBackend("gitweb", "http://localhost:8003/")
	fe.AddRoute("/blog", "blog", "/")
	fe.AddRoute("/go", "vanitypkg", "/")
	fe.AddRoute("/browse", "gitweb", "/")
	return fe
}

var access = logpkg.New(os.Stderr, "", 0)

func main() {
	flag.Parse()

	daemon.LogLevel = daemon.Verbose
	daemon.LameDuck = *lameDuck

	accessOut, err := os.OpenFile(*accessFile, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0600)
	if err != nil {
		daemon.Fatal.Printf("open access log: %s", err)
	}
	defer accessOut.Close()

	daemon.Info.Printf("Writing access log to %s", *accessFile)
	access = logpkg.New(accessOut, "", 0)

	// DefaultMaxIdleConnsPerHost = 32
	fe := setup()

	cert, err := tls.LoadX509KeyPair(*certFile, *keyFile)
	if err != nil {
		daemon.Fatal.Printf("loadX509: %s", err)
	}
	tlsConfig := &tls.Config{
		Certificates: []tls.Certificate{cert},
		CipherSuites: []uint16{
			tls.TLS_RSA_WITH_RC4_128_SHA,
			tls.TLS_ECDHE_RSA_WITH_RC4_128_SHA,
			tls.TLS_RSA_WITH_AES_128_CBC_SHA,
			tls.TLS_ECDHE_RSA_WITH_AES_128_CBC_SHA,
			tls.TLS_RSA_WITH_3DES_EDE_CBC_SHA,
			tls.TLS_ECDHE_RSA_WITH_3DES_EDE_CBC_SHA,
		},
		PreferServerCipherSuites: true,
	}

	httpSock, err := web.Listen()
	if err != nil {
		daemon.Fatal.Printf("listen(%q): %s", web, err)
	}

	httpsRawSock, err := ssl.Listen()
	if err != nil {
		daemon.Fatal.Printf("listen(%q): %s", ssl, err)
	}
	httpsSock := tls.NewListener(httpsRawSock, tlsConfig)

	// Drop privileges
	privs.Drop()

	go func() {
		if err := http.Serve(httpSock, fe); err != nil && err != daemon.ErrStopped {
			daemon.Fatal.Printf("http: %s", err)
		}
	}()
	go func() {
		if err := http.Serve(httpsSock, fe); err != nil && err != daemon.ErrStopped {
			daemon.Fatal.Printf("https: %s", err)
		}
	}()

	daemon.Run()
}
