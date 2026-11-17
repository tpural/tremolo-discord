// Package tremolo is the part of tremolo's API the bot uses. Its token is a
// person's (not an assistant's), so approvals and notes count as theirs.
package tremolo

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Client talks to tremolo.
type Client struct {
	URL   string
	Token string
	HTTP  *http.Client
}

// New returns a client for tremolo at base.
func New(base, token string) *Client {
	return &Client{URL: strings.TrimRight(base, "/"), Token: token, HTTP: &http.Client{Timeout: 30 * time.Second}}
}

// Error is tremolo answering with an error.
type Error struct {
	Code int
	Msg  string
}

func (e *Error) Error() string { return e.Msg }

func (c *Client) do(ctx context.Context, method, path string, q url.Values, body, out any) error {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rd = bytes.NewReader(b)
	}
	u := c.URL + path
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, method, u, rd)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode >= 300 {
		var e struct {
			Error string `json:"error"`
		}
		msg := strings.TrimSpace(string(b))
		if json.Unmarshal(b, &e) == nil && e.Error != "" {
			msg = e.Error
		}
		return &Error{resp.StatusCode, msg}
	}
	if out == nil || len(b) == 0 {
		return nil
	}
	return json.Unmarshal(b, out)
}

// Event is an entry in an investigation's timeline.
type Event struct {
	ID            int64     `json:"id"`
	Time          time.Time `json:"time"`
	By            string    `json:"by"`
	Kind          string    `json:"kind"`
	Text          string    `json:"text"`
	Action        string    `json:"action,omitempty"`
	Host          string    `json:"host,omitempty"`
	Namespace     string    `json:"namespace,omitempty"`
	Target        string    `json:"target,omitempty"`
	Disruptive    bool      `json:"disruptive,omitempty"`
	Result        string    `json:"result,omitempty"`
	Investigation int64     `json:"investigation,omitempty"`
	Title         string    `json:"title,omitempty"`
}

// Where is the action's target with its namespace.
func (e Event) Where() string {
	if e.Namespace != "" {
		return e.Namespace + "/" + e.Target
	}
	return e.Target
}

// Investigation is something agents are working on.
type Investigation struct {
	ID        int64     `json:"id"`
	Kind      string    `json:"kind"`
	Title     string    `json:"title"`
	Status    string    `json:"status"`
	Rehearsal bool      `json:"rehearsal,omitempty"`
	Opened    time.Time `json:"opened"`
	Summary   string    `json:"summary,omitempty"`
	Fix       string    `json:"fix,omitempty"`
	Events    []Event   `json:"events,omitempty"`
}

// Live reports whether it's still being worked.
func (i Investigation) Live() bool { return i.Status == "open" || i.Status == "escalated" }

// Escalation is the last thing said when it was handed to people.
func (i Investigation) Escalation() string {
	for j := len(i.Events) - 1; j >= 0; j-- {
		if i.Events[j].Kind == "escalated" {
			return i.Events[j].Text
		}
	}
	return ""
}

// Awaiting lists agents' actions waiting for a person.
func (c *Client) Awaiting(ctx context.Context) ([]Event, error) {
	var out []Event
	return out, c.do(ctx, http.MethodGet, "/api/agents/awaiting", nil, nil, &out)
}

// Escalated lists investigations waiting for a person.
func (c *Client) Escalated(ctx context.Context) ([]Investigation, error) {
	var out []Investigation
	return out, c.do(ctx, http.MethodGet, "/api/investigations", url.Values{"status": {"escalated"}, "limit": {"50"}}, nil, &out)
}

// Investigation gets one, with its timeline.
func (c *Client) Investigation(ctx context.Context, id int64) (Investigation, error) {
	var out Investigation
	return out, c.do(ctx, http.MethodGet, fmt.Sprintf("/api/investigations/%d", id), nil, nil, &out)
}

// Decide approves or denies an awaiting action.
func (c *Client) Decide(ctx context.Context, inv, event int64, approve bool) (Event, error) {
	verb := "deny"
	if approve {
		verb = "approve"
	}
	var out Event
	return out, c.do(ctx, http.MethodPost, fmt.Sprintf("/api/investigations/%d/actions/%d/%s", inv, event, verb), nil, struct{}{}, &out)
}

// Note adds a note to an investigation's timeline.
func (c *Client) Note(ctx context.Context, inv int64, text string) error {
	return c.do(ctx, http.MethodPost, fmt.Sprintf("/api/investigations/%d/events", inv), nil, map[string]string{"kind": "note", "text": text}, nil)
}

// Reopen hands an escalated investigation back to agents.
func (c *Client) Reopen(ctx context.Context, inv int64, why string) error {
	return c.do(ctx, http.MethodPost, fmt.Sprintf("/api/investigations/%d/reopen", inv), nil, map[string]string{"why": why}, nil)
}

// Abandon closes an investigation as not worth doing.
func (c *Client) Abandon(ctx context.Context, inv int64, summary string) error {
	return c.do(ctx, http.MethodPost, fmt.Sprintf("/api/investigations/%d/close", inv), nil, map[string]string{"status": "abandoned", "summary": summary}, nil)
}

// Ack acknowledges a firing alert.
func (c *Client) Ack(ctx context.Context, alert, note string) error {
	return c.do(ctx, http.MethodPost, "/api/alerts/ack", nil, map[string]string{"id": alert, "note": note}, nil)
}

// Silence silences a rule (or one key of it) until a time.
func (c *Client) Silence(ctx context.Context, rule, key string, until time.Time, note string) error {
	return c.do(ctx, http.MethodPost, "/api/alerts/silences", nil, map[string]any{"rule": rule, "key": key, "until": until, "note": note}, nil)
}

// Pause stops all agents' changes; Resume lets them go on.
func (c *Client) Pause(ctx context.Context, note string) error {
	return c.do(ctx, http.MethodPost, "/api/agents/pause", nil, map[string]string{"note": note}, nil)
}

// Resume lets agents make changes again.
func (c *Client) Resume(ctx context.Context) error {
	return c.do(ctx, http.MethodPost, "/api/agents/resume", nil, struct{}{}, nil)
}

// Agents is the agents' state.
type Agents struct {
	Paused struct {
		Paused bool   `json:"paused"`
		By     string `json:"by"`
		Note   string `json:"note"`
	} `json:"paused"`
	Open      int `json:"open"`
	Escalated int `json:"escalated"`
}

// AgentsState gets the agents' state.
func (c *Client) AgentsState(ctx context.Context) (Agents, error) {
	var out Agents
	return out, c.do(ctx, http.MethodGet, "/api/agents", nil, nil, &out)
}

// Alert is a firing or pending alert.
type Alert struct {
	ID       string    `json:"id"`
	Summary  string    `json:"summary"`
	Severity string    `json:"severity"`
	State    string    `json:"state"`
	Since    time.Time `json:"since"`
}

// Alerts lists the alerts now pending or firing.
func (c *Client) Alerts(ctx context.Context) ([]Alert, error) {
	var out struct {
		Alerts []Alert `json:"alerts"`
	}
	return out.Alerts, c.do(ctx, http.MethodGet, "/api/alerts", nil, nil, &out)
}
