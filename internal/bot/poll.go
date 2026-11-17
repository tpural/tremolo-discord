package bot

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/bwmarrin/discordgo"

	"github.com/tpural/tremolo-discord/internal/tremolo"
)

// Poll posts what's new from tremolo (actions awaiting approval, escalated
// investigations) and settles messages whose action was decided or whose
// investigation closed elsewhere.
func (b *Bot) Poll(ctx context.Context) error {
	waiting, err := b.T.Awaiting(ctx)
	if err != nil {
		return err
	}
	escalated, err := b.T.Escalated(ctx)
	if err != nil {
		return err
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.init()

	live := map[string]bool{}
	for _, e := range waiting {
		k := fmt.Sprintf("%d/%d", e.Investigation, e.ID)
		live[k] = true
		if b.posted["action "+k] {
			continue
		}
		m, err := b.post("action "+k, awaiting(e), row(
			button("Approve", "approve", discordgo.SuccessButton),
			button("Deny", "deny", discordgo.DangerButton),
			link("Open", b.invURL(e.Investigation)),
		))
		if err != nil {
			return err
		}
		b.actions[k] = m.ID
	}
	for k, msg := range b.actions {
		if live[k] {
			continue
		}
		inv, ev := splitAction(k)
		i, err := b.T.Investigation(ctx, inv)
		if err != nil {
			continue
		}
		for _, e := range i.Events {
			if e.ID == ev {
				if err := b.edit(msg, decidedColour(e.Kind), "", decided(e), nil); err != nil {
					return err
				}
			}
		}
		delete(b.actions, k)
	}

	escalatedNow := map[int64]bool{}
	for _, i := range escalated {
		// Gaps wait in the daily report; rehearsals are the eval's.
		if i.Rehearsal || i.Kind == "gap" {
			continue
		}
		escalatedNow[i.ID] = true
		key := "investigation " + strconv.FormatInt(i.ID, 10)
		if b.posted[key] {
			continue
		}
		full, err := b.T.Investigation(ctx, i.ID)
		if err != nil {
			return err
		}
		m, err := b.post(key, &discordgo.MessageEmbed{
			Title:       clip(fmt.Sprintf("Needs a person: #%d %s", i.ID, i.Title), 256),
			Description: clip(full.Escalation(), 4000),
			Color:       amber,
		}, row(
			button("Hand back to upkeeper", "reopen", discordgo.PrimaryButton),
			button("Abandon", "abandon", discordgo.SecondaryButton),
			link("Open", b.invURL(i.ID)),
		))
		if err != nil {
			return err
		}
		b.invs[i.ID] = m.ID
		if _, err := b.D.MessageThreadStartComplex(b.Channel, m.ID, &discordgo.ThreadStart{
			Name: clip(fmt.Sprintf("#%d %s", i.ID, i.Title), 100), AutoArchiveDuration: 10080,
		}); err != nil {
			b.Log.Warn("starting a thread", "investigation", i.ID, "err", err)
		} else if _, err := b.D.ChannelMessageSendComplex(m.ID, &discordgo.MessageSend{
			Content: "Replies here are added to the investigation as notes, for upkeeper and people to read.",
		}); err != nil {
			b.Log.Warn("posting in a thread", "investigation", i.ID, "err", err)
		}
	}
	for id, msg := range b.invs {
		if escalatedNow[id] {
			continue
		}
		i, err := b.T.Investigation(ctx, id)
		if err != nil || i.Live() {
			continue // handed back: settled when it's closed, or escalated again
		}
		b.closed(i, msg)
		delete(b.invs, id)
	}
	return nil
}

// closed says how an investigation ended, in its thread, and archives it.
func (b *Bot) closed(i tremolo.Investigation, msg string) {
	text := "Closed: " + i.Status
	if i.Fix != "" {
		text += ": " + i.Fix
	} else if i.Summary != "" {
		text += ": " + i.Summary
	}
	if err := b.edit(msg, green, "", clip(text, 1000), nil); err != nil {
		b.Log.Warn("editing a closed investigation", "investigation", i.ID, "err", err)
	}
	if _, err := b.D.ChannelMessageSendComplex(msg, &discordgo.MessageSend{Content: clip(text, 2000)}); err != nil {
		return // no thread
	}
	archived := true
	if _, err := b.D.ChannelEdit(msg, &discordgo.ChannelEdit{Archived: &archived}); err != nil {
		b.Log.Warn("archiving a thread", "investigation", i.ID, "err", err)
	}
}

func awaiting(e tremolo.Event) *discordgo.MessageEmbed {
	desc := e.Text
	if e.Disruptive {
		desc += "\n\nDisruptive."
	}
	desc += fmt.Sprintf("\n\nFor #%d %s", e.Investigation, e.Title)
	return &discordgo.MessageEmbed{
		Title:       clip(fmt.Sprintf("Approve? %s wants to %s %s", e.By, e.Action, e.Where()), 256),
		Description: clip(strings.TrimSpace(desc), 4000),
		Color:       purple,
	}
}

func decided(e tremolo.Event) string {
	switch e.Kind {
	case "action":
		return "Approved: " + e.Text
	case "awaiting":
		return "Still waiting."
	}
	return strings.ToUpper(e.Kind[:1]) + e.Kind[1:] + ": " + e.Text
}

func decidedColour(kind string) int {
	if kind == "action" {
		return green
	}
	if slices.Contains([]string{"denied", "expired", "refused"}, kind) {
		return grey
	}
	return 0
}

func splitAction(k string) (inv, ev int64) {
	a, b, _ := strings.Cut(k, "/")
	inv, _ = strconv.ParseInt(a, 10, 64)
	ev, _ = strconv.ParseInt(b, 10, 64)
	return inv, ev
}
