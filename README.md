# tremolo-discord

A Discord bot for [tremolo](https://github.com/tpural/tremolo): alerts, agents' actions and escalated
investigations in one channel, with buttons.

- **Alerts** come in with **Acknowledge** and **Silence 1h**. Repeats update the message; resolving
  turns it green.
- **Actions awaiting approval** (tremolo's `agents.approve`) come in with **Approve** and **Deny**. A
  decision made in tremolo's UI settles the message too.
- **Escalated investigations** come in with **Hand back to upkeeper** and **Abandon**, and a thread.
  Replies in the thread are added to the investigation as notes, for agents and people to read. When it
  closes, the thread says how and is archived. Gaps aren't posted; they wait in the daily report.
- **Commands:** `/status` (what's firing, what agents are doing and waiting on), `/pause why`, `/resume`.
- Other announcements (pause notices, the daily report, the weekly digest) are posted as they come.

Only `allow`ed Discord users (the server's owner, by default) can use the buttons and commands, or add
notes. Everyone else is told no.

It connects out to Discord's Gateway, so it needs no public address. It keeps no state: each message
carries what it's about in its footer, and on start it reads the channel's recent messages.

## Setting up Discord

1. In the [Developer Portal](https://discord.com/developers/applications), make an application, and under
   **Bot** turn on the **Message Content** intent (to read thread replies). Copy the bot's token.
2. Invite it with the `bot` and `applications.commands` scopes and these permissions: View Channels,
   Send Messages, Embed Links, Read Message History, Create Public Threads, Send Messages in Threads,
   Manage Threads, Add Reactions. That's `permissions=326417599552`:

   `https://discord.com/oauth2/authorize?client_id=<application id>&scope=bot+applications.commands&permissions=326417599552`

## Configuration

```yaml
listen: ":8080"
tremolo:
  url: http://tremolo.tremolo.svc     # its API
  ui: https://tremolo.example.com     # where links point; defaults to url
discord:
  guild: ""                           # the server's ID; empty when the bot is in just one
  channel: lab-ops                    # name or ID
  allow: []                           # Discord user IDs; empty is the server's owner
every: 15s                            # how often to look for actions and escalations
```

From the environment:

- `DISCORD_TOKEN`: the bot's token.
- `TREMOLO_TOKEN`: a tremolo API token **without** a scope, so it acts as you: approvals must come
  from a person, and an assistant token can't approve.
- `WEBHOOK_TOKEN`: a secret tremolo sends to `/notify`.

In tremolo, add a webhook channel for it, and send it what should reach Discord:

```yaml
config:
  notify:
    page: [phone, discord]
    warn: [phone, discord]
  channels:
    discord:
      type: webhook
      url: http://tremolo-discord.tremolo-discord.svc:8080/notify
      token: ${WEBHOOK_TOKEN}
agents:
  notify: [discord]                   # pause notices and the daily report
  approve:
    - name: nodes
      actions: [cordon, uncordon]
```
