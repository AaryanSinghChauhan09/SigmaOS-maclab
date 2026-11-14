package server

// How labd listens: on this machine only, never the LAN or the tailnet.
//
//   - A Unix socket only the account labd runs as can open. cloudflared
//     dials it, so it is the tunnel's: the only place CF-Connecting-IP and
//     what the edge forwards are believed (see edge.go).
//   - A loopback port for callers on the same machine (the Builder, the
//     Out-of-band controller's client side, break-glass), which believes none
//     of that: any local process could forge it.

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

// ListenConfig is where labd serves.
type ListenConfig struct {
	// Addr is the loopback port, host:port.
	Addr string
	// Socket is the path of the tunnel's Unix socket; empty serves none.
	Socket string
}

// Check is why labd must not serve as lc says, or nil.
func (lc ListenConfig) Check() error {
	host, _, err := net.SplitHostPort(lc.Addr)
	if err != nil {
		return fmt.Errorf("--listen %q: %v", lc.Addr, err)
	}
	if ip := net.ParseIP(host); host != "localhost" && (ip == nil || !ip.IsLoopback()) {
		return fmt.Errorf("--listen %s is beyond this machine: labd listens on loopback and on the tunnel's socket (--socket), never on the LAN or the tailnet", lc.Addr)
	}
	if lc.Socket != "" && !filepath.IsAbs(lc.Socket) {
		return fmt.Errorf("--socket %q: give the socket's full path", lc.Socket)
	}
	return nil
}

// Server limits. A request's headers must arrive within headerTimeout, and an
// idle keep-alive connection closes after idleTimeout, which stays above the
// 90 s cloudflared keeps a connection in its pool. There is no overall read
// or write timeout: long polls, the event stream, the console, an exec of up
// to an hour and downloads of large artifacts all run longer.
const (
	headerTimeout = 10 * time.Second
	idleTimeout   = 2 * time.Minute
	maxHeaderSize = 64 << 10
)

// Server is an http.Server for h as lc says. It knows which requests came
// through the tunnel's socket.
func (lc ListenConfig) Server(h http.Handler) (*http.Server, error) {
	if err := lc.Check(); err != nil {
		return nil, err
	}
	return &http.Server{Handler: h, ReadHeaderTimeout: headerTimeout, IdleTimeout: idleTimeout, MaxHeaderBytes: maxHeaderSize,
		ConnContext: func(ctx context.Context, c net.Conn) context.Context {
			if _, ok := c.(*net.UnixConn); ok {
				return onTunnel(ctx)
			}
			return ctx
		}}, nil
}

// Listen opens lc's listeners: the loopback port first, then the socket if
// lc names one. The socket is made for its owner alone (0600); one left by a
// labd that died is replaced, and anything else at its path stops labd.
func (lc ListenConfig) Listen() ([]net.Listener, error) {
	if err := lc.Check(); err != nil {
		return nil, err
	}
	tcp, err := net.Listen("tcp", lc.Addr)
	if err != nil {
		return nil, err
	}
	if lc.Socket == "" {
		return []net.Listener{tcp}, nil
	}
	unix, err := listenSocket("--socket", lc.Socket)
	if err != nil {
		tcp.Close()
		return nil, err
	}
	return []net.Listener{tcp, unix}, nil
}

// listenSocket opens the Unix socket the flag named flag gives.
func listenSocket(flag, path string) (net.Listener, error) {
	switch st, err := os.Lstat(path); {
	case err == nil && st.Mode()&os.ModeSocket == 0:
		return nil, fmt.Errorf("%s %s: something that is not a socket is there", flag, path)
	case err == nil:
		if c, err := net.DialTimeout("unix", path, time.Second); err == nil {
			c.Close()
			return nil, fmt.Errorf("%s %s: another labd is serving on it", flag, path)
		}
		if err := os.Remove(path); err != nil {
			return nil, err
		}
	case !errors.Is(err, os.ErrNotExist):
		return nil, err
	}
	// The socket is born closed to everyone else: a chmod after the bind
	// would leave a moment when it is not.
	old := syscall.Umask(0o177)
	ln, err := net.Listen("unix", path)
	syscall.Umask(old)
	if err != nil {
		return nil, err
	}
	if err := os.Chmod(path, 0o600); err != nil {
		ln.Close()
		return nil, err
	}
	return ln, nil
}

// Serve serves hs on every listener and returns when one of them stops.
func Serve(hs *http.Server, lns ...net.Listener) error {
	errs := make(chan error, len(lns))
	for _, ln := range lns {
		go func() { errs <- hs.Serve(ln) }()
	}
	return <-errs
}

type tunnelKey struct{}

// onTunnel marks a connection's context as the tunnel's socket's.
func onTunnel(ctx context.Context) context.Context {
	return context.WithValue(ctx, tunnelKey{}, true)
}

// viaTunnel reports whether r came in on the tunnel's socket.
func viaTunnel(r *http.Request) bool {
	ok, _ := r.Context().Value(tunnelKey{}).(bool)
	return ok
}
