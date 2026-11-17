package bot

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/bwmarrin/discordgo"
)

// Notification is what tremolo's webhook channel sends.
type Notification struct {
	Event      string            `json:"event"`
	Rule       string            `json:"rule"`
	Key        string            `json:"key,omitempty"`
	Summary    string            `json:"summary"`
	Severity   string            `json:"severity"`
	Labels     map[string]string `json:"labels,omitempty"`
	Since      time.Time         `json:"since"`
	URL        string            `json:"url,omitempty"`
	AckURL     string            `json:"ackUrl,omitempty"`
	RunbookURL string            `json:"runbookUrl,omitempty"`
	Note       string            `json:"note,omitempty"`
}

// ID is the alert's ID in tremolo: its rule, and its key if it has one.
func (n Notification) ID() string {
	if n.Key == "" {
		return n.Rule
	}
	return n.Rule + "/" + n.Key
}

// Announcements the bot posts better itself, from tremolo's API.
var covered = []string{"Agents need a person: ", "Approve? "}

// Notify posts or updates a message for a notification from tremolo.
func (b *Bot) Notify(n Notification) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.init()
	switch n.Event {
	case "firing", "repeat":
		return b.firing(n)
	case "acknowledged":
		if id, ok := b.alerts[n.ID()]; ok {
			return b.edit(id, amber, "", "Acknowledged.", nil)
		}
		return nil
	case "resolved":
		id, ok := b.alerts[n.ID()]
		if !ok {
			return nil
		}
		delete(b.alerts, n.ID())
		return b.edit(id, green, "Resolved: "+n.Rule, fmt.Sprintf("Resolved after %s.", human(b.Now().Sub(n.Since))), nil)
	case "announce":
		for _, c := range covered {
			if strings.HasPrefix(n.Rule, c) {
				return nil
			}
		}
		_, err := b.post("", &discordgo.MessageEmbed{Title: clip(n.Rule, 256), Description: clip(n.Summary, 4000), Color: blue}, nil)
		return err
	default:
		_, err := b.post("", &discordgo.MessageEmbed{Title: clip(n.Rule, 256), Description: clip(n.Summary, 4000), Color: grey}, nil)
		return err
	}
}

func (b *Bot) firing(n Notification) error {
	id := n.ID()
	if msg, ok := b.alerts[id]; ok {
		if n.Event == "repeat" {
			return b.edit(msg, 0, "", fmt.Sprintf("Still firing, %s on.", human(b.Now().Sub(n.Since))), nil)
		}
		// Fired again while the old message is live: it's done with.
		_ = b.edit(msg, grey, "", "Fired again; see below.", nil)
	}
	colour := amber
	if n.Severity == "page" {
		colour = red
	}
	e := &discordgo.MessageEmbed{Title: clip("Firing: "+n.Rule, 256), Description: clip(describe(n), 4000), Color: colour}
	m, err := b.post("alert "+id, e, row(
		button("Acknowledge", "ack", discordgo.PrimaryButton),
		button("Silence 1h", "silence", discordgo.SecondaryButton),
		link("Open", n.AckURL),
		link("Runbook", n.RunbookURL),
	))
	if err != nil {
		return err
	}
	b.alerts[id] = m.ID
	return nil
}

func describe(n Notification) string {
	var s strings.Builder
	s.WriteString(n.Summary)
	if len(n.Labels) > 0 {
		keys := make([]string, 0, len(n.Labels))
		for k := range n.Labels {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		s.WriteString("\n")
		for _, k := range keys {
			fmt.Fprintf(&s, "\n`%s` %s", k, n.Labels[k])
		}
	}
	return s.String()
}

func human(d time.Duration) string {
	switch {
	case d < time.Minute:
		return "under a minute"
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh%02dm", int(d.Hours()), int(d.Minutes())%60)
	}
	return fmt.Sprintf("%dd", int(d.Hours()/24))
}
