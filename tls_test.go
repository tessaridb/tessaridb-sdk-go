package tessaridb

import (
	"crypto/tls"
	"errors"
	"os"
	"testing"
)

// TLS to a node (protocol §1.1), against a node started with a certificate:
//
//	TESSARIDB_TEST_TLS_NODE=127.0.0.1:47919 TESSARIDB_TEST_TLS_HTTP=127.0.0.1:47920 \
//	TESSARIDB_TEST_TLS_AUTHORITY=ca.pem TESSARIDB_TEST_TLS_OTHER_AUTHORITY=other.pem go test ./...

type tlsTarget struct {
	wire, http string
	trust      *Trust
	other      *Trust
}

func tlsNode(t *testing.T) tlsTarget {
	t.Helper()
	wire, http := os.Getenv("TESSARIDB_TEST_TLS_NODE"), os.Getenv("TESSARIDB_TEST_TLS_HTTP")
	authority, other := os.Getenv("TESSARIDB_TEST_TLS_AUTHORITY"), os.Getenv("TESSARIDB_TEST_TLS_OTHER_AUTHORITY")
	if wire == "" || http == "" || authority == "" || other == "" {
		t.Skip("set TESSARIDB_TEST_TLS_NODE, _HTTP, _AUTHORITY and _OTHER_AUTHORITY to run the TLS tests")
	}
	read := func(path string) *Trust {
		pem, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("reading %s: %v", path, err)
		}
		trust, err := TrustPEM(pem)
		if err != nil {
			t.Fatalf("trusting %s: %v", path, err)
		}
		return trust
	}
	return tlsTarget{wire: wire, http: http, trust: read(authority), other: read(other)}
}

func TestNothingToTrustIsRefused(t *testing.T) {
	var refused *TLSError
	if _, err := TrustPEM(nil); !errors.As(err, &refused) {
		t.Fatalf("an empty authority was accepted: %v", err)
	}
}

func TestATrustAsksForTLS13AndTheHostDialled(t *testing.T) {
	trust, err := TrustSystem()
	if err != nil {
		t.Skipf("no system store here: %v", err)
	}
	cases := []struct{ address, host string }{
		{"db.example:9080", "db.example"},
		{"[::1]:9080", "::1"},
		{"127.0.0.1:9080", "127.0.0.1"},
	}
	for _, c := range cases {
		t.Run(c.address, func(t *testing.T) {
			config, err := trust.config(c.address)
			if err != nil {
				t.Fatal(err)
			}
			if config.ServerName != c.host || config.MinVersion != tls.VersionTLS13 || config.InsecureSkipVerify {
				t.Fatalf("%+v", config)
			}
		})
	}
}

func TestAClientThatVerifiedTheNodeIsAnsweredOnTheWireAndOverHTTP(t *testing.T) {
	node := tlsNode(t)
	c, err := DialTLS(node.wire, nil, node.trust)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer c.Close()
	reply, err := c.Execute("RETURN 40 + 2;", nil)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	value, ok := reply.Outcomes[0].(ValueOutcome)
	if !ok || value.Value != (Integer{Value: 42}) {
		t.Fatalf("answered %#v", reply.Outcomes)
	}
	client, err := NewHTTPClientTLS(node.http, nil, node.trust)
	if err != nil {
		t.Fatal(err)
	}
	health, err := client.Health()
	if err != nil {
		t.Fatalf("health: %v", err)
	}
	if _, ok := health.(Healthy); !ok {
		t.Fatalf("health %#v", health)
	}
}

func TestAClientInTheClearIsNotAnswered(t *testing.T) {
	node := tlsNode(t)
	if c, err := Dial(node.wire, nil); err == nil {
		_ = c.Close()
		t.Fatal("a plaintext greeting was answered")
	}
	if _, err := NewHTTPClient(node.http, nil).Health(); err == nil {
		t.Fatal("HTTP in the clear was answered")
	}
}

func TestAClientTrustingAnotherAuthorityRefusesTheNode(t *testing.T) {
	node := tlsNode(t)
	var refused *TLSError
	if _, err := DialTLS(node.wire, nil, node.other); !errors.As(err, &refused) {
		t.Fatalf("the wire: %v", err)
	}
	client, err := NewHTTPClientTLS(node.http, nil, node.other)
	if err != nil {
		t.Fatal(err)
	}
	var unverified *tls.CertificateVerificationError
	if _, err := client.Health(); !errors.As(err, &unverified) {
		t.Fatalf("HTTP: %v", err)
	}
}
