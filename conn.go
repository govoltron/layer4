// Copyright 2022 Kami
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

package layer4

import (
	"github.com/panjf2000/gnet/v2"
)

type Conn interface {
	gnet.Conn
}

type ConnEventHandler interface {

	// OnOpen fires when a new connection has been opened.
	//
	// The Conn conn has information about the connection such as its local and remote addresses.
	OnOpen(conn Conn)

	// OnTraffic fires when a socket receives data from the peer.
	//
	// Note that the []byte returned from Conn.Peek(int)/Conn.Next(int) is not allowed to be passed to a new goroutine,
	// as this []byte will be reused within event-loop after OnTraffic() returns.
	// If you have to use this []byte in a new goroutine, you should either make a copy of it or call Conn.Read([]byte)
	// to read data into your own []byte, then pass the new []byte to the new goroutine.
	OnTraffic(conn Conn)

	// OnClose fires when a connection has been closed.
	//
	// The parameter err is the last known connection error.
	OnClose(conn Conn, err error)
}
