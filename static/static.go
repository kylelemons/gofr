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

package static

import (
	"bytes"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"gopkg.in/fsnotify.v0"
	"kylelemons.net/go/daemon"
)

type fileData struct {
	mime string
	data []byte
}

func serve(w http.ResponseWriter, r *http.Request, file string, get func(string) (*fileData, time.Time), put func(string, time.Time, *fileData)) {
	// Check the cache
	data, touched := get(file)
	if data != nil {
		w.Header().Set("Content-Type", data.mime)
		http.ServeContent(w, r, file, touched, bytes.NewReader(data.data))
		return
	}

	// Serve the file
	now := time.Now()
	save := saveResp{
		ResponseWriter: w,
	}
	http.ServeFile(&save, r, file)

	// Store it in the cache
	go put(file, now, &fileData{
		mime: w.Header().Get("Content-Type"),
		data: save.buf.Bytes(),
	})
}

func watch(path string, stop chan bool, taint func(file string)) {
	// Start the filesystem notifications
	watch, err := fsnotify.NewWatcher()
	if err != nil {
		daemon.Fatal.Printf("fsnotify failed: %s", err)
	}
	defer watch.Close()
	watch.Watch(path)

	for {
		select {
		case ev := <-watch.Event:
			daemon.Verbose.Printf("static(%q): event: %q", path, ev)
			taint(ev.Name)
		case err := <-watch.Error:
			daemon.Verbose.Printf("static(%q): error: %s", path, err)
			return
		case <-stop:
			daemon.Verbose.Printf("static(%q): closing", path)
			return
		}
	}
}

// A DirCache is an http.Handler for serving static files.
//
// Files within the directory are cached; if a file is changed,
// an inotify mechanism will invalidate the cache.
//
// There is no limit to how much data will be cached.
type DirCache struct {
	dir   string
	strip string

	lock  sync.RWMutex
	data  map[string]*fileData
	touch map[string]time.Time

	stop chan bool
}

// Dir returns a DirCache serving files in the given directory.
func Dir(dir string) *DirCache {
	d := &DirCache{
		dir:   dir,
		data:  make(map[string]*fileData),
		touch: make(map[string]time.Time),
		stop:  make(chan bool),
	}

	// Start the filesystem watcher
	go watch(dir, d.stop, d.taint)

	return d
}

// Strip causes all requests to strip the given prefix from the request URI.
func (d *DirCache) Strip(prefix string) *DirCache {
	d.strip = prefix
	return d
}

func (d *DirCache) taint(file string) {
	d.lock.Lock()
	defer d.lock.Unlock()

	d.touch[file] = time.Now()
	delete(d.data, file)
}

func (d *DirCache) put(file string, t time.Time, data *fileData) {
	d.lock.Lock()
	defer d.lock.Unlock()

	if len(data.data) == 0 || data.mime == "" {
		return
	}

	if !d.touch[file].Before(t) {
		daemon.Verbose.Printf("static(%q): skipping update of %q (file has been modified)", d.dir, file)
		return
	}

	d.touch[file] = t
	d.data[file] = data
	daemon.Info.Printf("static(%q): caching %q (%s)", d.dir, file, data.mime)
}

func (d *DirCache) get(file string) (*fileData, time.Time) {
	d.lock.RLock()
	defer d.lock.RUnlock()

	return d.data[file], d.touch[file]
}

// Close should be called to clean up the cached resources and stop
// the inotify watcher if this DirCache is no longer necessary.
func (d *DirCache) Close() {
	close(d.stop)
}

type saveResp struct {
	buf bytes.Buffer
	http.ResponseWriter
}

func (w *saveResp) Write(b []byte) (n int, err error) {
	w.buf.Write(b)
	return w.ResponseWriter.Write(b)
}

// ServeHTTP is part of the http.Handler interface.
func (d *DirCache) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, d.strip)
	clean := filepath.Clean(filepath.FromSlash(path))
	file := filepath.Join(d.dir, clean)

	serve(w, r, file, d.get, d.put)
}

// A FileCache is an http.Handler for serving a single static file.
//
// The file's contents will be cached; if the file is changed,
// an inotify mechanism will invalidate the cache.
//
// There is no limit to how much data will be cached.
type FileCache struct {
	file string

	lock  sync.RWMutex
	data  *fileData
	touch time.Time

	stop chan bool
}

// File returns an http.Handler for serving a single static file.
func File(file string) *FileCache {
	f := &FileCache{
		file: file,
		stop: make(chan bool),
	}

	go watch(file, f.stop, f.taint)

	return f
}

func (f *FileCache) get(string) (*fileData, time.Time) {
	f.lock.RLock()
	defer f.lock.RUnlock()

	return f.data, f.touch
}

func (f *FileCache) put(file string, t time.Time, data *fileData) {
	f.lock.Lock()
	defer f.lock.Unlock()

	if !f.touch.Before(t) {
		daemon.Verbose.Printf("static(%q): skipping update (file has been modified)", f.file)
		return
	}

	f.touch = t
	f.data = data
	daemon.Info.Printf("static(%q): caching file (%s)", f.file, data.mime)
}

func (f *FileCache) taint(file string) {
	f.lock.Lock()
	defer f.lock.Unlock()

	f.touch = time.Now()
	f.data = nil
}

func (f *FileCache) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	serve(w, r, f.file, f.get, f.put)
}
