package config

import (
	"cmp"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"os"
	"strings"
)

// ClientTLSConfig enables server-authenticated TLS for a data connection.
type ClientTLSConfig struct {
	Enabled    bool   `mapstructure:"enabled"`
	ServerName string `mapstructure:"server_name"`
	CAFile     string `mapstructure:"ca_file"`
}

func (c ClientTLSConfig) validate() error {
	if !c.Enabled && (c.ServerName != "" || c.CAFile != "") {
		return errors.New("tls.server_name and tls.ca_file require tls.enabled")
	}
	if name := c.ServerName; name != "" && !validTLSServerName(name) {
		return errors.New("tls.server_name must be a hostname or IP address")
	}
	if c.CAFile != strings.TrimSpace(c.CAFile) {
		return errors.New("tls.ca_file must not include leading or trailing whitespace")
	}
	return nil
}

// ClientConfig loads the optional trust bundle and requires a verifiable peer name.
// A nil result means the connection was configured to use its legacy transport.
func (c ClientTLSConfig) ClientConfig(host string) (*tls.Config, error) {
	if err := c.validate(); err != nil || !c.Enabled {
		return nil, err
	}
	name := cmp.Or(c.ServerName, host)
	if !validTLSServerName(name) {
		return nil, errors.New("TLS requires a hostname or IP address")
	}
	cfg := &tls.Config{ServerName: name, MinVersion: tls.VersionTLS12}
	if c.CAFile != "" {
		pem, err := os.ReadFile(c.CAFile)
		if err != nil {
			return nil, fmt.Errorf("read TLS CA bundle: %w", err)
		}
		cfg.RootCAs = x509.NewCertPool()
		if !cfg.RootCAs.AppendCertsFromPEM(pem) {
			return nil, errors.New("TLS CA bundle contains no valid certificates")
		}
	}
	return cfg, nil
}

func validTLSServerName(name string) bool {
	if net.ParseIP(name) != nil {
		return true
	}
	return name != "" && !strings.ContainsAny(name, " \t\r\n:/\\@[]")
}
