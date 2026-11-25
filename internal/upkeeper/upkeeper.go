// Package upkeeper asks upkeeper for a patrol.
package upkeeper

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// ErrAsked is a patrol already asked for or running.
var ErrAsked = errors.New("a patrol is already on its way")

// Client is upkeeper's admin address and its patrol token.
type Client struct {
	URL   string
	Token string
	HTTP  *http.Client
}

// New returns a client for upkeeper at base.
func New(base, token string) *Client {
	return &Client{URL: strings.TrimRight(base, "/"), Token: token, HTTP: &http.Client{Timeout: 10 * time.Second}}
}

// Patrol asks for a patrol.
func (c *Client) Patrol(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.URL+"/patrol", nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusAccepted:
		return nil
	case http.StatusConflict:
		return ErrAsked
	}
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
	return fmt.Errorf("upkeeper answered %s: %s", resp.Status, strings.TrimSpace(string(b)))
}
