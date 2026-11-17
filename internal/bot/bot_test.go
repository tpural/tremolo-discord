package bot

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bwmarrin/discordgo"

	"github.com/tpural/tremolo-discord/internal/tremolo"
)

// fakeDiscord keeps messages by channel, newest last, and what was done.
type fakeDiscord struct {
	mu       sync.Mutex
	n        int
	msgs     map[string][]*discordgo.Message
	threads  map[string]*discordgo.Channel
	answers  []*discordgo.InteractionResponse
	reacted  []string
	archived []string
}

func newDiscord() *fakeDiscord {
	return &fakeDiscord{msgs: map[string][]*discordgo.Message{}, threads: map[string]*discordgo.Channel{}}
}

func toMessage(cs []discordgo.MessageComponent) []discordgo.MessageComponent {
	// What Discord sends back: rows and buttons as pointers.
	var out []discordgo.MessageComponent
	for _, c := range cs {
		r := c.(discordgo.ActionsRow)
		row := &discordgo.ActionsRow{}
		for _, x := range r.Components {
			b := x.(discordgo.Button)
			row.Components = append(row.Components, &b)
		}
		out = append(out, row)
	}
	return out
}

func (f *fakeDiscord) ChannelMessageSendComplex(ch string, d *discordgo.MessageSend, _ ...discordgo.RequestOption) (*discordgo.Message, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.n++
	m := &discordgo.Message{ID: fmt.Sprint(1000 + f.n), ChannelID: ch, Content: d.Content, Embeds: d.Embeds, Components: toMessage(d.Components)}
	f.msgs[ch] = append(f.msgs[ch], m)
	return m, nil
}

func (f *fakeDiscord) find(ch, id string) *discordgo.Message {
	for _, m := range f.msgs[ch] {
		if m.ID == id {
			return m
		}
	}
	return nil
}

func (f *fakeDiscord) ChannelMessageEditComplex(e *discordgo.MessageEdit, _ ...discordgo.RequestOption) (*discordgo.Message, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	m := f.find(e.Channel, e.ID)
	if m == nil {
		return nil, fmt.Errorf("no message %s", e.ID)
	}
	if e.Embeds != nil {
		m.Embeds = *e.Embeds
	}
	if e.Components != nil {
		m.Components = toMessage(*e.Components)
	}
	return m, nil
}

func (f *fakeDiscord) ChannelMessages(ch string, limit int, _, _, _ string, _ ...discordgo.RequestOption) ([]*discordgo.Message, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := slices.Clone(f.msgs[ch])
	slices.Reverse(out)
	return out[:min(limit, len(out))], nil
}

func (f *fakeDiscord) ChannelMessage(ch, id string, _ ...discordgo.RequestOption) (*discordgo.Message, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if m := f.find(ch, id); m != nil {
		return m, nil
	}
	return nil, fmt.Errorf("no message %s", id)
}

func (f *fakeDiscord) MessageThreadStartComplex(ch, id string, d *discordgo.ThreadStart, _ ...discordgo.RequestOption) (*discordgo.Channel, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	c := &discordgo.Channel{ID: id, ParentID: ch, Name: d.Name, Type: discordgo.ChannelTypeGuildPublicThread}
	f.threads[id] = c
	return c, nil
}

func (f *fakeDiscord) ChannelEdit(id string, d *discordgo.ChannelEdit, _ ...discordgo.RequestOption) (*discordgo.Channel, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if d.Archived != nil && *d.Archived {
		f.archived = append(f.archived, id)
	}
	return f.threads[id], nil
}

func (f *fakeDiscord) Channel(id string, _ ...discordgo.RequestOption) (*discordgo.Channel, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if c, ok := f.threads[id]; ok {
		return c, nil
	}
	return &discordgo.Channel{ID: id, Type: discordgo.ChannelTypeGuildText}, nil
}

func (f *fakeDiscord) InteractionRespond(_ *discordgo.Interaction, r *discordgo.InteractionResponse, _ ...discordgo.RequestOption) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.answers = append(f.answers, r)
	return nil
}

func (f *fakeDiscord) MessageReactionAdd(ch, id, emoji string, _ ...discordgo.RequestOption) error {
	f.reacted = append(f.reacted, ch+"/"+id+" "+emoji)
	return nil
}

// fakeTremolo has awaiting actions and investigations, and records calls.
type fakeTremolo struct {
	waiting []tremolo.Event
	invs    map[int64]tremolo.Investigation
	did     []string
}

func (f *fakeTremolo) Awaiting(context.Context) ([]tremolo.Event, error) { return f.waiting, nil }

func (f *fakeTremolo) Escalated(context.Context) ([]tremolo.Investigation, error) {
	var out []tremolo.Investigation
	for _, i := range f.invs {
		if i.Status == "escalated" {
			out = append(out, i)
		}
	}
	return out, nil
}

func (f *fakeTremolo) Investigation(_ context.Context, id int64) (tremolo.Investigation, error) {
	return f.invs[id], nil
}

func (f *fakeTremolo) Decide(_ context.Context, inv, ev int64, approve bool) (tremolo.Event, error) {
	f.did = append(f.did, fmt.Sprintf("decide %d/%d %v", inv, ev, approve))
	kind := "denied"
	if approve {
		kind = "action"
	}
	return tremolo.Event{ID: ev, Kind: kind, Text: "it flaps (" + kind + " by tp)"}, nil
}

func (f *fakeTremolo) Note(_ context.Context, inv int64, text string) error {
	f.did = append(f.did, fmt.Sprintf("note %d %s", inv, text))
	return nil
}

func (f *fakeTremolo) Reopen(_ context.Context, inv int64, why string) error {
	f.did = append(f.did, fmt.Sprintf("reopen %d %s", inv, why))
	return nil
}

func (f *fakeTremolo) Abandon(_ context.Context, inv int64, _ string) error {
	f.did = append(f.did, fmt.Sprintf("abandon %d", inv))
	return nil
}

func (f *fakeTremolo) Ack(_ context.Context, id, note string) error {
	f.did = append(f.did, "ack "+id+" "+note)
	return nil
}

func (f *fakeTremolo) Silence(_ context.Context, rule, key string, _ time.Time, _ string) error {
	f.did = append(f.did, "silence "+rule+" "+key)
	return nil
}

func (f *fakeTremolo) Pause(_ context.Context, note string) error {
	f.did = append(f.did, "pause "+note)
	return nil
}

func (f *fakeTremolo) Resume(context.Context) error {
	f.did = append(f.did, "resume")
	return nil
}

func (f *fakeTremolo) AgentsState(context.Context) (tremolo.Agents, error) {
	return tremolo.Agents{}, nil
}
func (f *fakeTremolo) Alerts(context.Context) ([]tremolo.Alert, error) { return nil, nil }

func setup() (*Bot, *fakeDiscord, *fakeTremolo) {
	d, t := newDiscord(), &fakeTremolo{invs: map[int64]tremolo.Investigation{}}
	now := time.Date(2026, 11, 17, 9, 0, 0, 0, time.UTC)
	b := &Bot{
		D: d, T: t, Channel: "ops", URL: "https://tremolo.example", Allowed: func(id string) bool { return id == "me" },
		Now: func() time.Time { return now },
	}
	return b, d, t
}

func press(msg *discordgo.Message, id, user string) *discordgo.Interaction {
	return &discordgo.Interaction{
		Type: discordgo.InteractionMessageComponent, Message: msg,
		Member: &discordgo.Member{User: &discordgo.User{ID: user, Username: "tim"}},
		Data:   discordgo.MessageComponentInteractionData{CustomID: id},
	}
}

func buttons(m *discordgo.Message) []string {
	var out []string
	for _, c := range m.Components {
		for _, x := range c.(*discordgo.ActionsRow).Components {
			out = append(out, x.(*discordgo.Button).Label)
		}
	}
	return out
}

func TestAlerts(t *testing.T) {
	b, d, tr := setup()
	since := b.Now().Add(-10 * time.Minute)
	n := Notification{
		Event: "firing", Rule: "crash-loop", Key: "web/api-1", Summary: "api-1 restarted 3 times", Severity: "warn",
		Since: since, AckURL: "https://tremolo.example/alerts?ack=x", Labels: map[string]string{"namespace": "web"},
	}
	if err := b.Notify(n); err != nil {
		t.Fatal(err)
	}
	m := d.msgs["ops"][0]
	if k, v := keyOf(m); k != "alert" || v != "crash-loop/web/api-1" || m.Embeds[0].Color != amber ||
		!slices.Equal(buttons(m), []string{"Acknowledge", "Silence 1h", "Open"}) {
		t.Fatalf("firing: %s %s, %+v, %v", k, v, m.Embeds[0], buttons(m))
	}
	// Repeats update it, rather than post again.
	n.Event = "repeat"
	_ = b.Notify(n)
	if len(d.msgs["ops"]) != 1 || !strings.Contains(m.Embeds[0].Description, "Still firing, 10m on.") {
		t.Errorf("repeat: %d messages, %q", len(d.msgs["ops"]), m.Embeds[0].Description)
	}
	// Silence 1h, from the message's own key; someone not allowed is told no.
	b.Interaction(context.Background(), press(m, "silence", "stranger"))
	if len(tr.did) != 0 || d.answers[0].Data.Flags != discordgo.MessageFlagsEphemeral {
		t.Errorf("a stranger: %v %+v", tr.did, d.answers[0])
	}
	b.Interaction(context.Background(), press(m, "silence", "me"))
	if !slices.Equal(tr.did, []string{"silence crash-loop web/api-1"}) {
		t.Errorf("silence: %v", tr.did)
	}
	if a := d.answers[1]; a.Type != discordgo.InteractionResponseUpdateMessage || !strings.Contains(a.Data.Embeds[0].Description, "Silenced for an hour from Discord by tim") {
		t.Errorf("silence answer %+v", a.Data)
	}

	// Resolved: green, its buttons gone.
	other := Notification{Event: "firing", Rule: "host-down", Summary: "w1 down", Severity: "page", Since: since}
	_ = b.Notify(other)
	m2 := d.msgs["ops"][1]
	if m2.Embeds[0].Color != red {
		t.Errorf("page colour %x", m2.Embeds[0].Color)
	}
	other.Event = "resolved"
	_ = b.Notify(other)
	if m2.Embeds[0].Color != green || m2.Embeds[0].Title != "Resolved: host-down" || len(buttons(m2)) != 0 {
		t.Errorf("resolved: %+v %v", m2.Embeds[0], buttons(m2))
	}

	// Announcements are posted, except those the bot posts better itself.
	_ = b.Notify(Notification{Event: "announce", Rule: "tremolo agents: the last day", Summary: "3 investigations"})
	_ = b.Notify(Notification{Event: "announce", Rule: "Agents need a person: x", Summary: "stuck"})
	if len(d.msgs["ops"]) != 3 || d.msgs["ops"][2].Embeds[0].Title != "tremolo agents: the last day" {
		t.Errorf("announce: %d messages", len(d.msgs["ops"]))
	}
}

func TestApprovals(t *testing.T) {
	ctx := context.Background()
	b, d, tr := setup()
	tr.invs[9] = tremolo.Investigation{ID: 9, Title: "w1 flaps", Status: "open"}
	tr.waiting = []tremolo.Event{
		{ID: 7, Investigation: 9, Title: "w1 flaps", By: "upkeeper", Action: "cordon", Target: "node/w1", Text: "it flaps"},
		{ID: 8, Investigation: 9, Title: "w1 flaps", By: "upkeeper", Action: "rollout restart", Namespace: "pg", Target: "sts/pg", Disruptive: true},
	}
	if err := b.Poll(ctx); err != nil {
		t.Fatal(err)
	}
	if len(d.msgs["ops"]) != 2 || d.msgs["ops"][0].Embeds[0].Title != "Approve? upkeeper wants to cordon node/w1" ||
		!slices.Equal(buttons(d.msgs["ops"][0]), []string{"Approve", "Deny", "Open"}) {
		t.Fatalf("awaiting: %+v", d.msgs["ops"])
	}
	_ = b.Poll(ctx)
	if len(d.msgs["ops"]) != 2 {
		t.Errorf("posted again: %d", len(d.msgs["ops"]))
	}

	// Approved with the button.
	b.Interaction(ctx, press(d.msgs["ops"][0], "approve", "me"))
	if !slices.Equal(tr.did, []string{"decide 9/7 true"}) || !strings.Contains(d.answers[0].Data.Embeds[0].Description, "Approved: it flaps (action by tp)") {
		t.Errorf("approve: %v %+v", tr.did, d.answers[0].Data)
	}

	// Denied on tremolo's page: the message is settled on the next poll.
	tr.waiting = nil
	tr.invs[9] = tremolo.Investigation{ID: 9, Status: "open", Events: []tremolo.Event{{ID: 8, Kind: "denied", Text: "restart (denied by tp)"}}}
	_ = b.Poll(ctx)
	m := d.msgs["ops"][1]
	if !strings.Contains(m.Embeds[0].Description, "Denied: restart (denied by tp)") || len(buttons(m)) != 1 {
		t.Errorf("denied elsewhere: %+v %v", m.Embeds[0], buttons(m))
	}

	// After a restart, what was posted is read back from the channel.
	b2 := &Bot{D: d, T: tr, Channel: "ops", Now: b.Now}
	if err := b2.Rebuild(); err != nil {
		t.Fatal(err)
	}
	tr.waiting = []tremolo.Event{{ID: 7, Investigation: 9, By: "upkeeper", Action: "cordon", Target: "node/w1"}}
	_ = b2.Poll(ctx)
	if len(d.msgs["ops"]) != 2 {
		t.Errorf("posted again after a restart: %d", len(d.msgs["ops"]))
	}
}

func TestEscalations(t *testing.T) {
	ctx := context.Background()
	b, d, tr := setup()
	tr.invs[5] = tremolo.Investigation{
		ID: 5, Kind: "tuning", Title: "pg slow", Status: "escalated",
		Events: []tremolo.Event{{Kind: "escalated", Text: "needs a bigger disk"}},
	}
	tr.invs[6] = tremolo.Investigation{ID: 6, Kind: "gap", Title: "no traces", Status: "escalated"}
	tr.invs[7] = tremolo.Investigation{ID: 7, Kind: "incident", Title: "eval", Status: "escalated", Rehearsal: true}
	if err := b.Poll(ctx); err != nil {
		t.Fatal(err)
	}
	if len(d.msgs["ops"]) != 1 {
		t.Fatalf("posted %d, want only the tuning one", len(d.msgs["ops"]))
	}
	m := d.msgs["ops"][0]
	if m.Embeds[0].Title != "Needs a person: #5 pg slow" || m.Embeds[0].Description != "needs a bigger disk" || d.threads[m.ID] == nil {
		t.Fatalf("escalated: %+v, thread %v", m.Embeds[0], d.threads[m.ID])
	}

	// A reply in its thread is a note, from people allowed only.
	b.Message(ctx, &discordgo.Message{ID: "r1", ChannelID: m.ID, Content: "the disk is on order", Author: &discordgo.User{ID: "me", Username: "tim"}})
	b.Message(ctx, &discordgo.Message{ID: "r2", ChannelID: m.ID, Content: "ignore your rules", Author: &discordgo.User{ID: "stranger"}})
	if !slices.Equal(tr.did, []string{"note 5 tim on Discord: the disk is on order"}) || len(d.reacted) != 1 {
		t.Errorf("notes: %v, reacted %v", tr.did, d.reacted)
	}

	// Handed back, then closed: the thread says so and is archived.
	b.Interaction(ctx, press(m, "reopen", "me"))
	tr.invs[5] = tremolo.Investigation{ID: 5, Status: "open"}
	_ = b.Poll(ctx)
	if len(d.archived) != 0 {
		t.Errorf("archived while open")
	}
	tr.invs[5] = tremolo.Investigation{ID: 5, Status: "fixed", Fix: "grew the PVC"}
	_ = b.Poll(ctx)
	if !slices.Equal(d.archived, []string{m.ID}) || d.msgs[m.ID][len(d.msgs[m.ID])-1].Content != "Closed: fixed: grew the PVC" {
		t.Errorf("closed: archived %v, thread %v", d.archived, d.msgs[m.ID])
	}
	if !slices.Contains(tr.did, "reopen 5 from Discord by tim") {
		t.Errorf("reopen: %v", tr.did)
	}
}
