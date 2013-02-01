package main

import (
	"bufio"
	"flag"
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
)

var (
	httpAddr = flag.String("http", ":80", "Address on which to listen for HTTP")
	chuser   = flag.String("user", "", "User to whom to drop privileges after listening (if set)")
)

type BackendPath struct {
	Name string
	Path string
}
type Mapping struct {
	Path   string
	Target *BackendPath
	// Options
}
type Config struct {
	Mappings []*Mapping
}

func ParseConfig(r io.Reader) (*Config, error) {
	var (
		lines = bufio.NewReader(r)
		cfg   = &Config{}
		last  *Mapping
	)

	// line is nonzero in length and has been stripped of spaces and comments
	process := func(lineno int, line string) {
		fields := strings.Fields(line)
		nField := len(fields)
		switch {
		case nField == 3 && fields[1] == "->":
			backend := strings.Split(fields[2], ":")
			if len(backend) != 2 {
				log.Fatalf("config:%d: malformed backend %q: want name:/path", lineno, fields[2])
			}

			last = &Mapping{
				Path: fields[0],
				Target: &BackendPath{
					Name: backend[0],
					Path: backend[1],
				},
			}
			cfg.Mappings = append(cfg.Mappings, last)
		}
	}

	for lineno := 0; ; lineno++ {
		line, err := lines.ReadString('\n')
		if idx := strings.Index(line, "#"); idx >= 0 {
			line = line[:idx]
		}
		line = strings.TrimSpace(line)
		if len(line) > 0 {
			process(lineno, line)
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
	}
	return cfg, nil
}

func main() {
	flag.Parse()

	var raw = strings.NewReader(`
/ -> blog:/
	`)

	config, err := ParseConfig(raw)
	if err != nil {
		log.Fatalf("parse config: %s", err)
	}

	backends := map[string]*urlpkg.URL{
		"blog": {
			Scheme: "http",
			Host:   "localhost:8001",
			Path:   "/",
		},
	}

	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		path := pathpkg.Clean(r.URL.Path)
		for _, m := range config.Mappings {
			if !strings.HasPrefix(path, m.Path) {
				continue
			}
			be, ok := backends[m.Target.Name]
			if !ok {
				log.Printf("misconfigured mapping: backend %q does not exist", m.Target.Name)
				http.NotFound(w, r)
				return
			}
			// Copy the URL
			url := *be
			url.Path = pathpkg.Join(m.Target.Path, path[len(m.Path):])

			// Copy the request
			req := *r
			req.URL = &url
			req.RequestURI = ""

			// Issue the backend request
			resp, err := http.DefaultClient.Do(&req)
			if err != nil {
				log.Printf("routing %q to %q: backend error: %s", path, req.URL, err)
				http.Error(w, "backend error", http.StatusInternalServerError)
				return
			}
			defer resp.Body.Close()

			// Copy the header
			for k, v := range resp.Header {
				w.Header()[k] = v
			}
			w.WriteHeader(resp.StatusCode)

			// Copy the response
			if n, err := io.Copy(w, resp.Body); err != nil {
				log.Printf("error writing response after %d bytes: %s", n, err)
				return
			}

			log.Printf("Successfully routed request from %q to %q", path, req.URL)
			return
		}
		http.NotFound(w, r)
	})

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

	log.Fatalf("serve: %s", http.Serve(listener, nil))
}
