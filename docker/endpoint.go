package docker

import (
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/yuuki/droot/log"
)

// DefaultHost is the daemon address used when nothing else is configured.
const DefaultHost = "unix:///var/run/docker.sock"

// Endpoint describes how to reach a Docker daemon.
type Endpoint struct {
	// Host is a DOCKER_HOST style address such as
	// unix:///var/run/docker.sock or tcp://192.0.2.1:2376.
	Host string
	// UseTLS enables TLS for tcp:// hosts.
	UseTLS bool
	// CertDir optionally contains ca.pem, cert.pem and key.pem.
	CertDir string
	// SkipTLSVerify disables verification of the server certificate.
	SkipTLSVerify bool
	// APIVersion pins the API version (e.g. "1.47"). Empty means negotiate.
	APIVersion string
}

// EndpointFromEnv resolves the daemon endpoint the same way the docker CLI
// does:
//
//  1. DOCKER_HOST (with DOCKER_TLS_VERIFY and DOCKER_CERT_PATH), if set;
//  2. otherwise the context named by DOCKER_CONTEXT, if set;
//  3. otherwise the "currentContext" of $DOCKER_CONFIG/config.json;
//  4. otherwise unix:///var/run/docker.sock.
//
// DOCKER_API_VERSION pins the API version in every case.
func EndpointFromEnv() (Endpoint, error) {
	ep, err := endpointFromEnv()
	if err != nil {
		return ep, err
	}
	ep.APIVersion = os.Getenv("DOCKER_API_VERSION")
	return ep, nil
}

func endpointFromEnv() (Endpoint, error) {
	configDir := dockerConfigDir()

	if host := os.Getenv("DOCKER_HOST"); host != "" {
		return endpointFromHostEnv(host, configDir), nil
	}

	name := os.Getenv("DOCKER_CONTEXT")
	if name == "" {
		name = currentContext(configDir)
	}
	if name != "" && name != "default" {
		return loadContextEndpoint(configDir, name)
	}
	return endpointFromHostEnv(DefaultHost, configDir), nil
}

// endpointFromHostEnv applies DOCKER_TLS_VERIFY, DOCKER_TLS and
// DOCKER_CERT_PATH like the docker CLI: TLS is used when DOCKER_TLS_VERIFY
// is set (verifying the daemon's certificate) or DOCKER_TLS is set (without
// verification). DOCKER_CERT_PATH alone does not enable TLS.
func endpointFromHostEnv(host, configDir string) Endpoint {
	ep := Endpoint{Host: host}
	certPath := os.Getenv("DOCKER_CERT_PATH")
	verify := os.Getenv("DOCKER_TLS_VERIFY") != ""
	if verify || os.Getenv("DOCKER_TLS") != "" {
		ep.UseTLS = true
		ep.SkipTLSVerify = !verify
		ep.CertDir = certPath
		if ep.CertDir == "" {
			ep.CertDir = configDir
		}
	} else if certPath != "" && strings.HasPrefix(host, "tcp://") {
		log.Warnf("DOCKER_CERT_PATH is set but TLS is not enabled; set DOCKER_TLS_VERIFY=1 to use TLS")
	}
	return ep
}

func dockerConfigDir() string {
	if dir := os.Getenv("DOCKER_CONFIG"); dir != "" {
		return dir
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".docker")
}

func currentContext(configDir string) string {
	if configDir == "" {
		return ""
	}
	b, err := os.ReadFile(filepath.Join(configDir, "config.json"))
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			log.Debugf("cannot read docker config: %v", err)
		}
		return ""
	}
	var cfg struct {
		CurrentContext string `json:"currentContext"`
	}
	if err := json.Unmarshal(b, &cfg); err != nil {
		log.Debugf("cannot parse %s: %v", filepath.Join(configDir, "config.json"), err)
		return ""
	}
	return cfg.CurrentContext
}

// loadContextEndpoint reads the docker endpoint of a docker CLI context from
// $DOCKER_CONFIG/contexts. See https://docs.docker.com/engine/manage-resources/contexts/.
func loadContextEndpoint(configDir, name string) (Endpoint, error) {
	sum := sha256.Sum256([]byte(name))
	id := hex.EncodeToString(sum[:])
	metaPath := filepath.Join(configDir, "contexts", "meta", id, "meta.json")

	b, err := os.ReadFile(metaPath) //nolint:gosec // G703: the context name is hashed and cannot traverse paths
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return Endpoint{}, fmt.Errorf("docker context %q not found (looked for %s)", name, metaPath)
		}
		return Endpoint{}, fmt.Errorf("read docker context %q: %w", name, err)
	}
	var meta struct {
		Endpoints map[string]struct {
			Host          string `json:"Host"`
			SkipTLSVerify bool   `json:"SkipTLSVerify"`
		} `json:"Endpoints"`
	}
	if err := json.Unmarshal(b, &meta); err != nil {
		return Endpoint{}, fmt.Errorf("parse docker context %q: %w", name, err)
	}
	dockerEP, ok := meta.Endpoints["docker"]
	if !ok || dockerEP.Host == "" {
		return Endpoint{}, fmt.Errorf("docker context %q has no docker endpoint", name)
	}

	// Like the docker CLI, use TLS when the context has TLS material or
	// skips verification.
	ep := Endpoint{Host: dockerEP.Host, SkipTLSVerify: dockerEP.SkipTLSVerify, UseTLS: dockerEP.SkipTLSVerify}
	tlsDir := filepath.Join(configDir, "contexts", "tls", id, "docker")
	if fi, err := os.Stat(tlsDir); err == nil && fi.IsDir() { //nolint:gosec // G703: see above
		ep.UseTLS = true
		ep.CertDir = tlsDir
	}
	log.Debugf("using docker context %q (%s)", name, ep.Host)
	return ep, nil
}

func (ep Endpoint) tlsConfig() (*tls.Config, error) {
	cfg := &tls.Config{
		MinVersion: tls.VersionTLS12,
		// Explicitly requested by the user (DOCKER_TLS_VERIFY unset or a
		// context with SkipTLSVerify).
		InsecureSkipVerify: ep.SkipTLSVerify, //nolint:gosec // opt-in, mirrors the docker CLI
	}
	if ep.CertDir == "" {
		return cfg, nil
	}

	caFile := filepath.Join(ep.CertDir, "ca.pem")
	if pem, err := os.ReadFile(caFile); err == nil {
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(pem) {
			return nil, fmt.Errorf("no valid certificate found in %s", caFile)
		}
		cfg.RootCAs = pool
	} else if !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}

	certFile := filepath.Join(ep.CertDir, "cert.pem")
	keyFile := filepath.Join(ep.CertDir, "key.pem")
	_, certErr := os.Stat(certFile)
	_, keyErr := os.Stat(keyFile)
	if certErr == nil && keyErr == nil {
		cert, err := tls.LoadX509KeyPair(certFile, keyFile)
		if err != nil {
			return nil, fmt.Errorf("load TLS client certificate: %w", err)
		}
		cfg.Certificates = []tls.Certificate{cert}
	}
	return cfg, nil
}
