package mysql

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"os"
)

// buildTLSConfig builds the verified-TLS configuration for TCP connections:
// the configured CA file must parse to at least one certificate, hostname
// verification is pinned to serverName (the configured host), and TLS 1.2 is
// the minimum version. InsecureSkipVerify stays false. The CA path is never
// included in returned errors.
func buildTLSConfig(caPath, serverName string) (*tls.Config, error) {
	pemBytes, err := os.ReadFile(caPath)
	if err != nil {
		return nil, errors.New("TLS CA file not readable")
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(pemBytes) {
		return nil, errors.New("TLS CA file contains no certificates")
	}
	return &tls.Config{
		RootCAs:    pool,
		ServerName: serverName,
		MinVersion: tls.VersionTLS12,
	}, nil
}
