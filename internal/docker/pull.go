package docker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/yuuki/droot/internal/log"
)

// WithDefaultTag returns ref with the tag "latest" if it has neither a tag
// nor a digest: pulling a name alone would pull every tag.
func WithDefaultTag(ref string) string {
	if strings.Contains(ref, "@") {
		return ref
	}
	if strings.Contains(ref[strings.LastIndex(ref, "/")+1:], ":") {
		return ref
	}
	return ref + ":latest"
}

// ImagePull pulls ref (see WithDefaultTag) for platform (optional) from its
// registry, authenticating with auth, the value of the X-Registry-Auth
// header (see RegistryAuth; empty for anonymous pulls). progress, if not
// nil, receives the status messages of the daemon.
func (c *Client) ImagePull(ctx context.Context, ref, platform, auth string, progress func(string)) error {
	ref = WithDefaultTag(ref)
	query := url.Values{"fromImage": {ref}}
	if platform != "" {
		query.Set("platform", platform)
	}
	header := http.Header{}
	if auth != "" {
		header.Set("X-Registry-Auth", auth)
		if c.cleartext {
			log.Warnf("sending registry credentials to the Docker daemon at %s without TLS", c.host)
		}
	}
	resp, err := c.doHeader(ctx, http.MethodPost, "/images/create", query, nil, header)
	if err != nil {
		return fmt.Errorf("pull %s: %w", ref, err)
	}
	defer resp.Body.Close()

	// The daemon answers 200 and streams JSON messages; errors are
	// messages too.
	dec := json.NewDecoder(resp.Body)
	for {
		var msg struct {
			Status   string `json:"status"`
			ID       string `json:"id"`
			Progress string `json:"progress"`
			Error    string `json:"error"`
			Detail   *struct {
				Message string `json:"message"`
			} `json:"errorDetail"`
		}
		err := dec.Decode(&msg)
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return ctxErr
			}
			return fmt.Errorf("pull %s: %w", ref, err)
		}
		if msg.Error != "" || msg.Detail != nil {
			text := msg.Error
			if text == "" {
				text = msg.Detail.Message
			}
			return fmt.Errorf("pull %s: %s", ref, text)
		}
		if progress != nil && msg.Status != "" {
			line := msg.Status
			if msg.ID != "" {
				line = msg.ID + ": " + line
			}
			progress(line)
		}
	}
}
