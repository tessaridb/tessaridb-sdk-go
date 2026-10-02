package tessaridb

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
)

// Trust says whom a client trusts a node by (protocol §1.1).
//
// A node given a certificate speaks TLS 1.3 on both ports and nothing else, and
// a cluster node serves clients in the clear only when its operator chose to.
// Every connection made with a Trust — the first, every one a redirect opens,
// and every HTTP request — checks the node's certificate chain against it and
// its name against the host dialled. There is no way to skip either check: a
// client that accepts any certificate is talking to whoever answered.
type Trust struct {
	roots *x509.CertPool
}

// TrustPEM trusts the certificates in a PEM file's bytes — one or several.
func TrustPEM(pem []byte) (*Trust, error) {
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(pem) {
		return nil, &TLSError{Err: errors.New("no certificate to trust")}
	}
	return &Trust{roots: roots}, nil
}

// TrustSystem trusts the operating system's own certificate store.
func TrustSystem() (*Trust, error) {
	roots, err := x509.SystemCertPool()
	if err != nil {
		return nil, &TLSError{Err: fmt.Errorf("the system's certificate store: %w", err)}
	}
	return &Trust{roots: roots}, nil
}

// TLSError is a TLS failure with the node — the handshake, its name, its chain.
//
// The transport class, kept apart from a socket error because nothing about the
// next attempt at the same node would differ: it is not retried.
type TLSError struct {
	Err error
}

func (e *TLSError) Error() string { return "tessaridb: TLS with the node failed: " + e.Err.Error() }

func (e *TLSError) Unwrap() error { return e.Err }

// config is the client side of TLS for the node at address, offering alpn.
func (t *Trust) config(address string, alpn ...string) (*tls.Config, error) {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return nil, fmt.Errorf("tessaridb: an address is host:port: %w", err)
	}
	return &tls.Config{
		RootCAs:    t.roots,
		ServerName: host,
		MinVersion: tls.VersionTLS13,
		NextProtos: alpn,
	}, nil
}

// secure completes the handshake on conn for the wire port at address.
func (t *Trust) secure(conn net.Conn, address string) (net.Conn, error) {
	config, err := t.config(address)
	if err != nil {
		return nil, err
	}
	secured := tls.Client(conn, config)
	if err := secured.Handshake(); err != nil {
		return nil, &TLSError{Err: err}
	}
	return secured, nil
}
