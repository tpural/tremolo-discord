package bot

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/bwmarrin/discordgo"

	"github.com/tpural/tremolo-discord/internal/tremolo"
	"github.com/tpural/tremolo-discord/internal/upkeeper"
)

var commands = []*discordgo.ApplicationCommand{
	{Name: "status", Description: "What's firing, and what agents are doing and waiting on"},
	{Name: "pause", Description: "Stop all agents' changes", Options: []*discordgo.ApplicationCommandOption{
		{Type: discordgo.ApplicationCommandOptionString, Name: "why", Description: "Why, for the record", Required: true},
	}},
	{Name: "resume", Description: "Let agents make changes again"},
}

var patrol = &discordgo.ApplicationCommand{Name: "patrol", Description: "Have upkeeper patrol the cluster now"}

// Commands are the slash commands the bot answers: /patrol only when it can
// ask upkeeper.
func (b *Bot) Commands() []*discordgo.ApplicationCommand {
	if b.Patrol == nil {
		return commands
	}
	return append(commands[:len(commands):len(commands)], patrol)
}

func userOf(i *discordgo.Interaction) *discordgo.User {
	if i.Member != nil && i.Member.User != nil {
		return i.Member.User
	}
	return i.User
}

func nameOf(u *discordgo.User) string {
	if u == nil {
		return "someone"
	}
	if u.GlobalName != "" {
		return u.GlobalName
	}
	return u.Username
}

// Interaction answers a button press or a command.
func (b *Bot) Interaction(ctx context.Context, i *discordgo.Interaction) {
	b.mu.Lock()
	b.init()
	b.mu.Unlock()
	u := userOf(i)
	if u == nil || b.Allowed == nil || !b.Allowed(u.ID) {
		b.whisper(i, "Only the lab's people can do that.")
		return
	}
	via := "from Discord by " + nameOf(u)
	switch i.Type {
	case discordgo.InteractionApplicationCommand:
		b.command(ctx, i, via)
	case discordgo.InteractionMessageComponent:
		b.press(ctx, i, i.MessageComponentData().CustomID, via)
	}
}

func (b *Bot) whisper(i *discordgo.Interaction, text string) {
	if err := b.D.InteractionRespond(i, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseChannelMessageWithSource,
		Data: &discordgo.InteractionResponseData{Content: clip(text, 2000), Flags: discordgo.MessageFlagsEphemeral},
	}); err != nil {
		b.Log.Warn("answering", "err", err)
	}
}

// update replaces the pressed message's embed and buttons.
func (b *Bot) update(i *discordgo.Interaction, colour int, line string) {
	e := amend(i.Message, colour, "", line)
	if err := b.D.InteractionRespond(i, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseUpdateMessage,
		Data: &discordgo.InteractionResponseData{Embeds: []*discordgo.MessageEmbed{e}, Components: links(i.Message)},
	}); err != nil {
		b.Log.Warn("updating", "err", err)
	}
}

func (b *Bot) press(ctx context.Context, i *discordgo.Interaction, id, via string) {
	kind, v := keyOf(i.Message)
	var err error
	switch {
	case kind == "alert" && id == "ack":
		if err = b.T.Ack(ctx, v, via); err == nil {
			b.forget(kind, v)
			b.update(i, amber, "Acknowledged "+via+".")
		}
	case kind == "alert" && id == "silence":
		rule, key, _ := strings.Cut(v, "/")
		if err = b.T.Silence(ctx, rule, key, b.Now().Add(time.Hour), via); err == nil {
			b.forget(kind, v)
			b.update(i, grey, "Silenced for an hour "+via+".")
		}
	case kind == "action" && (id == "approve" || id == "deny"):
		inv, ev := splitAction(v)
		var d tremolo.Event
		if d, err = b.T.Decide(ctx, inv, ev, id == "approve"); err == nil {
			b.forget(kind, v)
			b.update(i, decidedColour(d.Kind), decided(d))
		}
	case kind == "investigation" && (id == "reopen" || id == "abandon"):
		inv, _ := strconv.ParseInt(v, 10, 64)
		if id == "reopen" {
			if err = b.T.Reopen(ctx, inv, via); err == nil {
				b.update(i, blue, "Handed back to upkeeper "+via+".")
			}
		} else if err = b.T.Abandon(ctx, inv, "Abandoned "+via+"."); err == nil {
			b.update(i, grey, "Abandoned "+via+".")
		}
	default:
		err = fmt.Errorf("this message has nothing to %s", id)
	}
	if err != nil {
		b.whisper(i, "tremolo said: "+err.Error())
	}
}

// forget drops a message whose buttons were used.
func (b *Bot) forget(kind, v string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	switch kind {
	case "alert":
		delete(b.alerts, v)
	case "action":
		delete(b.actions, v)
	}
}

func (b *Bot) command(ctx context.Context, i *discordgo.Interaction, via string) {
	data := i.ApplicationCommandData()
	switch data.Name {
	case "pause":
		why := ""
		if len(data.Options) > 0 {
			why = data.Options[0].StringValue()
		}
		if err := b.T.Pause(ctx, strings.TrimSpace(why)+" ("+via+")"); err != nil {
			b.whisper(i, "tremolo said: "+err.Error())
			return
		}
		b.say(i, "Agents are paused: "+why)
	case "resume":
		if err := b.T.Resume(ctx); err != nil {
			b.whisper(i, "tremolo said: "+err.Error())
			return
		}
		b.say(i, "Agents can make changes again.")
	case "status":
		b.whisper(i, b.status(ctx))
	case "patrol":
		if b.Patrol == nil {
			b.whisper(i, "There's no upkeeper to ask.")
			return
		}
		switch err := b.Patrol(ctx); {
		case errors.Is(err, upkeeper.ErrAsked):
			b.whisper(i, "A patrol is already on its way.")
		case err != nil:
			b.whisper(i, "upkeeper said: "+err.Error())
		default:
			b.say(i, "upkeeper will patrol once the investigations waiting are done (asked "+via+").")
		}
	}
}

func (b *Bot) say(i *discordgo.Interaction, text string) {
	if err := b.D.InteractionRespond(i, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseChannelMessageWithSource,
		Data: &discordgo.InteractionResponseData{Content: clip(text, 2000)},
	}); err != nil {
		b.Log.Warn("answering", "err", err)
	}
}

func (b *Bot) status(ctx context.Context) string {
	var s strings.Builder
	if alerts, err := b.T.Alerts(ctx); err != nil {
		fmt.Fprintf(&s, "Alerts: couldn't ask (%s)\n", err)
	} else {
		firing := 0
		for _, a := range alerts {
			if a.State == "firing" {
				firing++
				fmt.Fprintf(&s, "🔥 %s (%s)\n", clip(a.Summary, 150), human(b.Now().Sub(a.Since)))
			}
		}
		if firing == 0 {
			s.WriteString("Nothing firing.\n")
		}
	}
	if st, err := b.T.AgentsState(ctx); err == nil {
		if st.Paused.Paused {
			fmt.Fprintf(&s, "Agents are paused by %s: %s\n", st.Paused.By, st.Paused.Note)
		}
		fmt.Fprintf(&s, "Investigations: %d open, %d waiting for a person.\n", st.Open, st.Escalated)
	}
	if w, err := b.T.Awaiting(ctx); err == nil && len(w) > 0 {
		fmt.Fprintf(&s, "Actions to approve: %d\n", len(w))
		for _, e := range w {
			fmt.Fprintf(&s, "  • %s: %s %s (#%d)\n", e.By, e.Action, e.Where(), e.Investigation)
		}
	}
	return s.String()
}

// Message adds a reply in an investigation's thread to its timeline.
func (b *Bot) Message(ctx context.Context, m *discordgo.Message) {
	if m.Author == nil || m.Author.Bot || strings.TrimSpace(m.Content) == "" {
		return
	}
	ch, err := b.D.Channel(m.ChannelID)
	if err != nil || !ch.IsThread() || ch.ParentID != b.Channel {
		return
	}
	starter, err := b.D.ChannelMessage(b.Channel, m.ChannelID)
	if err != nil {
		return
	}
	kind, v := keyOf(starter)
	if kind != "investigation" {
		return
	}
	if b.Allowed == nil || !b.Allowed(m.Author.ID) {
		return // only the lab's people talk to agents
	}
	inv, _ := strconv.ParseInt(v, 10, 64)
	if err := b.T.Note(ctx, inv, fmt.Sprintf("%s on Discord: %s", nameOf(m.Author), m.Content)); err != nil {
		_, _ = b.D.ChannelMessageSendComplex(m.ChannelID, &discordgo.MessageSend{
			Content: "Couldn't add that: " + err.Error(), Reference: m.Reference(),
		})
		return
	}
	if err := b.D.MessageReactionAdd(m.ChannelID, m.ID, "✅"); err != nil {
		b.Log.Warn("reacting", "err", err)
	}
}
