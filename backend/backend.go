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

// Package backend provides a mechianism for registering a backend
// such that incoming requests to the frontend can be distributed
// to it.
package backend

import (
	"encoding/gob"
	"fmt"
	"io"
	"net"

	"kylelemons.net/go/daemon"
	"kylelemons.net/go/gofr/frontend"
)

// A Backend contains the information that the frontend needs to
// be given in order to connect back to this backend.
type Backend struct {
	Name string
	Host string // will be inferred if empty
	Port int
}

// DialFrontend connects to the frontend on the given net/addr.
func (b *Backend) DialFrontend(netw, addr string) error {
	conn, err := net.Dial(netw, addr)
	if err != nil {
		return err
	}

	go func() {
		<-daemon.Lamed
		conn.Close()
	}()

	return b.connect(conn)
}

func (b *Backend) connect(conn net.Conn) error {
	defer conn.Close()

	enc := gob.NewEncoder(conn)
	dec := gob.NewDecoder(conn)

	reg := frontend.RegisterBackend{
		Name: b.Name,
		Host: b.Host,
		Port: b.Port,
	}
	if err := enc.Encode(reg); err != nil {
		return fmt.Errorf("handshake failed: %s", err)
	}

	daemon.Info.Printf("Backend registered as %q with frontend %s", b.Name, conn.RemoteAddr())

	for {
		var ping frontend.Status
		if err := dec.Decode(&ping); err != nil {
			if err == io.EOF || err == io.ErrClosedPipe {
				break
			}
			return fmt.Errorf("status decode failed: %s", err)
		}
		if err := enc.Encode(ping); err != nil {
			return fmt.Errorf("status encode failed: %s", err)
		}
	}

	daemon.Info.Printf("Frontend connection closed")
	return nil
}
