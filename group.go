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
	// Capture the start time before authenticating so messages posted during
	// the (brief) pre-auth handshake are not missed once we subscribe.
	since := nostr.Now()
	cfg.preAuth(ctx, relays)

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
	images := cCtx.StringSlice("image")
	if strings.TrimSpace(id) == "" || (!stdin && cCtx.Args().Len() == 0 && len(images) == 0) {
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

	// Upload images (group posts default to the relay's own media store) and
	// fold their URLs into the content before building the event.
	bds, err := uploadImages(cfg, images, cCtx.StringSlice("server"), true)
	if err != nil {
		return err
	}
	content = appendImageURLs(content, bds)

	ev, err := buildGroupPostEvent(pub, id, content, nostr.Now())
	if err != nil {
		return err
	}
	addImetaTags(ev, bds)

	return cfg.publishGroupEvent(ev, "cannot post to group")
}

// buildGroupDeleteEvent constructs an unsigned kind 9005 event that asks the
// relay to delete a single message from a NIP-29 group. NIP-29 relays do not
// honor a bare NIP-09 kind 5 for group content; deletion goes through this
// moderation event, which the relay authorizes (the author for their own
// message, or a moderator). The group id is the "h" tag and the target message
// id is the "e" tag. A 9005 must reference exactly one target, so deleting
// several messages means publishing one event per target.
func buildGroupDeleteEvent(pubkey, groupID, targetID string, createdAt nostr.Timestamp) (*nostr.Event, error) {
	if strings.TrimSpace(groupID) == "" {
		return nil, errors.New("group id is empty")
	}
	if strings.TrimSpace(targetID) == "" {
		return nil, errors.New("target id is empty")
	}
	ev := &nostr.Event{
		PubKey:    pubkey,
		CreatedAt: createdAt,
		Kind:      nostr.KindSimpleGroupDeleteEvent,
		Tags: nostr.Tags{
			nostr.Tag{"h", groupID},
			nostr.Tag{"e", targetID},
		},
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

	relays := writeRelays(cfg)
	if len(relays) == 0 {
		return errors.New("no write relays available")
	}

	ctx := context.Background()
	cfg.preAuth(ctx, relays)

	// A 9005 event references exactly one target, so publish one per message.
	var failed int
	for _, targetID := range targetIDs {
		ev, err := buildGroupDeleteEvent(pub, id, targetID, nostr.Now())
		if err != nil {
			return err
		}
		if err := cfg.signEvent(ev); err != nil {
			return err
		}
		var ok bool
		for res := range cfg.pool.PublishMany(ctx, relays, *ev) {
			if res.Error != nil {
				fmt.Fprintln(os.Stderr, res.RelayURL, targetID, res.Error)
			} else {
				ok = true
			}
		}
		if ok {
			if cfg.verbose {
				fmt.Println(ev.ID)
			}
		} else {
			failed++
		}
	}
	if failed > 0 {
		return fmt.Errorf("failed to delete %d of %d message(s)", failed, len(targetIDs))
	}
	return nil
}

// buildGroupReactEvent constructs an unsigned kind 7 reaction to a message in a
// NIP-29 group. Like the message itself, it carries the group "h" tag so the
// relay associates it with the group; the target message is the "e" tag.
func buildGroupReactEvent(pubkey, groupID, targetID, content, emoji string, createdAt nostr.Timestamp) (*nostr.Event, error) {
	if strings.TrimSpace(groupID) == "" {
		return nil, errors.New("group id is empty")
	}
	if strings.TrimSpace(targetID) == "" {
		return nil, errors.New("target id is empty")
	}
	ev := &nostr.Event{
		PubKey:    pubkey,
		CreatedAt: createdAt,
		Kind:      nostr.KindReaction,
		Tags: nostr.Tags{
			nostr.Tag{"h", groupID},
			nostr.Tag{"e", targetID},
		},
		Content: content,
	}
	if emoji != "" {
		if ev.Content == "" {
			ev.Content = "like"
		}
		ev.Tags = ev.Tags.AppendUnique(nostr.Tag{"emoji", ev.Content, emoji})
		ev.Content = ":" + ev.Content + ":"
	}
	if ev.Content == "" {
		ev.Content = "+"
	}
	return ev, nil
}

func doGroupReact(cCtx *cli.Context) error {
	id := cCtx.String("id")
	target := cCtx.String("target")
	if strings.TrimSpace(id) == "" || strings.TrimSpace(target) == "" {
		return cli.ShowSubcommandHelp(cCtx)
	}

	cfg := cCtx.App.Metadata["config"].(*Config)

	_, pub, err := getSkAndPub(cfg)
	if err != nil {
		return err
	}

	evp := sdk.InputToEventPointer(target)
	if evp == nil {
		return fmt.Errorf("failed to parse event id from '%s'", target)
	}

	ev, err := buildGroupReactEvent(pub, id, evp.ID, cCtx.String("content"), cCtx.String("emoji"), nostr.Now())
	if err != nil {
		return err
	}
	return cfg.publishGroupEvent(ev, "cannot react to group message")
}

// buildGroupJoinEvent constructs an unsigned kind 9021 join request for a
// NIP-29 group. An open group admits the sender immediately; a closed group
// queues the request for a moderator. An optional invite code goes into a
// "code" tag.
func buildGroupJoinEvent(pubkey, groupID, code string, createdAt nostr.Timestamp) (*nostr.Event, error) {
	if strings.TrimSpace(groupID) == "" {
		return nil, errors.New("group id is empty")
	}
	ev := &nostr.Event{
		PubKey:    pubkey,
		CreatedAt: createdAt,
		Kind:      nostr.KindSimpleGroupJoinRequest,
		Tags:      nostr.Tags{nostr.Tag{"h", groupID}},
	}
	if strings.TrimSpace(code) != "" {
		ev.Tags = append(ev.Tags, nostr.Tag{"code", code})
	}
	return ev, nil
}

// buildGroupLeaveEvent constructs an unsigned kind 9022 leave request.
func buildGroupLeaveEvent(pubkey, groupID string, createdAt nostr.Timestamp) (*nostr.Event, error) {
	if strings.TrimSpace(groupID) == "" {
		return nil, errors.New("group id is empty")
	}
	return &nostr.Event{
		PubKey:    pubkey,
		CreatedAt: createdAt,
		Kind:      nostr.KindSimpleGroupLeaveRequest,
		Tags:      nostr.Tags{nostr.Tag{"h", groupID}},
	}, nil
}

// publishGroupEvent signs ev and publishes it to the write relays, pre-authing
// first so NIP-29 relays that require auth accept it. Returns an error if no
// relay accepted the event. failMsg names the operation for the error.
func (cfg *Config) publishGroupEvent(ev *nostr.Event, failMsg string) error {
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
		return errors.New(failMsg)
	}
	if cfg.verbose {
		fmt.Println(ev.ID)
	}
	return nil
}

func doGroupJoin(cCtx *cli.Context) error {
	id := cCtx.String("id")
	if strings.TrimSpace(id) == "" {
		return cli.ShowSubcommandHelp(cCtx)
	}
	cfg := cCtx.App.Metadata["config"].(*Config)
	_, pub, err := getSkAndPub(cfg)
	if err != nil {
		return err
	}
	ev, err := buildGroupJoinEvent(pub, id, cCtx.String("code"), nostr.Now())
	if err != nil {
		return err
	}
	return cfg.publishGroupEvent(ev, "cannot join group")
}

func doGroupLeave(cCtx *cli.Context) error {
	id := cCtx.String("id")
	if strings.TrimSpace(id) == "" {
		return cli.ShowSubcommandHelp(cCtx)
	}
	cfg := cCtx.App.Metadata["config"].(*Config)
	_, pub, err := getSkAndPub(cfg)
	if err != nil {
		return err
	}
	ev, err := buildGroupLeaveEvent(pub, id, nostr.Now())
	if err != nil {
		return err
	}
	return cfg.publishGroupEvent(ev, "cannot leave group")
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
					&cli.StringSliceFlag{Name: "image", Aliases: []string{"i"}, Usage: "image file(s) to upload and attach (repeatable)"},
					&cli.StringSliceFlag{Name: "server", Aliases: []string{"s"}, Usage: "media server override (default: the group relay's media store)"},
				},
				Usage:     "post a message to a group (NIP-29 kind 9)",
				UsageText: "algia group post --id [group id] [-i <image>...] [message]",
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
			{
				Name: "react",
				Flags: []cli.Flag{
					&cli.StringFlag{Name: "id", Required: true, Usage: "group id (the h-tag value)"},
					&cli.StringFlag{Name: "target", Required: true, Usage: "target message id (note/nevent/hex)"},
					&cli.StringFlag{Name: "content", Usage: "reaction content (default: +)"},
					&cli.StringFlag{Name: "emoji", Usage: "custom emoji URL (NIP-30)"},
				},
				Usage:     "react to a message in a group (NIP-29 kind 7)",
				UsageText: "algia group react --id [group id] --target [message id] [--content +]",
				Action:    doGroupReact,
			},
			{
				Name: "join",
				Flags: []cli.Flag{
					&cli.StringFlag{Name: "id", Required: true, Usage: "group id (the h-tag value)"},
					&cli.StringFlag{Name: "code", Usage: "invite code (for closed groups)"},
				},
				Usage:     "request to join a group (NIP-29 kind 9021)",
				UsageText: "algia group join --id [group id] [--code <invite>]",
				Action:    doGroupJoin,
			},
			{
				Name: "leave",
				Flags: []cli.Flag{
					&cli.StringFlag{Name: "id", Required: true, Usage: "group id (the h-tag value)"},
				},
				Usage:     "leave a group (NIP-29 kind 9022)",
				UsageText: "algia group leave --id [group id]",
				Action:    doGroupLeave,
			},
		},
	}
}
