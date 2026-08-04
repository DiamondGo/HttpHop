package server

import (
	"io"
	"net"

	"github.com/DiamondGo/HttpHop/internal/registry"
)

type countedConn struct {
	net.Conn
	tunnel *registry.ClientTunnel
}

func (c *countedConn) Close() error {
	err := c.Conn.Close()
	c.tunnel.ActiveStreams.Add(-1)
	return err
}

func OpenStream(t *registry.ClientTunnel) (net.Conn, error) {
	stream, err := t.Yamux.Open()
	if err != nil {
		return nil, err
	}
	t.ActiveStreams.Add(1)
	return &countedConn{Conn: stream, tunnel: t}, nil
}

func Bridge(a, b net.Conn) error {
	errCh := make(chan error, 2)
	go func() { _, err := copyOneWay(a, b); errCh <- err }()
	go func() { _, err := copyOneWay(b, a); errCh <- err }()
	err := <-errCh
	_ = a.Close()
	_ = b.Close()
	<-errCh
	return err
}

func copyOneWay(dst, src net.Conn) (int64, error) {
	n, err := io.Copy(dst, src)
	if c, ok := src.(interface{ CloseRead() error }); ok {
		_ = c.CloseRead()
	}
	return n, err
}
