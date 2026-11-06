package docker

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/yuuki/droot/internal/log"
)

// dockerHubServer is the key of Docker Hub credentials in the docker CLI's
// configuration and credential helpers.
const dockerHubServer = "https://index.docker.io/v1/"

// credentialHelperTimeout bounds a credential helper, which may prompt or
// hang on a locked keychain.
const credentialHelperTimeout = 30 * time.Second

// registryHost returns the registry of an image reference: the first
// component if it looks like a host name (it contains "." or ":", or is
// "localhost"), Docker Hub otherwise.
func registryHost(ref string) string {
	first, _, found := strings.Cut(ref, "/")
	if found && (strings.ContainsAny(first, ".:") || first == "localhost") {
		if first == "docker.io" || first == "index.docker.io" || first == "registry-1.docker.io" {
			return "docker.io"
		}
		return first
	}
	return "docker.io"
}

// normalizeServer turns a key of "auths" (https://index.docker.io/v1/,
// https://ghcr.io, ghcr.io, ...) into a registry host as registryHost
// returns it.
func normalizeServer(key string) string {
	if i := strings.Index(key, "://"); i >= 0 {
		key = key[i+3:]
	}
	key, _, _ = strings.Cut(key, "/")
	switch key {
	case "index.docker.io", "docker.io", "registry-1.docker.io":
		return "docker.io"
	}
	return key
}

type authConfig struct {
	Username      string `json:"username,omitempty"`
	Password      string `json:"password,omitempty"`
	IdentityToken string `json:"identitytoken,omitempty"`
	RegistryToken string `json:"registrytoken,omitempty"`
	ServerAddress string `json:"serveraddress,omitempty"`
}

// Credentials are registry credentials of the docker CLI.
type Credentials struct {
	Username, Password string
	// IdentityToken is an OAuth2 refresh token, RegistryToken a bearer
	// token.
	IdentityToken, RegistryToken string
}

// RegistryAuth returns the X-Registry-Auth header that lets the daemon pull
// ref, from the credentials of the docker CLI (see RegistryCredentials). It
// returns "" when there are no credentials for the registry.
func RegistryAuth(ctx context.Context, ref string) (string, error) {
	host := registryHost(ref)
	creds, err := RegistryCredentials(ctx, host)
	if err != nil || creds == nil {
		return "", err
	}
	auth := authConfig{
		Username: creds.Username, Password: creds.Password,
		IdentityToken: creds.IdentityToken, RegistryToken: creds.RegistryToken,
		ServerAddress: host,
	}
	if host == "docker.io" {
		auth.ServerAddress = dockerHubServer
	}
	j, err := json.Marshal(auth) //nolint:gosec // G117: the credentials are for the daemon, like with the docker CLI
	if err != nil {
		return "", err
	}
	return base64.URLEncoding.EncodeToString(j), nil
}

// RegistryCredentials returns the credentials of the docker CLI
// ($DOCKER_CONFIG/config.json, default ~/.docker) for the registry host
// ("docker.io" for Docker Hub): from the credential helper of the registry
// (credHelpers), the credential store (credsStore) or the "auths" entries.
// It returns nil when there are none.
func RegistryCredentials(ctx context.Context, host string) (*Credentials, error) {
	configDir := dockerConfigDir()
	if configDir == "" {
		return nil, nil
	}
	b, err := os.ReadFile(filepath.Join(configDir, "config.json"))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var cfg struct {
		Auths map[string]struct {
			Auth          string `json:"auth"`
			IdentityToken string `json:"identitytoken"`
			RegistryToken string `json:"registrytoken"`
		} `json:"auths"`
		CredsStore  string            `json:"credsStore"`
		CredHelpers map[string]string `json:"credHelpers"`
	}
	if err := json.Unmarshal(b, &cfg); err != nil {
		return nil, fmt.Errorf("parse %s: %w", filepath.Join(configDir, "config.json"), err)
	}

	server := host
	if host == "docker.io" {
		server = dockerHubServer
	}
	var auth *authConfig
	helper := cfg.CredsStore
	for _, k := range slices.Sorted(maps.Keys(cfg.CredHelpers)) {
		if normalizeServer(k) == host {
			helper = cfg.CredHelpers[k]
			break
		}
	}
	if helper != "" {
		if auth, err = credentialHelper(ctx, helper, server); err != nil {
			return nil, err
		}
	} else {
		// Sorted, so that the choice is deterministic when several keys
		// name the same registry.
		for _, k := range slices.Sorted(maps.Keys(cfg.Auths)) {
			a := cfg.Auths[k]
			if normalizeServer(k) != host {
				continue
			}
			auth = &authConfig{IdentityToken: a.IdentityToken, RegistryToken: a.RegistryToken}
			if a.Auth != "" {
				dec, err := base64.StdEncoding.DecodeString(a.Auth)
				if err != nil {
					return nil, fmt.Errorf("invalid credentials for %s in %s", k, filepath.Join(configDir, "config.json"))
				}
				auth.Username, auth.Password, _ = strings.Cut(string(dec), ":")
			}
			break
		}
	}
	if auth == nil || (*auth == authConfig{}) {
		log.Debugf("no credentials for %s", host)
		return nil, nil
	}
	log.Debugf("using the credentials of %s for %s", auth.Username, host)
	return &Credentials{
		Username: auth.Username, Password: auth.Password,
		IdentityToken: auth.IdentityToken, RegistryToken: auth.RegistryToken,
	}, nil
}

// credentialHelper runs `docker-credential-NAME get` for server. It
// returns nil when the helper has no credentials for it.
func credentialHelper(ctx context.Context, name, server string) (*authConfig, error) {
	if strings.ContainsAny(name, "/\\") {
		return nil, fmt.Errorf("invalid credential helper name %q", name)
	}
	prog := "docker-credential-" + name
	path, err := exec.LookPath(prog)
	if err != nil {
		return nil, fmt.Errorf("credential helper %s not found in PATH", prog)
	}
	ctx, cancel := context.WithTimeout(ctx, credentialHelperTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, path, "get")
	cmd.Stdin = strings.NewReader(server)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		out := strings.TrimSpace(stdout.String() + stderr.String())
		if strings.Contains(strings.ToLower(out), "credentials not found") {
			return nil, nil
		}
		return nil, fmt.Errorf("%s get: %w: %s", prog, err, out)
	}
	var creds struct {
		Username string `json:"Username"`
		Secret   string `json:"Secret"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &creds); err != nil {
		return nil, fmt.Errorf("%s get: invalid output: %w", prog, err)
	}
	if creds.Username == "<token>" {
		return &authConfig{IdentityToken: creds.Secret}, nil
	}
	return &authConfig{Username: creds.Username, Password: creds.Secret}, nil
}
