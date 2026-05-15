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

// buildChannelPostEvent constructs an unsigned kind 42 event for a message in a channel.
// channelID must be the hex id of the kind 40 event. If replyID is non-empty it is added as
// a NIP-10 style "reply" e-tag. relayHint is written into the e-tag(s) as the relay hint (may be "").
// Links (r) and hashtags (t) found in content are auto-attached.
func buildChannelPostEvent(pubkey, content, channelID, replyID, relayHint string, createdAt nostr.Timestamp) (*nostr.Event, error) {
	if strings.TrimSpace(content) == "" {
		return nil, errors.New("content is empty")
	}
	if channelID == "" {
		return nil, errors.New("channel id is empty")
	}
	ev := &nostr.Event{
		PubKey:    pubkey,
		CreatedAt: createdAt,
		Kind:      nostr.KindChannelMessage,
		Tags:      nostr.Tags{},
		Content:   content,
	}
	clientTag(ev)

	ev.Tags = ev.Tags.AppendUnique(nostr.Tag{"e", channelID, relayHint, "root"})
	if replyID != "" {
		ev.Tags = ev.Tags.AppendUnique(nostr.Tag{"e", replyID, relayHint, "reply"})
	}

	for _, entry := range extractLinks(content) {
		ev.Tags = ev.Tags.AppendUnique(nostr.Tag{"r", entry.text})
	}
	hashtag := nostr.Tag{"t"}
	for _, m := range extractTags(content) {
		hashtag = append(hashtag, m.text)
	}
	if len(hashtag) > 1 {
		ev.Tags = ev.Tags.AppendUnique(hashtag)
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

func doChannelPost(cCtx *cli.Context) error {
	id := cCtx.String("id")
	reply := cCtx.String("reply")
	stdin := cCtx.Bool("stdin")
	if !stdin && cCtx.Args().Len() == 0 {
		return cli.ShowSubcommandHelp(cCtx)
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

	ev, err := buildChannelPostEvent(pub, content, channelID, replyID, firstWriteRelay(cfg), nostr.Now())
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
