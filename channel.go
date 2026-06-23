package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"sync/atomic"

	"github.com/fatih/color"
	"github.com/urfave/cli/v2"

	"github.com/nbd-wtf/go-nostr"
	"github.com/nbd-wtf/go-nostr/nip19"
	"github.com/nbd-wtf/go-nostr/sdk"
)

// channelMetadata is the JSON content shape for kind 40 / 41 events.
type channelMetadata struct {
	Name    string   `json:"name,omitempty"`
	About   string   `json:"about,omitempty"`
	Picture string   `json:"picture,omitempty"`
	Relays  []string `json:"relays,omitempty"`
}

// resolveChannelID turns nevent1.../note1.../hex into a hex event id.
func resolveChannelID(id string) (string, error) {
	if evp := sdk.InputToEventPointer(id); evp != nil {
		return evp.ID, nil
	}
	return "", fmt.Errorf("failed to parse channel id from '%s'", id)
}

// buildChannelCreateEvent constructs an unsigned kind 40 event for the given metadata.
func buildChannelCreateEvent(pubkey string, meta channelMetadata, createdAt nostr.Timestamp) (*nostr.Event, error) {
	if strings.TrimSpace(meta.Name) == "" {
		return nil, errors.New("channel name is empty")
	}
	b, err := json.Marshal(meta)
	if err != nil {
		return nil, err
	}
	ev := &nostr.Event{
		PubKey:    pubkey,
		CreatedAt: createdAt,
		Kind:      nostr.KindChannelCreation,
		Tags:      nostr.Tags{},
		Content:   string(b),
	}
	clientTag(ev)
	return ev, nil
}

// channelPostOpts captures everything buildChannelPostEvent needs.
type channelPostOpts struct {
	Content        string
	ChannelID      string
	ReplyID        string
	RelayHint      string
	MentionPubkeys []string // pre-resolved hex pubkeys
	Sensitive      string
	Geohash        string
	Emojis         []string // "name=url" pairs from --emoji
	Tags           []string // "name=v1;v2" pairs from --tag
}

// buildChannelPostEvent constructs an unsigned kind 42 event for a message in a channel.
// ChannelID must be the hex id of the kind 40 event. If ReplyID is non-empty it is added as
// a NIP-10 style "reply" e-tag. RelayHint is written into the e-tag(s) as the relay hint (may be "").
// MentionPubkeys get "p" tags and "nostr:<npub> " mentions prepended to the content (NIP-27).
// Links (r) and hashtags (t) found in content are auto-attached. cfgEmojis is the configured
// shortcode→icon map for inline :name: emoji expansion.
func buildChannelPostEvent(pubkey string, opts channelPostOpts, cfgEmojis map[string]string, createdAt nostr.Timestamp) (*nostr.Event, error) {
	if strings.TrimSpace(opts.Content) == "" {
		return nil, errors.New("content is empty")
	}
	if opts.ChannelID == "" {
		return nil, errors.New("channel id is empty")
	}
	ev := &nostr.Event{
		PubKey:    pubkey,
		CreatedAt: createdAt,
		Kind:      nostr.KindChannelMessage,
		Tags:      nostr.Tags{},
		Content:   opts.Content,
	}
	clientTag(ev)

	ev.Tags = ev.Tags.AppendUnique(nostr.Tag{"e", opts.ChannelID, opts.RelayHint, "root"})
	if opts.ReplyID != "" {
		ev.Tags = ev.Tags.AppendUnique(nostr.Tag{"e", opts.ReplyID, opts.RelayHint, "reply"})
	}

	var mentions []string
	for _, p := range opts.MentionPubkeys {
		npub, err := nip19.EncodePublicKey(p)
		if err != nil {
			return nil, err
		}
		mentions = append(mentions, "nostr:"+npub)
		ev.Tags = ev.Tags.AppendUnique(nostr.Tag{"p", p})
	}
	if len(mentions) > 0 {
		ev.Content = strings.Join(mentions, " ") + " " + ev.Content
	}

	for _, entry := range extractLinks(ev.Content) {
		ev.Tags = ev.Tags.AppendUnique(nostr.Tag{"r", entry.text})
	}
	for _, u := range opts.Emojis {
		tok := strings.SplitN(u, "=", 2)
		if len(tok) != 2 {
			return nil, usageError
		}
		ev.Tags = ev.Tags.AppendUnique(nostr.Tag{"emoji", tok[0], tok[1]})
	}
	for _, entry := range extractEmojis(ev.Content) {
		name := strings.Trim(entry.text, ":")
		if icon, ok := cfgEmojis[name]; ok {
			ev.Tags = ev.Tags.AppendUnique(nostr.Tag{"emoji", name, icon})
		}
	}
	if opts.Sensitive != "" {
		ev.Tags = ev.Tags.AppendUnique(nostr.Tag{"content-warning", opts.Sensitive})
	}
	if opts.Geohash != "" {
		ev.Tags = ev.Tags.AppendUnique(nostr.Tag{"g", opts.Geohash})
	}

	hashtag := nostr.Tag{"t"}
	for _, m := range extractTags(ev.Content) {
		hashtag = append(hashtag, m.text)
	}
	if len(hashtag) > 1 {
		ev.Tags = ev.Tags.AppendUnique(hashtag)
	}

	for _, t := range opts.Tags {
		name, value, found := strings.Cut(t, "=")
		tag := nostr.Tag{name}
		if found {
			tag = append(tag, strings.Split(value, ";")...)
		}
		if len(tag) == 0 {
			continue
		}
		if tag[0] == "client" {
			tags := make(nostr.Tags, 0, len(ev.Tags))
			for _, existing := range ev.Tags {
				if len(existing) > 0 && existing[0] == "client" {
					continue
				}
				tags = append(tags, existing)
			}
			ev.Tags = append(tags, tag)
		} else {
			ev.Tags = ev.Tags.AppendUnique(tag)
		}
	}
	return ev, nil
}

// firstWriteRelay returns one URL from cfg.Relays that has Write enabled, or "".
// Picking a deterministic one (lexicographically smallest) keeps test/runtime behavior reproducible.
func firstWriteRelay(cfg *Config) string {
	urls := []string{}
	for u, r := range cfg.Relays {
		if r.Write {
			urls = append(urls, u)
		}
	}
	if len(urls) == 0 {
		return ""
	}
	sort.Strings(urls)
	return urls[0]
}

func doChannelCreate(cCtx *cli.Context) error {
	name := cCtx.String("name")
	if strings.TrimSpace(name) == "" {
		return cli.ShowSubcommandHelp(cCtx)
	}
	cfg := cCtx.App.Metadata["config"].(*Config)

	sk, pub, err := getSkAndPub(cfg)
	if err != nil {
		return err
	}

	ev, err := buildChannelCreateEvent(pub, channelMetadata{
		Name:    name,
		About:   cCtx.String("about"),
		Picture: cCtx.String("picture"),
	}, nostr.Now())
	if err != nil {
		return err
	}
	if err := ev.Sign(sk); err != nil {
		return err
	}

	var success atomic.Int64
	cfg.Do(context.Background(), Relay{Write: true}, func(ctx context.Context, relay *nostr.Relay) bool {
		if err := relay.Publish(ctx, *ev); err != nil {
			fmt.Fprintln(os.Stderr, relay.URL, err)
		} else {
			success.Add(1)
		}
		return true
	})
	if success.Load() == 0 {
		return errors.New("cannot create channel")
	}

	if nev, err := nip19.EncodeEvent(ev.ID, nil, pub); err == nil {
		fmt.Println(nev)
	} else {
		fmt.Println(ev.ID)
	}
	return nil
}

func doChannelList(cCtx *cli.Context) error {
	j := cCtx.Bool("json")
	all := cCtx.Bool("all")

	cfg := cCtx.App.Metadata["config"].(*Config)

	_, pub, err := getSkAndPub(cfg)
	if err != nil {
		return err
	}

	filter := nostr.Filter{
		Kinds: []int{nostr.KindChannelCreation},
		Limit: cCtx.Int("n"),
	}
	if !all {
		filter.Authors = []string{pub}
	}

	evs, err := cfg.QueryEvents(context.Background(), nostr.Filters{filter})
	if err != nil {
		return err
	}

	sort.Slice(evs, func(i, k int) bool {
		return evs[i].CreatedAt > evs[k].CreatedAt
	})

	if j {
		for _, ev := range evs {
			json.NewEncoder(os.Stdout).Encode(ev)
		}
		return nil
	}

	for _, ev := range evs {
		var meta channelMetadata
		_ = json.Unmarshal([]byte(ev.Content), &meta)
		nev, _ := nip19.EncodeEvent(ev.ID, nil, ev.PubKey)
		color.Set(color.FgHiBlue)
		fmt.Print(nev)
		color.Set(color.Reset)
		fmt.Print(": ")
		color.Set(color.FgHiRed)
		fmt.Println(meta.Name)
		color.Set(color.Reset)
		if meta.About != "" {
			fmt.Println(meta.About)
		}
		fmt.Println()
	}
	return nil
}

func doChannelTimeline(cCtx *cli.Context) error {
	id := cCtx.String("id")
	n := cCtx.Int("n")
	j := cCtx.Bool("json")
	extra := cCtx.Bool("extra")

	channelID, err := resolveChannelID(id)
	if err != nil {
		return err
	}

	cfg := cCtx.App.Metadata["config"].(*Config)

	filter := nostr.Filter{
		Kinds: []int{nostr.KindChannelMessage},
		Tags:  nostr.TagMap{"e": []string{channelID}},
		Limit: n,
	}

	evs, err := cfg.QueryEvents(context.Background(), nostr.Filters{filter})
	if err != nil {
		return err
	}

	sort.Slice(evs, func(i, k int) bool {
		return evs[i].CreatedAt.Time().Before(evs[k].CreatedAt.Time())
	})
	if len(evs) > n {
		evs = evs[len(evs)-n:]
	}

	for _, ev := range evs {
		cfg.PrintEvent(ev, j, extra)
	}
	return nil
}

func doChannelStream(cCtx *cli.Context) error {
	id := cCtx.String("id")
	j := cCtx.Bool("json")

	channelID, err := resolveChannelID(id)
	if err != nil {
		return err
	}

	cfg := cCtx.App.Metadata["config"].(*Config)

	relays := []string{}
	for rurl, r := range cfg.Relays {
		if r.Read {
			relays = append(relays, rurl)
		}
	}
	if len(relays) == 0 {
		return errors.New("no read relays available")
	}

	since := nostr.Now()
	filter := nostr.Filter{
		Kinds: []int{nostr.KindChannelMessage},
		Tags:  nostr.TagMap{"e": []string{channelID}},
		Since: &since,
	}

	sub := cfg.pool.SubMany(context.Background(), relays, nostr.Filters{filter})
	for ie := range sub {
		if ie.Event == nil {
			continue
		}
		if j {
			json.NewEncoder(os.Stdout).Encode(ie.Event)
		} else {
			cfg.PrintEvent(ie.Event, false, false)
		}
	}
	return nil
}

func doChannelPost(cCtx *cli.Context) error {
	id := cCtx.String("id")
	reply := cCtx.String("reply")
	stdin := cCtx.Bool("stdin")
	if !stdin && cCtx.Args().Len() == 0 {
		return cli.ShowSubcommandHelp(cCtx)
	}

	createdAt := nostr.Timestamp(cCtx.Int64("created-at"))
	if createdAt == 0 {
		createdAt = nostr.Now()
	}

	channelID, err := resolveChannelID(id)
	if err != nil {
		return err
	}

	var replyID string
	if reply != "" {
		replyID, err = resolveChannelID(reply)
		if err != nil {
			return err
		}
	}

	cfg := cCtx.App.Metadata["config"].(*Config)

	sk, pub, err := getSkAndPub(cfg)
	if err != nil {
		return err
	}

	var content string
	if stdin {
		b, err := io.ReadAll(os.Stdin)
		if err != nil {
			return err
		}
		content = string(b)
	} else {
		content = strings.Join(cCtx.Args().Slice(), "\n")
	}

	mentionPubkeys, err := resolveMentions(context.TODO(), cCtx.StringSlice("u"))
	if err != nil {
		return err
	}

	ev, err := buildChannelPostEvent(pub, channelPostOpts{
		Content:        content,
		ChannelID:      channelID,
		ReplyID:        replyID,
		RelayHint:      firstWriteRelay(cfg),
		MentionPubkeys: mentionPubkeys,
		Sensitive:      cCtx.String("sensitive"),
		Geohash:        cCtx.String("geohash"),
		Emojis:         cCtx.StringSlice("emoji"),
		Tags:           cCtx.StringSlice("tag"),
	}, cfg.Emojis, createdAt)
	if err != nil {
		return err
	}
	if err := ev.Sign(sk); err != nil {
		return err
	}

	var success atomic.Int64
	cfg.Do(context.Background(), Relay{Write: true}, func(ctx context.Context, relay *nostr.Relay) bool {
		if err := relay.Publish(ctx, *ev); err != nil {
			fmt.Fprintln(os.Stderr, relay.URL, err)
		} else {
			success.Add(1)
		}
		return true
	})
	if success.Load() == 0 {
		return errors.New("cannot post to channel")
	}
	if cfg.verbose {
		if id, err := nip19.EncodeNote(ev.ID); err == nil {
			fmt.Println(id)
		}
	}
	return nil
}

// channelCommand returns the "channel" parent command with its subcommands (NIP-28).
func channelCommand() *cli.Command {
	return &cli.Command{
		Name:  "channel",
		Usage: "public chat channels (NIP-28)",
		Subcommands: []*cli.Command{
			{
				Name: "create",
				Flags: []cli.Flag{
					&cli.StringFlag{Name: "name", Required: true, Usage: "channel name"},
					&cli.StringFlag{Name: "about", Usage: "channel description"},
					&cli.StringFlag{Name: "picture", Usage: "channel picture URL"},
				},
				Usage:     "create a new channel (NIP-28 kind 40)",
				UsageText: "algia channel create --name [name] [--about ...] [--picture ...]",
				Action:    doChannelCreate,
			},
			{
				Name: "list",
				Flags: []cli.Flag{
					&cli.BoolFlag{Name: "json", Usage: "output JSON"},
					&cli.BoolFlag{Name: "all", Usage: "list channels from all authors, not just mine"},
					&cli.IntFlag{Name: "n", Value: 30, Usage: "number of items"},
				},
				Usage:     "list channels (NIP-28 kind 40)",
				UsageText: "algia channel list",
				Action:    doChannelList,
			},
			{
				Name: "timeline",
				Flags: []cli.Flag{
					&cli.StringFlag{Name: "id", Required: true, Usage: "channel id (note/nevent/hex of the kind 40 event)"},
					&cli.IntFlag{Name: "n", Value: 30, Usage: "number of items"},
					&cli.BoolFlag{Name: "json", Usage: "output JSON"},
					&cli.BoolFlag{Name: "extra", Usage: "extra JSON"},
				},
				Usage:     "show channel timeline (NIP-28 kind 42)",
				UsageText: "algia channel timeline --id [channel id]",
				Action:    doChannelTimeline,
			},
			{
				Name: "stream",
				Flags: []cli.Flag{
					&cli.StringFlag{Name: "id", Required: true, Usage: "channel id (note/nevent/hex of the kind 40 event)"},
					&cli.BoolFlag{Name: "json", Usage: "output JSON"},
				},
				Usage:     "stream new channel messages (NIP-28 kind 42)",
				UsageText: "algia channel stream --id [channel id]",
				Action:    doChannelStream,
			},
			{
				Name: "post",
				Flags: []cli.Flag{
					&cli.StringFlag{Name: "id", Required: true, Usage: "channel id (note/nevent/hex of the kind 40 event)"},
					&cli.StringFlag{Name: "reply", Usage: "reply target message id (note/nevent/hex of a kind 42 event)"},
					&cli.StringSliceFlag{Name: "u", Usage: "users to mention (npub/nprofile/hex/NIP-05)"},
					&cli.BoolFlag{Name: "stdin"},
					&cli.StringFlag{Name: "sensitive"},
					&cli.StringSliceFlag{Name: "emoji"},
					&cli.StringFlag{Name: "geohash"},
					&cli.StringSliceFlag{Name: "tag", Aliases: []string{"t"}, Usage: "tag (key=value1;value2)"},
					&cli.Int64Flag{Name: "created-at", Usage: "override created_at (unix timestamp)"},
				},
				Usage:     "post a message to a channel (NIP-28 kind 42)",
				UsageText: "algia channel post --id [channel id] [--reply <message id>] [-u <user>...] [message]",
				ArgsUsage: "[message]",
				Action:    doChannelPost,
			},
		},
	}
}
