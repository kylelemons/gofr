package main

import (
	"crypto/tls"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	urlpkg "net/url"
	"os"
	"os/signal"
	"os/user"
	pathpkg "path"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

var (
	accessFile = flag.String("access", "access.log", "Path to the access log")
	logFile    = flag.String("log", "gofr.log", "Path to the gofr log")

	httpAddr = flag.String("http", ":80", "Address on which to listen for HTTP")
	chuser   = flag.String("user", "", "User to whom to drop privileges after listening (if set)")

	httpsAddr = flag.String("https", ":443", "Address on which to listen for HTTPS")
	certFile  = flag.String("cert", "/d/ssl/kylelemons.net.cert", "File containing SSL certificate(s)")
	keyFile   = flag.String("key", "/d/ssl/kylelemons.net.key", "File containing SSL key")
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
	resp, err := http.DefaultTransport.RoundTrip(req) // TODO(kevlar): custom client with custom transport that sets max idle conns
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

type dir struct {
	Prefix, Dir string
}

func (d *dir) Route(w http.ResponseWriter, original *http.Request, stripped string) error {
	file := filepath.Join(d.Dir, filepath.FromSlash(strings.TrimPrefix(original.URL.Path, d.Prefix)))
	log.Printf("Serving %q from %q", original.URL, file)
	http.ServeFile(w, original, file)
	return nil
}

func (fe *Frontend) AddStaticDir(prefix, basedir string) {
	if _, exist := fe.Routes[prefix]; exist {
		log.Panicf("a handler for %q already exists", prefix)
	}

	if fe.Routes == nil {
		fe.Routes = make(map[string]Router)
	}
	fe.Routes[prefix] = &dir{
		Prefix: prefix,
		Dir:    basedir,
	}
}

type file struct {
	File string
}

func (f *file) Route(w http.ResponseWriter, original *http.Request, stripped string) error {
	log.Printf("Serving %q from %q", original.URL, f.File)
	http.ServeFile(w, original, f.File)
	return nil
}

func (fe *Frontend) AddStaticFile(urlpath, realpath string) {
	if _, exist := fe.Routes[urlpath]; exist {
		log.Panicf("a handler for %q already exists", urlpath)
	}

	if fe.Routes == nil {
		fe.Routes = make(map[string]Router)
	}
	fe.Routes[urlpath] = &file{
		File: realpath,
	}
}

type Frontend struct {
	Backends map[string]*Backend
	Routes   map[string]Router
}

func (fe *Frontend) AddRedirect(prefix, replace string) {
	if _, exist := fe.Routes[prefix]; exist {
		log.Panicf("a handler for %q already exists", prefix)
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
	now := time.Now().Format("[02/Jan/2006:15:04:05 -0700]")
	defer func() {
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
		log.Printf("internal error: %s", err)
	}
}

func setup() *Frontend {
	fe := new(Frontend)
	fe.AddRedirect("/", "/blog")
	fe.AddStaticFile("/robots.txt", "/d/www/static/robots.txt")
	fe.AddStaticFile("/favicon.ico", "/d/www/static/favicon.ico")
	fe.AddStaticDir("/static", "/d/www/static")
	fe.AddStaticDir("/download", "/d/www/download")
	fe.AddBackend("blog", "http://localhost:8001/")
	fe.AddBackend("vanitypkg", "http://localhost:8002/")
	fe.AddRoute("/blog", "blog", "/")
	fe.AddRoute("/go", "vanitypkg", "/")
	return fe
}

var access = log.New(os.Stderr, "", 0)

func main() {
	flag.Parse()

	logOut, err := os.OpenFile(*logFile, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0644)
	if err != nil {
		log.Fatalf("open log: %s", err)
	}
	defer logOut.Close()

	log.Printf("Logging to %s", *logFile)
	log.SetOutput(logOut)
	log.Printf("Logging started for PID %d", os.Getpid())
	defer log.Printf("Logging complete for PID %d", os.Getpid())

	accessOut, err := os.OpenFile(*accessFile, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0600)
	if err != nil {
		log.Fatalf("open access log: %s", err)
	}
	defer accessOut.Close()

	log.Printf("Writing access log to %s", *accessFile)
	access = log.New(accessOut, "", 0)

	// DefaultMaxIdleConnsPerHost = 32
	fe := setup()

	cert, err := tls.LoadX509KeyPair(*certFile, *keyFile)
	if err != nil {
		log.Fatalf("loadX509: %s", err)
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

	httpSock, err := net.Listen("tcp", *httpAddr)
	if err != nil {
		log.Fatalf("listen(%q): %s", *httpAddr, err)
	}

	httpsSock, err := tls.Listen("tcp", *httpsAddr, tlsConfig)
	if err != nil {
		log.Fatalf("listen(%q): %s", *httpsAddr, err)
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

	go func() {
		log.Printf("Listening on %s", *httpAddr)
		log.Fatalf("http: %s", http.Serve(httpSock, fe))
	}()
	go func() {
		log.Printf("Listening on %s [SSL]", *httpsAddr)
		log.Fatalf("serve: %s", http.Serve(httpsSock, fe))
	}()

	incoming := make(chan os.Signal)
	signal.Notify(incoming)
	for sig := range incoming {
		switch sig {
		case syscall.SIGTERM, syscall.SIGINT:
			return
		default:
			log.Printf("Received signal %q", sig)
		}
	}
}
