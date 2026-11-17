// tremolo-discord is tremolo's Discord bot: alerts, approvals and escalated
// investigations in a channel, with buttons and threads. It connects out to
// Discord's Gateway, so it needs no public address; tremolo sends it
// notifications on /notify.
package main

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/bwmarrin/discordgo"
	"sigs.k8s.io/yaml"

	"github.com/tpural/tremolo-discord/internal/bot"
	"github.com/tpural/tremolo-discord/internal/tremolo"
)

var version = "dev"

// Config is the bot's configuration. Tokens come from the environment:
// DISCORD_TOKEN, TREMOLO_TOKEN (a person's, not an assistant's) and
// WEBHOOK_TOKEN (what tremolo's webhook channel sends).
type Config struct {
	Listen  string `json:"listen"`
	Tremolo struct {
		URL string `json:"url"`
		// UI is where links point, when tremolo's own URL isn't one people open.
		UI string `json:"ui"`
	} `json:"tremolo"`
	Discord struct {
		// Guild is the server's ID; empty is the only one the bot is in.
		Guild string `json:"guild"`
		// Channel is the channel's name or ID.
		Channel string `json:"channel"`
		// Allow is who may use the buttons and commands, by Discord user ID;
		// empty is the server's owner.
		Allow []string `json:"allow"`
	} `json:"discord"`
	Every string `json:"every"`
}

func main() {
	path := flag.String("config", "/etc/tremolo-discord/config.yaml", "config file")
	flag.Parse()
	log := slog.New(slog.NewJSONHandler(os.Stderr, nil))
	if err := run(*path, log); err != nil {
		log.Error("tremolo-discord", "err", err)
		os.Exit(1)
	}
}

func run(path string, log *slog.Logger) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var cfg Config
	if err := yaml.UnmarshalStrict(b, &cfg); err != nil {
		return err
	}
	if cfg.Listen == "" {
		cfg.Listen = ":8080"
	}
	if cfg.Discord.Channel == "" {
		cfg.Discord.Channel = "lab-ops"
	}
	every := 15 * time.Second
	if cfg.Every != "" {
		if every, err = time.ParseDuration(cfg.Every); err != nil {
			return fmt.Errorf("every: %w", err)
		}
	}
	if cfg.Tremolo.URL == "" {
		return errors.New("tremolo.url is required")
	}
	if cfg.Tremolo.UI == "" {
		cfg.Tremolo.UI = cfg.Tremolo.URL
	}
	env := map[string]string{}
	for _, k := range []string{"DISCORD_TOKEN", "TREMOLO_TOKEN", "WEBHOOK_TOKEN"} {
		if env[k] = os.Getenv(k); env[k] == "" {
			return fmt.Errorf("$%s is required", k)
		}
	}

	dg, err := discordgo.New("Bot " + env["DISCORD_TOKEN"])
	if err != nil {
		return err
	}
	dg.Identify.Intents = discordgo.IntentsGuilds | discordgo.IntentsGuildMessages | discordgo.IntentsMessageContent
	t := tremolo.New(cfg.Tremolo.URL, env["TREMOLO_TOKEN"])
	bt := &bot.Bot{D: dg, T: t, URL: strings.TrimRight(cfg.Tremolo.UI, "/"), Log: log}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	ready := make(chan error, 1)
	dg.AddHandler(func(s *discordgo.Session, r *discordgo.Ready) {
		// Ready comes again after a reconnect; only the first is waited on.
		select {
		case ready <- setUp(s, r, &cfg, bt, log):
		default:
		}
	})
	dg.AddHandler(func(_ *discordgo.Session, i *discordgo.InteractionCreate) {
		if bt.Channel != "" {
			bt.Interaction(ctx, i.Interaction)
		}
	})
	dg.AddHandler(func(_ *discordgo.Session, m *discordgo.MessageCreate) {
		if bt.Channel != "" {
			bt.Message(ctx, m.Message)
		}
	})
	if err := dg.Open(); err != nil {
		return fmt.Errorf("connecting to Discord: %w", err)
	}
	defer dg.Close()
	select {
	case err := <-ready:
		if err != nil {
			return err
		}
	case <-time.After(time.Minute):
		return errors.New("no ready from Discord in a minute")
	case <-ctx.Done():
		return nil
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok\n")) })
	mux.HandleFunc("POST /notify", func(w http.ResponseWriter, r *http.Request) {
		got := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if subtle.ConstantTimeCompare([]byte(got), []byte(env["WEBHOOK_TOKEN"])) != 1 {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		var n bot.Notification
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&n); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if err := bt.Notify(n); err != nil {
			log.Error("posting a notification", "event", n.Event, "rule", n.Rule, "err", err)
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	srv := &http.Server{Addr: cfg.Listen, Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	go func() {
		<-ctx.Done()
		shut, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = srv.Shutdown(shut)
	}()
	go func() {
		tick := time.NewTicker(every)
		defer tick.Stop()
		for {
			if err := bt.Poll(ctx); err != nil && ctx.Err() == nil {
				log.Warn("polling tremolo", "err", err)
			}
			select {
			case <-ctx.Done():
				return
			case <-tick.C:
			}
		}
	}()
	log.Info("tremolo-discord", "version", version, "listen", cfg.Listen, "channel", bt.Channel)
	if err := srv.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// setUp finds the guild and channel, registers the commands, decides who's
// allowed, and reads what was posted before.
func setUp(s *discordgo.Session, r *discordgo.Ready, cfg *Config, bt *bot.Bot, log *slog.Logger) error {
	guild := cfg.Discord.Guild
	if guild == "" {
		if len(r.Guilds) != 1 {
			return fmt.Errorf("the bot is in %d servers; set discord.guild", len(r.Guilds))
		}
		guild = r.Guilds[0].ID
	}
	chans, err := s.GuildChannels(guild)
	if err != nil {
		return err
	}
	for _, c := range chans {
		if c.ID == cfg.Discord.Channel || c.Name == strings.TrimPrefix(cfg.Discord.Channel, "#") {
			bt.Channel = c.ID
		}
	}
	if bt.Channel == "" {
		return fmt.Errorf("no channel %q in the server", cfg.Discord.Channel)
	}
	allow := cfg.Discord.Allow
	if len(allow) == 0 {
		g, err := s.Guild(guild)
		if err != nil {
			return err
		}
		allow = []string{g.OwnerID}
	}
	bt.Allowed = func(id string) bool { return slices.Contains(allow, id) }
	if _, err := s.ApplicationCommandBulkOverwrite(r.User.ID, guild, bot.Commands); err != nil {
		return fmt.Errorf("registering commands: %w", err)
	}
	if err := bt.Rebuild(); err != nil {
		return fmt.Errorf("reading the channel: %w", err)
	}
	log.Info("ready", "user", r.User.Username, "guild", guild, "channel", bt.Channel, "allowed", len(allow))
	return nil
}
