package docker

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
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

// RegistryAuth returns the X-Registry-Auth header that lets the daemon pull
// ref, from the credentials of the docker CLI ($DOCKER_CONFIG/config.json,
// default ~/.docker): the credential helper of the registry (credHelpers),
// the credential store (credsStore) or the "auths" entries. It returns ""
// when there are no credentials for the registry.
func RegistryAuth(ctx context.Context, ref string) (string, error) {
	configDir := dockerConfigDir()
	if configDir == "" {
		return "", nil
	}
	b, err := os.ReadFile(filepath.Join(configDir, "config.json"))
	if errors.Is(err, fs.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
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
		return "", fmt.Errorf("parse %s: %w", filepath.Join(configDir, "config.json"), err)
	}

	host := registryHost(ref)
	server := host
	if host == "docker.io" {
		server = dockerHubServer
	}
	var auth *authConfig
	helper := cfg.CredsStore
	for k, h := range cfg.CredHelpers {
		if normalizeServer(k) == host {
			helper = h
		}
	}
	if helper != "" {
		if auth, err = credentialHelper(ctx, helper, server); err != nil {
			return "", err
		}
	} else {
		for k, a := range cfg.Auths {
			if normalizeServer(k) != host {
				continue
			}
			auth = &authConfig{IdentityToken: a.IdentityToken, RegistryToken: a.RegistryToken}
			if a.Auth != "" {
				dec, err := base64.StdEncoding.DecodeString(a.Auth)
				if err != nil {
					return "", fmt.Errorf("invalid credentials for %s in %s", k, filepath.Join(configDir, "config.json"))
				}
				auth.Username, auth.Password, _ = strings.Cut(string(dec), ":")
			}
			break
		}
	}
	if auth == nil || (*auth == authConfig{}) {
		log.Debugf("no credentials for %s", host)
		return "", nil
	}
	auth.ServerAddress = server
	log.Debugf("using the credentials of %s for %s", auth.Username, host)
	j, err := json.Marshal(auth)
	if err != nil {
		return "", err
	}
	return base64.URLEncoding.EncodeToString(j), nil
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
