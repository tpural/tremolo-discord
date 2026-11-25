// Package bot is tremolo in a Discord channel: alerts with buttons to
// acknowledge or silence them, agents' actions to approve or deny, a thread
// for each escalated investigation where replies become notes, and commands
// to pause and resume agents.
//
// It keeps no state of its own. Each message it posts carries its key (an
// alert, an action or an investigation) in its footer; on start it reads the
// channel's recent messages to know what it posted, and a button finds what
// it's for in the message it's on. A thread started from a message has the
// message's ID, so a reply finds its investigation the same way.
package bot

import (
	"context"
	"fmt"
	"log/slog"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/bwmarrin/discordgo"

	"github.com/tpural/tremolo-discord/internal/tremolo"
)

// Discord is the part of a discordgo session the bot uses.
type Discord interface {
	ChannelMessageSendComplex(channelID string, data *discordgo.MessageSend, options ...discordgo.RequestOption) (*discordgo.Message, error)
	ChannelMessageEditComplex(m *discordgo.MessageEdit, options ...discordgo.RequestOption) (*discordgo.Message, error)
	ChannelMessages(channelID string, limit int, beforeID, afterID, aroundID string, options ...discordgo.RequestOption) ([]*discordgo.Message, error)
	ChannelMessage(channelID, messageID string, options ...discordgo.RequestOption) (*discordgo.Message, error)
	MessageThreadStartComplex(channelID, messageID string, data *discordgo.ThreadStart, options ...discordgo.RequestOption) (*discordgo.Channel, error)
	ChannelEdit(channelID string, data *discordgo.ChannelEdit, options ...discordgo.RequestOption) (*discordgo.Channel, error)
	Channel(channelID string, options ...discordgo.RequestOption) (*discordgo.Channel, error)
	InteractionRespond(interaction *discordgo.Interaction, resp *discordgo.InteractionResponse, options ...discordgo.RequestOption) error
	MessageReactionAdd(channelID, messageID, emojiID string, options ...discordgo.RequestOption) error
}

// Tremolo is the part of tremolo's API the bot uses.
type Tremolo interface {
	Awaiting(ctx context.Context) ([]tremolo.Event, error)
	Escalated(ctx context.Context) ([]tremolo.Investigation, error)
	Investigation(ctx context.Context, id int64) (tremolo.Investigation, error)
	Decide(ctx context.Context, inv, event int64, approve bool) (tremolo.Event, error)
	Note(ctx context.Context, inv int64, text string) error
	Reopen(ctx context.Context, inv int64, why string) error
	Abandon(ctx context.Context, inv int64, summary string) error
	Ack(ctx context.Context, alert, note string) error
	Silence(ctx context.Context, rule, key string, until time.Time, note string) error
	Pause(ctx context.Context, note string) error
	Resume(ctx context.Context) error
	AgentsState(ctx context.Context) (tremolo.Agents, error)
	Alerts(ctx context.Context) ([]tremolo.Alert, error)
}

// Bot posts to one channel.
type Bot struct {
	D       Discord
	T       Tremolo
	Channel string
	// URL is tremolo's UI, for links.
	URL string
	// Allowed is who may press buttons, run commands and add notes.
	Allowed func(userID string) bool
	// Patrol asks upkeeper for a patrol; nil means there's no /patrol.
	Patrol func(ctx context.Context) error
	Log    *slog.Logger
	Now    func() time.Time

	mu sync.Mutex
	// posted is every key in the channel's recent messages.
	posted map[string]bool
	// alerts, actions and invs are messages whose buttons are still live.
	alerts  map[string]string // alert ID → message
	actions map[string]string // "inv/event" → message
	invs    map[int64]string  // investigation → message, and its thread
}

// Colours by what a message is about.
const (
	red    = 0xd9534f
	amber  = 0xf0ad4e
	green  = 0x5cb85c
	blue   = 0x5b8def
	purple = 0x9b59b6
	grey   = 0x8a8f98
)

const footer = "tremolo · "

var keyRe = regexp.MustCompile(`^tremolo · (alert|action|investigation) (\S+)$`)

// keyOf is a message's key, e.g. "action 509/7", and its kind and value.
func keyOf(m *discordgo.Message) (kind, value string) {
	if m == nil || len(m.Embeds) == 0 || m.Embeds[0].Footer == nil {
		return "", ""
	}
	if k := keyRe.FindStringSubmatch(m.Embeds[0].Footer.Text); k != nil {
		return k[1], k[2]
	}
	return "", ""
}

func (b *Bot) init() {
	if b.posted == nil {
		b.posted, b.alerts, b.actions, b.invs = map[string]bool{}, map[string]string{}, map[string]string{}, map[int64]string{}
	}
	if b.Now == nil {
		b.Now = time.Now
	}
	if b.Log == nil {
		b.Log = slog.New(slog.DiscardHandler)
	}
}

// Rebuild reads the channel's recent messages to learn what's been posted
// and which buttons are still live.
func (b *Bot) Rebuild() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.init()
	before := ""
	for range 5 {
		msgs, err := b.D.ChannelMessages(b.Channel, 100, before, "", "")
		if err != nil {
			return err
		}
		for _, m := range msgs {
			kind, v := keyOf(m)
			if kind == "" {
				continue
			}
			b.posted[kind+" "+v] = true
			if !hasButtons(m) {
				continue
			}
			switch kind {
			case "alert":
				if _, ok := b.alerts[v]; !ok { // newest first
					b.alerts[v] = m.ID
				}
			case "action":
				b.actions[v] = m.ID
			case "investigation":
				if id, err := strconv.ParseInt(v, 10, 64); err == nil {
					b.invs[id] = m.ID
				}
			}
		}
		if len(msgs) < 100 {
			break
		}
		before = msgs[len(msgs)-1].ID
	}
	return nil
}

func hasButtons(m *discordgo.Message) bool {
	for _, c := range m.Components {
		if row, ok := c.(*discordgo.ActionsRow); ok {
			for _, x := range row.Components {
				if bt, ok := x.(*discordgo.Button); ok && bt.Style != discordgo.LinkButton {
					return true
				}
			}
		}
	}
	return false
}

func clip(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n-1]) + "…"
	}
	return s
}

func button(label, id string, style discordgo.ButtonStyle) discordgo.MessageComponent {
	return discordgo.Button{Label: label, CustomID: id, Style: style}
}

func link(label, url string) discordgo.MessageComponent {
	return discordgo.Button{Label: label, URL: url, Style: discordgo.LinkButton}
}

func row(cs ...discordgo.MessageComponent) []discordgo.MessageComponent {
	var keep []discordgo.MessageComponent
	for _, c := range cs {
		if bt, ok := c.(discordgo.Button); ok && bt.Style == discordgo.LinkButton && bt.URL == "" {
			continue
		}
		keep = append(keep, c)
	}
	return []discordgo.MessageComponent{discordgo.ActionsRow{Components: keep}}
}

// links keeps only a message's link buttons.
func links(m *discordgo.Message) []discordgo.MessageComponent {
	var keep []discordgo.MessageComponent
	for _, c := range m.Components {
		if r, ok := c.(*discordgo.ActionsRow); ok {
			for _, x := range r.Components {
				if bt, ok := x.(*discordgo.Button); ok && bt.Style == discordgo.LinkButton {
					keep = append(keep, *bt)
				}
			}
		}
	}
	if len(keep) == 0 {
		return []discordgo.MessageComponent{}
	}
	return []discordgo.MessageComponent{discordgo.ActionsRow{Components: keep}}
}

func (b *Bot) invURL(id int64) string {
	if b.URL == "" {
		return ""
	}
	return fmt.Sprintf("%s/investigations/%d", b.URL, id)
}

func (b *Bot) post(key string, e *discordgo.MessageEmbed, components []discordgo.MessageComponent) (*discordgo.Message, error) {
	if key != "" {
		e.Footer = &discordgo.MessageEmbedFooter{Text: footer + key}
	}
	e.Timestamp = b.Now().Format(time.RFC3339)
	m, err := b.D.ChannelMessageSendComplex(b.Channel, &discordgo.MessageSend{Embeds: []*discordgo.MessageEmbed{e}, Components: components})
	if err == nil && key != "" {
		b.posted[key] = true
	}
	return m, err
}

// edit replaces a message's embed: its colour, title and a line added, and
// its buttons with components.
func (b *Bot) edit(msgID string, colour int, title, line string, components []discordgo.MessageComponent) error {
	m, err := b.D.ChannelMessage(b.Channel, msgID)
	if err != nil {
		return err
	}
	if components == nil {
		components = links(m)
	}
	_, err = b.D.ChannelMessageEditComplex(&discordgo.MessageEdit{
		ID: msgID, Channel: b.Channel, Embeds: &[]*discordgo.MessageEmbed{amend(m, colour, title, line)}, Components: &components,
	})
	return err
}

func amend(m *discordgo.Message, colour int, title, line string) *discordgo.MessageEmbed {
	e := &discordgo.MessageEmbed{}
	if len(m.Embeds) > 0 {
		c := *m.Embeds[0]
		e = &c
	}
	if colour != 0 {
		e.Color = colour
	}
	if title != "" {
		e.Title = title
	}
	if line != "" {
		e.Description = clip(strings.TrimSpace(e.Description+"\n\n"+line), 4000)
	}
	return e
}
