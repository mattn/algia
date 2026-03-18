package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync/atomic"

	"github.com/urfave/cli/v2"

	"github.com/nbd-wtf/go-nostr"
	"github.com/nbd-wtf/go-nostr/nip19"
	"github.com/nbd-wtf/go-nostr/sdk"
)

// standardListKinds are NIP-51 standard lists (one per kind per user, no d tag).
var standardListKinds = []int{
	10000, // Mute list
	10001, // Pinned notes
	10002, // Read/write relays (NIP-65)
	10003, // Bookmarks
	10004, // Communities
	10005, // Public chats
	10006, // Blocked relays
	10007, // Search relays
	10009, // Simple groups
	10015, // Interests
	10030, // Emojis
	10050, // DM relays
}

// setKinds are NIP-51 sets (multiple per kind, identified by d tag).
var setKinds = []int{
	30000, // Follow sets
	30002, // Relay sets
	30003, // Bookmark sets
	30004, // Curation sets (articles)
	30005, // Curation sets (videos)
	30006, // Curation sets (pictures)
	30015, // Interest sets
	30030, // Emoji sets
}

func kindLabel(kind int) string {
	switch kind {
	case 10000:
		return "mute"
	case 10001:
		return "pin"
	case 10002:
		return "relay"
	case 10003:
		return "bookmark"
	case 10004:
		return "community"
	case 10005:
		return "public-chat"
	case 10006:
		return "blocked-relay"
	case 10007:
		return "search-relay"
	case 10009:
		return "simple-group"
	case 10015:
		return "interest"
	case 10030:
		return "emoji"
	case 10050:
		return "dm-relay"
	case 30000:
		return "follow-set"
	case 30002:
		return "relay-set"
	case 30003:
		return "bookmark-set"
	case 30004:
		return "curation-set(article)"
	case 30005:
		return "curation-set(video)"
	case 30006:
		return "curation-set(picture)"
	case 30015:
		return "interest-set"
	case 30030:
		return "emoji-set"
	default:
		return fmt.Sprintf("kind:%d", kind)
	}
}

// isStandardList returns true for NIP-51 standard list kinds (no d tag).
func isStandardList(kind int) bool {
	return kind >= 10000 && kind < 20000
}

func getSkAndPub(cfg *Config) (string, string, error) {
	var sk string
	if _, s, err := nip19.Decode(cfg.PrivateKey); err == nil {
		sk = s.(string)
	} else {
		return "", "", err
	}
	pub, err := nostr.GetPublicKey(sk)
	if err != nil {
		return "", "", err
	}
	return sk, pub, nil
}

// resolveKindAndName resolves the kind and name from --kind flag and positional args.
// For standard lists (kind 10000-19999), name is not used.
// For sets (kind >= 30000), the first positional arg is the name.
func resolveKindAndName(cCtx *cli.Context) (kind int, name string, args []string) {
	kind = cCtx.Int("kind")
	allArgs := cCtx.Args().Slice()

	if kind == 0 {
		kind = 30000
	}

	if isStandardList(kind) {
		return kind, "", allArgs
	}

	if len(allArgs) > 0 {
		name = allArgs[0]
		allArgs = allArgs[1:]
	}
	return kind, name, allArgs
}

// listArg is the argument for callList.
type listArg struct {
	ctx  context.Context
	cfg  *Config
	kind int
}

// listEntry represents a single list in the list overview.
type listEntry struct {
	Kind  int    `json:"kind"`
	Label string `json:"label"`
	Name  string `json:"name,omitempty"`
	Title string `json:"title,omitempty"`
	Count int    `json:"count"`
}

func callList(arg *listArg) ([]listEntry, error) {
	_, pub, err := getSkAndPub(arg.cfg)
	if err != nil {
		return nil, err
	}

	var kinds []int
	if arg.kind > 0 {
		kinds = []int{arg.kind}
	} else {
		kinds = append(kinds, standardListKinds...)
		kinds = append(kinds, setKinds...)
	}

	filter := nostr.Filter{
		Kinds:   kinds,
		Authors: []string{pub},
	}

	evs, err := arg.cfg.QueryEvents(arg.ctx, nostr.Filters{filter})
	if err != nil {
		return nil, err
	}

	var entries []listEntry
	for _, ev := range evs {
		label := kindLabel(ev.Kind)
		if isStandardList(ev.Kind) {
			count := 0
			for _, tag := range ev.Tags {
				if len(tag) >= 2 {
					switch tag[0] {
					case "p", "e", "a", "t", "relay", "word", "emoji", "group":
						count++
					}
				}
			}
			entries = append(entries, listEntry{
				Kind:  ev.Kind,
				Label: label,
				Count: count,
			})
		} else {
			d := ev.Tags.GetFirst([]string{"d", ""})
			if d == nil {
				continue
			}
			name := (*d)[1]
			title := ""
			if t := ev.Tags.GetFirst([]string{"title", ""}); t != nil {
				title = (*t)[1]
			}
			count := 0
			for _, tag := range ev.Tags {
				if len(tag) >= 2 {
					switch tag[0] {
					case "p", "e", "a", "t", "relay", "word", "emoji", "group":
						count++
					}
				}
			}
			entries = append(entries, listEntry{
				Kind:  ev.Kind,
				Label: label,
				Name:  name,
				Title: title,
				Count: count,
			})
		}
	}
	return entries, nil
}

func doList(cCtx *cli.Context) error {
	j := cCtx.Bool("json")
	kind := cCtx.Int("kind")

	cfg := cCtx.App.Metadata["config"].(*Config)

	entries, err := callList(&listArg{
		ctx:  context.Background(),
		cfg:  cfg,
		kind: kind,
	})
	if err != nil {
		return err
	}

	for _, e := range entries {
		if j {
			b, _ := json.Marshal(e)
			fmt.Println(string(b))
		} else {
			if isStandardList(e.Kind) {
				fmt.Printf("kind:%d [%s] (%d items)\n", e.Kind, e.Label, e.Count)
			} else {
				if e.Title != "" {
					fmt.Printf("kind:%d [%s] %s (%s)\n", e.Kind, e.Label, e.Name, e.Title)
				} else {
					fmt.Printf("kind:%d [%s] %s\n", e.Kind, e.Label, e.Name)
				}
			}
		}
	}
	return nil
}

// listShowArg is the argument for callListShow.
type listShowArg struct {
	ctx  context.Context
	cfg  *Config
	kind int
	name string
}

func callListShow(arg *listShowArg) (*nostr.Event, error) {
	_, pub, err := getSkAndPub(arg.cfg)
	if err != nil {
		return nil, err
	}

	filter := nostr.Filter{
		Kinds:   []int{arg.kind},
		Authors: []string{pub},
		Limit:   1,
	}

	if !isStandardList(arg.kind) {
		if arg.name == "" {
			return nil, errors.New("list name is required")
		}
		filter.Tags = nostr.TagMap{"d": []string{arg.name}}
	}

	evs, err := arg.cfg.QueryEvents(arg.ctx, nostr.Filters{filter})
	if err != nil {
		return nil, err
	}

	if len(evs) == 0 {
		if isStandardList(arg.kind) {
			return nil, fmt.Errorf("no %s list found", kindLabel(arg.kind))
		}
		return nil, fmt.Errorf("list %q not found", arg.name)
	}

	return evs[len(evs)-1], nil
}

func printListTags(cfg *Config, ev *nostr.Event) {
	for _, tag := range ev.Tags {
		if len(tag) < 2 {
			continue
		}
		switch tag[0] {
		case "p":
			npub, _ := nip19.EncodePublicKey(tag[1])
			profile, err := cfg.GetProfile(tag[1])
			if err == nil && profile.Name != "" {
				fmt.Printf("%s (%s)\n", npub, profile.Name)
			} else {
				fmt.Println(npub)
			}
		case "e":
			note, _ := nip19.EncodeNote(tag[1])
			fmt.Println(note)
		case "a":
			fmt.Println(tag[1])
		case "relay":
			fmt.Println(tag[1])
		case "t":
			fmt.Printf("#%s\n", tag[1])
		case "word":
			fmt.Println(tag[1])
		case "emoji":
			if len(tag) >= 3 {
				fmt.Printf(":%s: %s\n", tag[1], tag[2])
			}
		case "group":
			if len(tag) >= 3 {
				fmt.Printf("%s (%s)\n", tag[1], tag[2])
			} else {
				fmt.Println(tag[1])
			}
		case "d", "title", "image", "description", "client":
			// skip metadata tags
		default:
			fmt.Printf("[%s] %s\n", tag[0], tag[1])
		}
	}
}

func formatListTags(cfg *Config, ev *nostr.Event) string {
	var sb strings.Builder
	for _, tag := range ev.Tags {
		if len(tag) < 2 {
			continue
		}
		switch tag[0] {
		case "p":
			npub, _ := nip19.EncodePublicKey(tag[1])
			profile, err := cfg.GetProfile(tag[1])
			if err == nil && profile.Name != "" {
				fmt.Fprintf(&sb, "%s (%s)\n", npub, profile.Name)
			} else {
				fmt.Fprintln(&sb, npub)
			}
		case "e":
			note, _ := nip19.EncodeNote(tag[1])
			fmt.Fprintln(&sb, note)
		case "a":
			fmt.Fprintln(&sb, tag[1])
		case "relay":
			fmt.Fprintln(&sb, tag[1])
		case "t":
			fmt.Fprintf(&sb, "#%s\n", tag[1])
		case "word":
			fmt.Fprintln(&sb, tag[1])
		case "emoji":
			if len(tag) >= 3 {
				fmt.Fprintf(&sb, ":%s: %s\n", tag[1], tag[2])
			}
		case "group":
			if len(tag) >= 3 {
				fmt.Fprintf(&sb, "%s (%s)\n", tag[1], tag[2])
			} else {
				fmt.Fprintln(&sb, tag[1])
			}
		case "d", "title", "image", "description", "client":
			// skip metadata tags
		}
	}
	return sb.String()
}

func doListShow(cCtx *cli.Context) error {
	j := cCtx.Bool("json")
	kind, name, _ := resolveKindAndName(cCtx)

	cfg := cCtx.App.Metadata["config"].(*Config)

	if !isStandardList(kind) && name == "" {
		return cli.ShowSubcommandHelp(cCtx)
	}

	ev, err := callListShow(&listShowArg{
		ctx:  context.Background(),
		cfg:  cfg,
		kind: kind,
		name: name,
	})
	if err != nil {
		return err
	}

	if j {
		b, _ := json.Marshal(ev)
		fmt.Println(string(b))
		return nil
	}

	printListTags(cfg, ev)
	return nil
}

// listAddArg is the argument for callListAdd.
type listAddArg struct {
	ctx   context.Context
	cfg   *Config
	kind  int
	name  string
	items []string
}

func callListAdd(arg *listAddArg) error {
	sk, pub, err := getSkAndPub(arg.cfg)
	if err != nil {
		return err
	}

	// Fetch existing list
	filter := nostr.Filter{
		Kinds:   []int{arg.kind},
		Authors: []string{pub},
		Limit:   1,
	}
	if !isStandardList(arg.kind) {
		filter.Tags = nostr.TagMap{"d": []string{arg.name}}
	}

	evs, err := arg.cfg.QueryEvents(arg.ctx, nostr.Filters{filter})
	if err != nil {
		return err
	}

	ev := nostr.Event{}
	ev.PubKey = pub
	ev.Kind = arg.kind
	ev.CreatedAt = nostr.Now()
	ev.Tags = nostr.Tags{}
	clientTag(&ev)

	// Carry over existing tags
	if len(evs) > 0 {
		existing := evs[len(evs)-1]
		for _, tag := range existing.Tags {
			if tag[0] == "client" {
				continue
			}
			ev.Tags = append(ev.Tags, tag)
		}
		ev.Content = existing.Content
	} else if !isStandardList(arg.kind) {
		ev.Tags = ev.Tags.AppendUnique(nostr.Tag{"d", arg.name})
	}

	// Add new items
	for _, item := range arg.items {
		tag := itemToTag(item, arg.kind)
		if tag == nil {
			return fmt.Errorf("cannot determine tag type for %q", item)
		}
		ev.Tags = ev.Tags.AppendUnique(tag)
	}

	if err := ev.Sign(sk); err != nil {
		return err
	}

	var success atomic.Int64
	arg.cfg.Do(arg.ctx, Relay{Write: true}, func(ctx context.Context, relay *nostr.Relay) bool {
		if err := relay.Publish(ctx, ev); err != nil {
			fmt.Fprintln(os.Stderr, relay.URL, err)
		} else {
			success.Add(1)
		}
		return true
	})
	if success.Load() == 0 {
		return errors.New("cannot publish list")
	}
	return nil
}

func doListAdd(cCtx *cli.Context) error {
	kind, name, rest := resolveKindAndName(cCtx)

	if len(rest) < 1 {
		return cli.ShowSubcommandHelp(cCtx)
	}

	cfg := cCtx.App.Metadata["config"].(*Config)

	return callListAdd(&listAddArg{
		ctx:   context.Background(),
		cfg:   cfg,
		kind:  kind,
		name:  name,
		items: rest,
	})
}

// listRemoveArg is the argument for callListRemove.
type listRemoveArg struct {
	ctx   context.Context
	cfg   *Config
	kind  int
	name  string
	items []string
}

func callListRemove(arg *listRemoveArg) error {
	sk, pub, err := getSkAndPub(arg.cfg)
	if err != nil {
		return err
	}

	// Fetch existing list
	filter := nostr.Filter{
		Kinds:   []int{arg.kind},
		Authors: []string{pub},
		Limit:   1,
	}
	if !isStandardList(arg.kind) {
		filter.Tags = nostr.TagMap{"d": []string{arg.name}}
	}

	evs, err := arg.cfg.QueryEvents(arg.ctx, nostr.Filters{filter})
	if err != nil {
		return err
	}

	if len(evs) == 0 {
		if isStandardList(arg.kind) {
			return fmt.Errorf("no %s list found", kindLabel(arg.kind))
		}
		return fmt.Errorf("list %q not found", arg.name)
	}

	existing := evs[len(evs)-1]

	// Build set of values to remove
	removeSet := make(map[string]struct{})
	for _, item := range arg.items {
		tag := itemToTag(item, arg.kind)
		if tag == nil {
			return fmt.Errorf("cannot determine tag type for %q", item)
		}
		removeSet[tag[0]+":"+tag[1]] = struct{}{}
	}

	ev := nostr.Event{}
	ev.PubKey = pub
	ev.Kind = arg.kind
	ev.CreatedAt = nostr.Now()
	ev.Tags = nostr.Tags{}
	ev.Content = existing.Content
	clientTag(&ev)

	for _, tag := range existing.Tags {
		if len(tag) >= 2 && tag[0] != "client" {
			key := tag[0] + ":" + tag[1]
			if _, ok := removeSet[key]; ok {
				continue
			}
		}
		ev.Tags = append(ev.Tags, tag)
	}

	if err := ev.Sign(sk); err != nil {
		return err
	}

	var success atomic.Int64
	arg.cfg.Do(arg.ctx, Relay{Write: true}, func(ctx context.Context, relay *nostr.Relay) bool {
		if err := relay.Publish(ctx, ev); err != nil {
			fmt.Fprintln(os.Stderr, relay.URL, err)
		} else {
			success.Add(1)
		}
		return true
	})
	if success.Load() == 0 {
		return errors.New("cannot publish list")
	}
	return nil
}

func doListRemove(cCtx *cli.Context) error {
	kind, name, rest := resolveKindAndName(cCtx)

	if len(rest) < 1 {
		return cli.ShowSubcommandHelp(cCtx)
	}

	cfg := cCtx.App.Metadata["config"].(*Config)

	return callListRemove(&listRemoveArg{
		ctx:   context.Background(),
		cfg:   cfg,
		kind:  kind,
		name:  name,
		items: rest,
	})
}

// listDeleteArg is the argument for callListDelete.
type listDeleteArg struct {
	ctx  context.Context
	cfg  *Config
	kind int
	name string
}

func callListDelete(arg *listDeleteArg) error {
	sk, pub, err := getSkAndPub(arg.cfg)
	if err != nil {
		return err
	}

	ev := nostr.Event{}
	ev.PubKey = pub
	ev.Kind = nostr.KindDeletion
	ev.CreatedAt = nostr.Now()

	if isStandardList(arg.kind) {
		ev.Tags = nostr.Tags{
			nostr.Tag{"k", fmt.Sprintf("%d", arg.kind)},
		}
	} else {
		if arg.name == "" {
			return errors.New("list name is required")
		}
		ev.Tags = nostr.Tags{
			nostr.Tag{"a", fmt.Sprintf("%d:%s:%s", arg.kind, pub, arg.name)},
		}
	}

	if err := ev.Sign(sk); err != nil {
		return err
	}

	var success atomic.Int64
	arg.cfg.Do(arg.ctx, Relay{Write: true}, func(ctx context.Context, relay *nostr.Relay) bool {
		if err := relay.Publish(ctx, ev); err != nil {
			fmt.Fprintln(os.Stderr, relay.URL, err)
		} else {
			success.Add(1)
		}
		return true
	})
	if success.Load() == 0 {
		return errors.New("cannot delete list")
	}
	return nil
}

func doListDelete(cCtx *cli.Context) error {
	kind, name, _ := resolveKindAndName(cCtx)

	if !isStandardList(kind) && name == "" {
		return cli.ShowSubcommandHelp(cCtx)
	}

	cfg := cCtx.App.Metadata["config"].(*Config)

	return callListDelete(&listDeleteArg{
		ctx:  context.Background(),
		cfg:  cfg,
		kind: kind,
		name: name,
	})
}

// itemToTag converts a user-provided item string to the appropriate nostr.Tag.
// It auto-detects the type from the input format.
func itemToTag(item string, kind int) nostr.Tag {
	// npub/nprofile → p tag
	if pp := sdk.InputToProfile(context.TODO(), item); pp != nil {
		return nostr.Tag{"p", pp.PublicKey}
	}
	// note/nevent → e tag
	if evp := sdk.InputToEventPointer(item); evp != nil {
		return nostr.Tag{"e", evp.ID}
	}
	// naddr → a tag
	if strings.HasPrefix(item, "naddr1") {
		if _, v, err := nip19.Decode(item); err == nil {
			if ep, ok := v.(nostr.EntityPointer); ok {
				return nostr.Tag{"a", fmt.Sprintf("%d:%s:%s", ep.Kind, ep.PublicKey, ep.Identifier)}
			}
		}
	}
	// relay URL → relay tag
	if strings.HasPrefix(item, "wss://") || strings.HasPrefix(item, "ws://") {
		return nostr.Tag{"relay", item}
	}
	// hashtag (with or without #)
	if strings.HasPrefix(item, "#") {
		return nostr.Tag{"t", strings.TrimPrefix(item, "#")}
	}
	// Infer from kind
	switch kind {
	case 10000: // mute list
		return nostr.Tag{"p", item}
	case 10001: // pinned notes
		return nostr.Tag{"e", item}
	case 10002, 10006, 10007, 10050: // relay lists
		return nostr.Tag{"relay", item}
	case 10003: // bookmarks
		return nostr.Tag{"e", item}
	case 10015: // interests
		return nostr.Tag{"t", item}
	case 30000: // follow set
		return nostr.Tag{"p", item}
	case 30002: // relay set
		return nostr.Tag{"relay", item}
	case 30015: // interest set
		return nostr.Tag{"t", item}
	case 30003, 30004, 30005, 30006: // bookmark/curation
		return nostr.Tag{"e", item}
	}
	return nil
}
