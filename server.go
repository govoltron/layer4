// Copyright 2023 Kami
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
	"context"
	"errors"
	"sync"
	"time"

	"github.com/panjf2000/gnet/v2"
)

var (
	ErrInvalidNetwork                          = errors.New("invalid network")
	ErrInvalidAddress                          = errors.New("invalid address")
	ErrServerHasStopped                        = errors.New("server has stopped")
	ErrInvalidConnectionEventHandlerCreateFunc = errors.New("invalid connection event handler creation function")
)

type Server struct {

	// Multicore indicates whether the engine will be effectively created with multi-cores, if so,
	// then you must take care with synchronizing memory between all event callbacks, otherwise,
	// it will run the engine with single thread. The number of threads in the engine will be automatically
	// assigned to the value of logical CPUs usable by the current process.
	Multicore bool

	// LockOSThread is used to determine whether each I/O event-loop is associated to an OS thread, it is useful when you
	// need some kind of mechanisms like thread local storage, or invoke certain C libraries (such as graphics lib: GLib)
	// that require thread-level manipulation via cgo, or want all I/O event-loops to actually run in parallel for a
	// potential higher performance.
	LockOSThread bool

	// NumEventLoop is set up to start the given number of event-loop goroutine.
	// Note: Setting up NumEventLoop will override Multicore.
	NumEventLoop int

	// ReuseAddr indicates whether to set up the SO_REUSEADDR socket option.
	ReuseAddr bool

	// ReusePort indicates whether to set up the SO_REUSEPORT socket option.
	ReusePort bool

	// SocketRecvBuffer sets the maximum socket receive buffer in bytes.
	SocketRecvBuffer int

	// SocketSendBuffer sets the maximum socket send buffer in bytes.
	SocketSendBuffer int

	// TCPKeepAlive sets up a duration for (SO_KEEPALIVE) socket option.
	TCPKeepAlive time.Duration

	// OnBoot fires when the server is ready for accepting connections.
	OnBoot func()

	// OnShutdown fires when the server is being shut down, it is called right after
	// all event-loops and connections are closed.
	OnShutdown func()

	// OnConnect fires when a new connection has been opened.
	//
	// The Conn conn has information about the connection such as its local and remote addresses.
	OnConnect func(conn Conn)

	// OnDisconnect fires when a connection has been closed.
	//
	// The parameter err is the last known connection error.
	OnDisconnect func(conn Conn, err error)

	// OnNewConnection fires when a new connection has been opened.
	OnNewConnection func() (handler ConnEventHandler)

	numConnectionsFunc func() (num int)
	dupFunc            func() (dupFD int, err error)
	shutdownFunc       func()
	stopFunc           func(ctx context.Context) (err error)
}

func (s *Server) Run(network, addr string) error {
	return s.RunContext(context.Background(), network, addr)
}

func (s *Server) RunContext(ctx context.Context, network, addr string) error {
	if network == "" {
		panic(ErrInvalidNetwork)
	}
	if addr == "" {
		panic(ErrInvalidAddress)
	}
	if s.OnNewConnection == nil {
		panic(ErrInvalidConnectionEventHandlerCreateFunc)
	}

	var (
		opts       gnet.Options
		cancelFunc func()
	)

	ctx, cancelFunc = context.WithCancel(ctx)

	// Required
	opts.Ticker = true

	// Optional
	if s.Multicore {
		opts.Multicore = s.Multicore
	}
	if s.TCPKeepAlive > 0 {
		opts.TCPKeepAlive = s.TCPKeepAlive
	}
	if s.LockOSThread {
		opts.LockOSThread = s.LockOSThread
	}
	if s.ReuseAddr {
		opts.ReuseAddr = s.ReuseAddr
	}
	if s.ReusePort {
		opts.ReusePort = s.ReusePort
	}
	if s.NumEventLoop > 0 {
		opts.NumEventLoop = s.NumEventLoop
	}
	if s.SocketRecvBuffer > 0 {
		opts.SocketRecvBuffer = s.SocketRecvBuffer
	}
	if s.SocketSendBuffer > 0 {
		opts.SocketSendBuffer = s.SocketSendBuffer
	}

	e := &event{
		ctx:        ctx,
		boot:       s.OnBoot,
		shutdown:   s.OnShutdown,
		connect:    s.OnConnect,
		disconnect: s.OnDisconnect,
	}
	e.pool.New = func() interface{} { return s.OnNewConnection() }

	s.numConnectionsFunc = func() int { return e.engine.CountConnections() }
	s.dupFunc = func() (int, error) { return e.engine.Dup() }
	s.shutdownFunc = func() { cancelFunc() }
	s.stopFunc = func(ctx context.Context) error { return e.engine.Stop(ctx) }

	defer func() {
		s.numConnectionsFunc = nil
		s.dupFunc = nil
		s.shutdownFunc = nil
		s.stopFunc = nil
	}()

	return gnet.Run(e, network+"://"+addr, gnet.WithOptions(opts))
}

// NumConnections counts the number of currently active connections and returns it.
func (s *Server) NumConnections() (num int) {
	if s.numConnectionsFunc != nil {
		num = s.numConnectionsFunc()
	}
	return
}

// Dup returns a copy of the underlying file descriptor of listener.
// It is the caller's responsibility to close dupFD when finished.
// Closing listener does not affect dupFD, and closing dupFD does not affect listener.
func (s *Server) Dup() (dupFD int, err error) {
	if s.dupFunc != nil {
		dupFD, err = s.dupFunc()
	} else {
		err = ErrServerHasStopped
	}
	return
}

// Shutdown shutdowns the server.
func (s *Server) Shutdown() {
	if s.shutdownFunc != nil {
		s.shutdownFunc()
	}
}

// Stop gracefully shuts down this Engine without interrupting any active event-loops,
// it waits indefinitely for connections and event-loops to be closed and then shuts down.
func (s *Server) Stop(ctx context.Context) (err error) {
	if s.stopFunc != nil {
		err = s.stopFunc(ctx)
	}
	return
}

type event struct {
	ctx        context.Context
	pool       sync.Pool
	engine     gnet.Engine
	boot       func()
	shutdown   func()
	connect    func(Conn)
	disconnect func(Conn, error)
}

func (e *event) OnBoot(engine gnet.Engine) (action gnet.Action) {
	e.engine = engine

	// Trigger event
	if e.boot != nil {
		e.boot()
	}

	// If we need to shutdown the engine.
	e.handleShutdown(func() {
		action = gnet.Shutdown
		// When the engine is shut down during the startup phase,
		// no event is fired, so we need do something.
		e.OnShutdown(engine)
	})
	return
}

func (e *event) OnShutdown(eng gnet.Engine) {
	// Trigger event
	if e.shutdown != nil {
		e.shutdown()
	}
}

func (e *event) OnOpen(conn gnet.Conn) (out []byte, action gnet.Action) {
	handler := e.bindConnEventHandler(conn)

	// Trigger event
	if e.connect != nil {
		e.connect(conn)
	}
	handler.OnOpen(conn)

	// If we need to shutdown the engine.
	e.handleShutdown(func() {
		action = gnet.Shutdown
	})
	return
}

func (e *event) OnTraffic(conn gnet.Conn) (action gnet.Action) {
	// Trigger event
	handler, ok := e.getConnEventHandler(conn)
	if !ok {
		handler = e.bindConnEventHandler(conn)
		defer e.unbindConnEventHandler(conn)
	}

	handler.OnTraffic(conn)

	// If we need to shutdown the engine.
	e.handleShutdown(func() {
		action = gnet.Shutdown
	})
	return
}

func (e *event) OnClose(conn gnet.Conn, err error) (action gnet.Action) {
	handler, ok := e.getConnEventHandler(conn)
	if !ok {
		panic("unexpected event")
	}

	// Trigger event
	handler.OnClose(conn, err)
	if e.disconnect != nil {
		e.disconnect(conn, err)
	}

	e.unbindConnEventHandler(conn)

	// If we need to shutdown the engine.
	e.handleShutdown(func() {
		action = gnet.Shutdown
	})
	return
}

func (e *event) OnTick() (delay time.Duration, action gnet.Action) {
	delay = time.Millisecond

	// If we need to shutdown the engine.
	e.handleShutdown(func() {
		action = gnet.Shutdown
	})
	return
}

func (e *event) bindConnEventHandler(conn gnet.Conn) (handler ConnEventHandler) {
	handler = e.pool.Get().(ConnEventHandler)

	// Bind Conn and gnet.Conn
	conn.SetContext(handler)

	return
}

func (e *event) getConnEventHandler(conn gnet.Conn) (handler ConnEventHandler, ok bool) {
	if ctx := conn.Context(); ctx != nil {
		handler, ok = ctx.(ConnEventHandler), true
	}
	return
}

func (e *event) unbindConnEventHandler(conn gnet.Conn) {
	handler := conn.Context().(ConnEventHandler)

	// Unbind
	conn.SetContext(nil)

	e.pool.Put(handler)
}

func (e *event) handleShutdown(shutdown func()) {
	select {
	case <-e.ctx.Done():
		shutdown()
	default:
		break
	}
}
