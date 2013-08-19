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

package backend

import (
	"net"
	"testing"
	"time"

	"kylelemons.net/go/daemon"
	"kylelemons.net/go/gofr/frontend"
)

func init() {
	daemon.LogLevel = daemon.Verbose
}

func TestConnect(t *testing.T) {
	feConn, beConn := net.Pipe()
	feDone, beDone := make(chan bool), make(chan bool)

	// Setup fake Sleepish
	defer func(orig func(time.Duration)) {
		frontend.Sleepish = orig
	}(frontend.Sleepish)

	var count int
	frontend.Sleepish = func(_ time.Duration) {
		count++
		if count > 10 {
			beConn.Close()
		}
	}

	// Setup Frontend
	fe := frontend.New()
	fe.HandleEndpoint(&frontend.Endpoint{
		Name: "test",
		Root: "/test",
	})
	go func() {
		defer close(feDone)
		if err := fe.ServeBackend(feConn, 30*time.Second); err != nil {
			t.Errorf("ServeBackend: %s", err)
		}
	}()

	// Setup Backend
	be := &Backend{
		Name: "test",
		Host: "fake",
		Port: 1337,
	}
	go func() {
		defer close(beDone)
		if err := be.connect(beConn); err != nil {
			t.Errorf("connect: %s", err)
		}
	}()

	<-feDone
	<-beDone
}
