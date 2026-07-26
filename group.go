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

	"github.com/fatih/color"
	"github.com/urfave/cli/v2"

	"github.com/nbd-wtf/go-nostr"
	"github.com/nbd-wtf/go-nostr/sdk"
)

// groupInfo is the parsed form of a NIP-29 group metadata event (kind 39000).
// The group id is the event's "d" tag and is what clients pass around as the
// "h" tag on messages.
type groupInfo struct {
	ID      string `json:"id"`
	Name    string `json:"name,omitempty"`
	About   string `json:"about,omitempty"`
	Type    string `json:"type,omitempty"` // "t" tag, e.g. "stream" or "dm"
	Private bool   `json:"private"`
	Closed  bool   `json:"closed"`
}

// parseGroupMetadata reads a kind 39000 event into a groupInfo.
func parseGroupMetadata(ev *nostr.Event) groupInfo {
	g := groupInfo{}
	for _, tag := range ev.Tags {
		if len(tag) == 0 {
			continue
		}
		switch tag[0] {
		case "d":
			if len(tag) >= 2 {
				g.ID = tag[1]
			}
		case "name":
			if len(tag) >= 2 {
				g.Name = tag[1]
			}
		case "about":
			if len(tag) >= 2 {
				g.About = tag[1]
			}
		case "t":
			if len(tag) >= 2 {
				g.Type = tag[1]
			}
		case "private":
			g.Private = true
		case "closed":
			g.Closed = true
		}
	}
	return g
}

// writeRelays returns the configured relays that have Write enabled, sorted for
// reproducibility. NIP-29 groups live on a specific relay, so posting is aimed
// at the write relays (typically the group's host relay).
func writeRelays(cfg *Config) []string {
	urls := []string{}
	for u, r := range cfg.Relays {
		if r.Write {
			urls = append(urls, u)
		}
	}
	sort.Strings(urls)
	return urls
}

func doGroupList(cCtx *cli.Context) error {
	j := cCtx.Bool("json")
	cfg := cCtx.App.Metadata["config"].(*Config)

	evs, err := cfg.QueryEvents(context.Background(), nostr.Filters{{
		Kinds: []int{nostr.KindSimpleGroupMetadata},
		Limit: cCtx.Int("n"),
	}})
	if err != nil {
		return err
	}

	sort.Slice(evs, func(i, k int) bool {
		return evs[i].CreatedAt > evs[k].CreatedAt
	})

	if j {
		for _, ev := range evs {
			json.NewEncoder(os.Stdout).Encode(parseGroupMetadata(ev))
		}
		return nil
	}

	for _, ev := range evs {
		g := parseGroupMetadata(ev)
		color.Set(color.FgHiBlue)
		fmt.Print(g.ID)
		color.Set(color.Reset)
		fmt.Print(": ")
		color.Set(color.FgHiRed)
		fmt.Print(g.Name)
		color.Set(color.Reset)
		if g.Private {
			color.Set(color.FgHiBlack)
			fmt.Print(" (private)")
			color.Set(color.Reset)
		}
		fmt.Println()
		if g.About != "" {
			fmt.Println(g.About)
		}
		fmt.Println()
	}
	return nil
}

func doGroupTimeline(cCtx *cli.Context) error {
	id := cCtx.String("id")
	n := cCtx.Int("n")
	j := cCtx.Bool("json")
	extra := cCtx.Bool("extra")

	if strings.TrimSpace(id) == "" {
		return cli.ShowSubcommandHelp(cCtx)
	}

	cfg := cCtx.App.Metadata["config"].(*Config)

	evs, err := cfg.QueryEvents(context.Background(), nostr.Filters{{
		Kinds: []int{nostr.KindSimpleGroupChatMessage},
		Tags:  nostr.TagMap{"h": []string{id}},
		Limit: n,
	}})
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

func doGroupStream(cCtx *cli.Context) error {
	id := cCtx.String("id")
	j := cCtx.Bool("json")

	if strings.TrimSpace(id) == "" {
		return cli.ShowSubcommandHelp(cCtx)
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

	ctx := context.Background()
	cfg.preAuth(ctx, relays)

	since := nostr.Now()
	sub := cfg.pool.SubMany(ctx, relays, nostr.Filters{{
		Kinds: []int{nostr.KindSimpleGroupChatMessage},
		Tags:  nostr.TagMap{"h": []string{id}},
		Since: &since,
	}})
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

// buildGroupPostEvent constructs an unsigned kind 9 message for a NIP-29 group.
// The group id goes into the "h" tag; links and hashtags in the content are
// auto-attached like the other post builders.
func buildGroupPostEvent(pubkey, groupID, content string, createdAt nostr.Timestamp) (*nostr.Event, error) {
	if strings.TrimSpace(content) == "" {
		return nil, errors.New("content is empty")
	}
	if strings.TrimSpace(groupID) == "" {
		return nil, errors.New("group id is empty")
	}
	ev := &nostr.Event{
		PubKey:    pubkey,
		CreatedAt: createdAt,
		Kind:      nostr.KindSimpleGroupChatMessage,
		Tags:      nostr.Tags{nostr.Tag{"h", groupID}},
		Content:   content,
	}
	clientTag(ev)

	for _, entry := range extractLinks(ev.Content) {
		ev.Tags = ev.Tags.AppendUnique(nostr.Tag{"r", entry.text})
	}
	hashtag := nostr.Tag{"t"}
	for _, m := range extractTags(ev.Content) {
		hashtag = append(hashtag, m.text)
	}
	if len(hashtag) > 1 {
		ev.Tags = ev.Tags.AppendUnique(hashtag)
	}
	return ev, nil
}

func doGroupPost(cCtx *cli.Context) error {
	id := cCtx.String("id")
	stdin := cCtx.Bool("stdin")
	if strings.TrimSpace(id) == "" || (!stdin && cCtx.Args().Len() == 0) {
		return cli.ShowSubcommandHelp(cCtx)
	}

	cfg := cCtx.App.Metadata["config"].(*Config)

	_, pub, err := getSkAndPub(cfg)
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

	ev, err := buildGroupPostEvent(pub, id, content, nostr.Now())
	if err != nil {
		return err
	}
	if err := cfg.signEvent(ev); err != nil {
		return err
	}

	relays := writeRelays(cfg)
	if len(relays) == 0 {
		return errors.New("no write relays available")
	}

	ctx := context.Background()
	// NIP-29 relays require NIP-42 auth to accept messages; authenticate the
	// write relays up front. PublishMany also re-auths reactively as a fallback.
	cfg.preAuth(ctx, relays)

	var success int
	for res := range cfg.pool.PublishMany(ctx, relays, *ev) {
		if res.Error != nil {
			fmt.Fprintln(os.Stderr, res.RelayURL, res.Error)
		} else {
			success++
		}
	}
	if success == 0 {
		return errors.New("cannot post to group")
	}
	if cfg.verbose {
		fmt.Println(ev.ID)
	}
	return nil
}

// buildGroupDeleteEvent constructs an unsigned kind 9005 event that asks the
// relay to delete one or more messages from a NIP-29 group. NIP-29 relays do
// not honor a bare NIP-09 kind 5 for group content; deletion goes through this
// moderation event, which the relay authorizes (the author for their own
// message, or a moderator). The group id is the "h" tag and each target message
// id is an "e" tag.
func buildGroupDeleteEvent(pubkey, groupID string, targetIDs []string, createdAt nostr.Timestamp) (*nostr.Event, error) {
	if strings.TrimSpace(groupID) == "" {
		return nil, errors.New("group id is empty")
	}
	if len(targetIDs) == 0 {
		return nil, errors.New("no target event id")
	}
	ev := &nostr.Event{
		PubKey:    pubkey,
		CreatedAt: createdAt,
		Kind:      nostr.KindSimpleGroupDeleteEvent,
		Tags:      nostr.Tags{nostr.Tag{"h", groupID}},
	}
	for _, id := range targetIDs {
		ev.Tags = append(ev.Tags, nostr.Tag{"e", id})
	}
	return ev, nil
}

func doGroupDelete(cCtx *cli.Context) error {
	id := cCtx.String("id")
	if strings.TrimSpace(id) == "" || cCtx.Args().Len() == 0 {
		return cli.ShowSubcommandHelp(cCtx)
	}

	cfg := cCtx.App.Metadata["config"].(*Config)

	_, pub, err := getSkAndPub(cfg)
	if err != nil {
		return err
	}

	var targetIDs []string
	for _, arg := range cCtx.Args().Slice() {
		evp := sdk.InputToEventPointer(arg)
		if evp == nil {
			return fmt.Errorf("failed to parse event id from '%s'", arg)
		}
		targetIDs = append(targetIDs, evp.ID)
	}

	ev, err := buildGroupDeleteEvent(pub, id, targetIDs, nostr.Now())
	if err != nil {
		return err
	}
	if err := cfg.signEvent(ev); err != nil {
		return err
	}

	relays := writeRelays(cfg)
	if len(relays) == 0 {
		return errors.New("no write relays available")
	}

	ctx := context.Background()
	cfg.preAuth(ctx, relays)

	var success int
	for res := range cfg.pool.PublishMany(ctx, relays, *ev) {
		if res.Error != nil {
			fmt.Fprintln(os.Stderr, res.RelayURL, res.Error)
		} else {
			success++
		}
	}
	if success == 0 {
		return errors.New("cannot delete group message")
	}
	if cfg.verbose {
		fmt.Println(ev.ID)
	}
	return nil
}

// groupCommand returns the "group" parent command with its subcommands (NIP-29).
func groupCommand() *cli.Command {
	return &cli.Command{
		Name:  "group",
		Usage: "relay-based groups / channels (NIP-29)",
		Action: func(cCtx *cli.Context) error {
			return cli.ShowSubcommandHelp(cCtx)
		},
		Subcommands: []*cli.Command{
			{
				Name: "list",
				Flags: []cli.Flag{
					&cli.BoolFlag{Name: "json", Usage: "output JSON"},
					&cli.IntFlag{Name: "n", Value: 100, Usage: "number of items"},
				},
				Usage:     "list groups (NIP-29 kind 39000)",
				UsageText: "algia group list",
				Action:    doGroupList,
			},
			{
				Name: "timeline",
				Flags: []cli.Flag{
					&cli.StringFlag{Name: "id", Required: true, Usage: "group id (the h-tag value)"},
					&cli.IntFlag{Name: "n", Value: 30, Usage: "number of items"},
					&cli.BoolFlag{Name: "json", Usage: "output JSON"},
					&cli.BoolFlag{Name: "extra", Usage: "extra JSON"},
				},
				Usage:     "show group timeline (NIP-29 kind 9)",
				UsageText: "algia group timeline --id [group id]",
				Action:    doGroupTimeline,
			},
			{
				Name: "stream",
				Flags: []cli.Flag{
					&cli.StringFlag{Name: "id", Required: true, Usage: "group id (the h-tag value)"},
					&cli.BoolFlag{Name: "json", Usage: "output JSON"},
				},
				Usage:     "stream new group messages (NIP-29 kind 9)",
				UsageText: "algia group stream --id [group id]",
				Action:    doGroupStream,
			},
			{
				Name: "post",
				Flags: []cli.Flag{
					&cli.StringFlag{Name: "id", Required: true, Usage: "group id (the h-tag value)"},
					&cli.BoolFlag{Name: "stdin"},
				},
				Usage:     "post a message to a group (NIP-29 kind 9)",
				UsageText: "algia group post --id [group id] [message]",
				ArgsUsage: "[message]",
				Action:    doGroupPost,
			},
			{
				Name: "delete",
				Flags: []cli.Flag{
					&cli.StringFlag{Name: "id", Required: true, Usage: "group id (the h-tag value)"},
				},
				Usage:     "delete message(s) from a group (NIP-29 kind 9005)",
				UsageText: "algia group delete --id [group id] <event id> [event id...]",
				ArgsUsage: "<event id> [event id...]",
				Action:    doGroupDelete,
			},
		},
	}
}
