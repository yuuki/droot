package image

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/yuuki/droot/internal/log"
)

// reference is a parsed docker:// reference.
type reference struct {
	// host is the registry as the docker CLI names it in its
	// credentials: "docker.io" for Docker Hub.
	host   string
	repo   string
	tag    string
	digest string
}

var (
	repoComponent = `[a-z0-9]+(?:(?:[._]|__|-+)[a-z0-9]+)*`
	repoPattern   = regexp.MustCompile(`^` + repoComponent + `(?:/` + repoComponent + `)*$`)
	tagPattern    = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.-]{0,127}$`)
)

// parseReference parses [REGISTRY/]REPOSITORY[:TAG][@DIGEST], with the
// defaults of Docker: Docker Hub, library/ for official images, the tag
// latest.
func parseReference(s string) (reference, error) {
	var r reference
	name := s
	if i := strings.IndexByte(name, '@'); i >= 0 {
		name, r.digest = name[:i], name[i+1:]
		if err := validDigest(r.digest); err != nil {
			return r, err
		}
	}
	if i := strings.LastIndexByte(name, ':'); i > strings.LastIndexByte(name, '/') {
		name, r.tag = name[:i], name[i+1:]
		if !tagPattern.MatchString(r.tag) {
			return r, fmt.Errorf("invalid tag %q in %q", r.tag, s)
		}
	}
	first, rest, found := strings.Cut(name, "/")
	if found && (strings.ContainsAny(first, ".:") || first == "localhost") {
		r.host, name = first, rest
	} else {
		r.host = "docker.io"
	}
	switch r.host {
	case "index.docker.io", "registry-1.docker.io":
		r.host = "docker.io"
	}
	if r.host == "docker.io" && !strings.Contains(name, "/") {
		name = "library/" + name
	}
	if !repoPattern.MatchString(name) || len(name) > 255 {
		return r, fmt.Errorf("invalid image name %q: want [REGISTRY/]REPOSITORY[:TAG][@DIGEST], the repository in lowercase", s)
	}
	r.repo = name
	if r.tag == "" && r.digest == "" {
		r.tag = "latest"
	}
	return r, nil
}

func (r reference) String() string {
	s := r.host + "/" + r.repo
	if r.tag != "" {
		s += ":" + r.tag
	}
	if r.digest != "" {
		s += "@" + r.digest
	}
	return s
}

// apiHost is the host serving the registry API.
func (r reference) apiHost() string {
	if r.host == "docker.io" {
		return "registry-1.docker.io"
	}
	return r.host
}

// isLoopback reports whether host[:port] is this machine, where registries
// may serve plain HTTP, as Docker allows.
func isLoopback(hostport string) bool {
	host := hostport
	if h, _, err := net.SplitHostPort(hostport); err == nil {
		host = h
	}
	host = strings.Trim(host, "[]")
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

const userAgent = "droot"

// downloadConcurrency is the number of layers downloaded at the same time,
// as by Docker.
const downloadConcurrency = 3

// retryDelay is the delay before the first retry of a download, doubled
// for the next one.
var retryDelay = time.Second

var manifestAccept = strings.Join([]string{MediaTypeOCIIndex, MediaTypeOCIManifest, MediaTypeDockerList, MediaTypeDockerManifest}, ", ")

// registry is a client of the registry API (the OCI distribution
// specification) for one repository, pulling only.
type registry struct {
	ref      reference
	client   *http.Client
	getCreds func(ctx context.Context, host string) (*Credentials, error)

	mu        sync.Mutex
	scheme    string
	creds     *Credentials
	credsRead bool
	auth      string            // the Authorization header
	files     map[string]string // downloaded blobs: digest → file
}

func openRegistry(ctx context.Context, spec string, sel selector, opts Options) (*Image, error) {
	ref, err := parseReference(spec)
	if err != nil {
		return nil, err
	}
	transport := opts.Transport
	if transport == nil {
		t := http.DefaultTransport.(*http.Transport).Clone()
		t.ResponseHeaderTimeout = 2 * time.Minute
		transport = t
	}
	r := &registry{
		ref:      ref,
		client:   &http.Client{Transport: transport, CheckRedirect: checkRedirect},
		getCreds: opts.Credentials,
		scheme:   "https",
		files:    map[string]string{},
	}
	img, err := resolve(ctx, r, Descriptor{}, sel)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", ref, err)
	}
	dir, err := os.MkdirTemp(opts.TempDir, "droot-pull-")
	if err != nil {
		return nil, err
	}
	img.closers = append(img.closers, func() error { return os.RemoveAll(dir) })
	if err := r.download(ctx, img.Layers, dir); err != nil {
		_ = img.Close()
		return nil, fmt.Errorf("%s: %w", ref, err)
	}
	return img, nil
}

// checkRedirect is the redirect policy of the registry client: plain HTTP
// only from and to this machine, no credentials
// for another origin (blobs are often redirected to a CDN, which needs
// none), and no form with a refresh token sent to another origin.
func checkRedirect(req *http.Request, via []*http.Request) error {
	if len(via) >= 10 {
		return errors.New("stopped after 10 redirects")
	}
	if req.URL.Scheme != "https" && (!isLoopback(via[0].URL.Host) || !isLoopback(req.URL.Host)) {
		return fmt.Errorf("refusing a redirect from %s to plain HTTP (%s)", via[0].URL.Host, req.URL.Redacted())
	}
	if req.URL.Scheme != via[0].URL.Scheme || req.URL.Host != via[0].URL.Host {
		if req.Method != http.MethodGet {
			return fmt.Errorf("refusing to send a %s request to another origin (%s)", req.Method, req.URL.Redacted())
		}
		req.Header.Del("Authorization")
	}
	return nil
}

func (r *registry) url(p string) string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.scheme + "://" + r.ref.apiHost() + p
}

func (r *registry) authorization() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.auth
}

// fallbackToHTTP switches to plain HTTP if err says that the registry, on
// this machine, does not serve HTTPS.
func (r *registry) fallbackToHTTP(err error) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.scheme == "https" && errors.Is(err, http.ErrSchemeMismatch) && isLoopback(r.ref.apiHost()) {
		log.Debugf("%s serves plain HTTP", r.ref.apiHost())
		r.scheme = "http"
		return true
	}
	return false
}

// get sends a GET request to the registry, authenticating when the registry
// asks for it.
func (r *registry) get(ctx context.Context, p, accept string) (*http.Response, error) {
	authenticated := false
	for {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, r.url(p), nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("User-Agent", userAgent)
		if accept != "" {
			req.Header.Set("Accept", accept)
		}
		if a := r.authorization(); a != "" {
			req.Header.Set("Authorization", a)
		}
		resp, err := r.client.Do(req)
		if err != nil {
			if r.fallbackToHTTP(err) {
				continue
			}
			return nil, err
		}
		if resp.StatusCode == http.StatusUnauthorized && !authenticated {
			authenticated = true
			challenge := resp.Header.Get("WWW-Authenticate")
			_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
			_ = resp.Body.Close()
			if err := r.authenticate(ctx, challenge); err != nil {
				return nil, err
			}
			continue
		}
		return resp, nil
	}
}

// credentials returns the credentials for the registry, read once.
func (r *registry) credentials(ctx context.Context) (*Credentials, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.credsRead && r.getCreds != nil {
		c, err := r.getCreds(ctx, r.ref.host)
		if err != nil {
			return nil, fmt.Errorf("registry credentials: %w", err)
		}
		r.creds = c
	}
	r.credsRead = true
	return r.creds, nil
}

func (r *registry) setAuth(a string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.auth = a
}

// authenticate answers the challenge of a 401 response (the
// WWW-Authenticate header).
func (r *registry) authenticate(ctx context.Context, challenge string) error {
	scheme, params := parseChallenge(challenge)
	creds, err := r.credentials(ctx)
	if err != nil {
		return err
	}
	switch strings.ToLower(scheme) {
	case "basic":
		if creds == nil || creds.Username == "" {
			return fmt.Errorf("%s requires credentials: log in with `docker login %s`", r.ref.host, r.ref.host)
		}
		r.setAuth("Basic " + base64.StdEncoding.EncodeToString([]byte(creds.Username+":"+creds.Password)))
		return nil
	case "bearer":
		if creds != nil && creds.RegistryToken != "" {
			r.setAuth("Bearer " + creds.RegistryToken)
			return nil
		}
		token, err := r.fetchToken(ctx, params, creds)
		if err != nil {
			return err
		}
		r.setAuth("Bearer " + token)
		return nil
	}
	return fmt.Errorf("unsupported authentication scheme %q of %s", scheme, r.ref.apiHost())
}

// fetchToken gets a bearer token for pulling the repository from the
// authorization server named by the challenge (the token authentication of
// Docker registries).
func (r *registry) fetchToken(ctx context.Context, params map[string]string, creds *Credentials) (string, error) {
	realm, err := url.Parse(params["realm"])
	if err != nil || realm.Host == "" || (realm.Scheme != "https" && realm.Scheme != "http") {
		return "", fmt.Errorf("invalid authorization server %q of %s", params["realm"], r.ref.apiHost())
	}
	if realm.Scheme == "http" && !isLoopback(realm.Host) {
		return "", fmt.Errorf("refusing to authenticate to %s over plain HTTP", realm.Host)
	}
	scope := "repository:" + r.ref.repo + ":pull"
	var req *http.Request
	if creds != nil && creds.IdentityToken != "" {
		form := url.Values{
			"grant_type":    {"refresh_token"},
			"refresh_token": {creds.IdentityToken},
			"service":       {params["service"]},
			"scope":         {scope},
			"client_id":     {userAgent},
		}
		req, err = http.NewRequestWithContext(ctx, http.MethodPost, realm.String(), strings.NewReader(form.Encode()))
		if err != nil {
			return "", err
		}
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	} else {
		q := realm.Query()
		if s := params["service"]; s != "" {
			q.Set("service", s)
		}
		q.Set("scope", scope)
		realm.RawQuery = q.Encode()
		req, err = http.NewRequestWithContext(ctx, http.MethodGet, realm.String(), nil)
		if err != nil {
			return "", err
		}
		if creds != nil && creds.Username != "" {
			req.SetBasicAuth(creds.Username, creds.Password)
		}
	}
	req.Header.Set("User-Agent", userAgent)
	resp, err := r.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("authenticate to %s: %w", r.ref.host, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		err := responseError(resp, "authenticate to "+r.ref.host)
		if creds != nil && (resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden) {
			err = fmt.Errorf("%w (check the credentials of `docker login %s`)", err, r.ref.host)
		}
		return "", err
	}
	b, err := readAllLimited(resp.Body, 1<<20, "the token")
	if err != nil {
		return "", err
	}
	var tok struct {
		Token       string `json:"token"`
		AccessToken string `json:"access_token"`
	}
	if err := json.Unmarshal(b, &tok); err != nil {
		return "", fmt.Errorf("authenticate to %s: invalid answer: %w", r.ref.host, err)
	}
	if tok.Token == "" {
		tok.Token = tok.AccessToken
	}
	if tok.Token == "" {
		return "", fmt.Errorf("authenticate to %s: no token in the answer", r.ref.host)
	}
	return tok.Token, nil
}

// parseChallenge parses a WWW-Authenticate header: a scheme and
// comma-separated key=value or key="value" parameters.
func parseChallenge(h string) (string, map[string]string) {
	scheme, rest, _ := strings.Cut(strings.TrimSpace(h), " ")
	params := map[string]string{}
	for {
		rest = strings.TrimLeft(rest, " \t,")
		k, v, ok := strings.Cut(rest, "=")
		if !ok {
			break
		}
		k = strings.ToLower(strings.TrimSpace(k))
		v = strings.TrimLeft(v, " \t")
		if strings.HasPrefix(v, `"`) {
			var b strings.Builder
			i := 1
			for ; i < len(v) && v[i] != '"'; i++ {
				if v[i] == '\\' && i+1 < len(v) {
					i++
				}
				b.WriteByte(v[i])
			}
			params[k] = b.String()
			rest = v[min(i+1, len(v)):]
		} else {
			var val string
			val, rest, _ = strings.Cut(v, ",")
			params[k] = strings.TrimSpace(val)
		}
	}
	return scheme, params
}

// statusError is a response of the registry other than 200 OK.
type statusError struct {
	code int
	msg  string
}

func (e *statusError) Error() string { return e.msg }

// responseError describes an error response, with the errors of the body
// in the format of the distribution specification.
func responseError(resp *http.Response, what string) error {
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	var body struct {
		Errors []struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"errors"`
	}
	var details []string
	if json.Unmarshal(b, &body) == nil {
		for _, e := range body.Errors {
			details = append(details, strings.TrimSpace(e.Code+" "+e.Message))
		}
	}
	msg := what + ": " + resp.Status
	if len(details) > 0 {
		msg += ": " + strings.Join(details, "; ")
	}
	switch resp.StatusCode {
	case http.StatusUnauthorized, http.StatusForbidden:
		msg += " (log in with `docker login`, or check the name of the image)"
	case http.StatusTooManyRequests:
		msg += " (the rate limit of the registry was reached; log in with `docker login` to raise it)"
	}
	return &statusError{code: resp.StatusCode, msg: msg}
}

func (r *registry) manifest(ctx context.Context, d Descriptor) ([]byte, string, error) {
	digest := d.Digest
	if digest == "" {
		digest = r.ref.digest
	}
	name := digest
	if name == "" {
		name = r.ref.tag
	}
	if digest != "" {
		if err := validDigest(digest); err != nil {
			return nil, "", err
		}
	}
	resp, err := r.get(ctx, "/v2/"+r.ref.repo+"/manifests/"+name, manifestAccept)
	if err != nil {
		return nil, "", fmt.Errorf("get the manifest %s: %w", name, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, "", responseError(resp, "get the manifest "+name)
	}
	b, err := readAllLimited(resp.Body, maxManifestSize, "the manifest "+name)
	if err != nil {
		return nil, "", err
	}
	sum := sha256.Sum256(b)
	got := "sha256:" + hex.EncodeToString(sum[:])
	if digest != "" && got != digest {
		return nil, "", fmt.Errorf("the manifest %s has the digest %s", digest, got)
	}
	log.Debugf("manifest %s of %s: %s", name, r.ref, got)
	mt, _, _ := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	return b, mt, nil
}

func (r *registry) blob(ctx context.Context, d Descriptor) (io.ReadCloser, error) {
	if err := validDigest(d.Digest); err != nil {
		return nil, err
	}
	if d.Size <= 0 {
		return nil, fmt.Errorf("the descriptor of %s has no size", d.Digest)
	}
	r.mu.Lock()
	file, ok := r.files[d.Digest]
	r.mu.Unlock()
	if ok {
		f, err := os.Open(file)
		if err != nil {
			return nil, err
		}
		return newVerifier(f, d.Digest, "blob "+d.Digest)
	}
	resp, err := r.get(ctx, "/v2/"+r.ref.repo+"/blobs/"+d.Digest, "")
	if err != nil {
		return nil, fmt.Errorf("get the blob %s: %w", d.Digest, err)
	}
	if resp.StatusCode != http.StatusOK {
		defer resp.Body.Close()
		return nil, responseError(resp, "get the blob "+d.Digest)
	}
	return newVerifier(limitSize(resp.Body, d), d.Digest, "blob "+d.Digest)
}

// download downloads the layers into dir, a few at a time.
func (r *registry) download(ctx context.Context, layers []*Layer, dir string) error {
	var todo []Descriptor
	seen := map[string]bool{}
	var total int64
	for _, l := range layers {
		if !seen[l.Digest] {
			seen[l.Digest] = true
			todo = append(todo, Descriptor{Digest: l.Digest, Size: l.Size})
			total += l.Size
		}
	}
	log.Infof("downloading %d layers (%.1f MB) of %s", len(todo), float64(total)/1e6, r.ref)
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	sem := make(chan struct{}, downloadConcurrency)
	errs := make([]error, len(todo))
	var wg sync.WaitGroup
	for i, d := range todo {
		wg.Go(func() {
			select {
			case sem <- struct{}{}:
			case <-ctx.Done():
				errs[i] = ctx.Err()
				return
			}
			defer func() { <-sem }()
			if errs[i] = r.downloadBlob(ctx, d, dir); errs[i] != nil {
				cancel()
			}
		})
	}
	wg.Wait()
	// The first error that is not the cancellation it caused.
	for _, err := range errs {
		if err != nil && !errors.Is(err, context.Canceled) {
			return err
		}
	}
	for _, err := range errs {
		if err != nil {
			return err
		}
	}
	return nil
}

// downloadBlob downloads a blob, verifying it, retrying after transient
// errors.
func (r *registry) downloadBlob(ctx context.Context, d Descriptor, dir string) error {
	_, hexPart, _ := strings.Cut(d.Digest, ":")
	file := filepath.Join(dir, hexPart)
	for attempt := 1; ; attempt++ {
		err := r.fetch(ctx, d, file)
		if err == nil {
			r.mu.Lock()
			r.files[d.Digest] = file
			r.mu.Unlock()
			log.Debugf("downloaded %s (%d bytes)", d.Digest, d.Size)
			return nil
		}
		var se *statusError
		var de *digestError
		if attempt == 3 || ctx.Err() != nil || (errors.As(err, &se) && se.code < 500) || errors.As(err, &de) {
			return err
		}
		log.Debugf("download of %s failed (%v); retrying", d.Digest, err)
		select {
		case <-time.After(time.Duration(1<<(attempt-1)) * retryDelay):
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

func (r *registry) fetch(ctx context.Context, d Descriptor, file string) error {
	rc, err := r.blob(ctx, d)
	if err != nil {
		return err
	}
	defer rc.Close()
	f, err := os.OpenFile(file, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(f, rc); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}
