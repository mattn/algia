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

	"github.com/urfave/cli/v2"

	"github.com/fatih/color"
	"github.com/nbd-wtf/go-nostr"
	"github.com/nbd-wtf/go-nostr/nip04"
	"github.com/nbd-wtf/go-nostr/nip19"
	"github.com/nbd-wtf/go-nostr/nip44"
	"github.com/nbd-wtf/go-nostr/nip59"
	"github.com/nbd-wtf/go-nostr/sdk"
)

func doDMList(cCtx *cli.Context) error {
	j := cCtx.Bool("json")

	cfg := cCtx.App.Metadata["config"].(*Config)

	var sk string
	var pub string
	var err error
	if _, s, err := nip19.Decode(cfg.PrivateKey); err == nil {
		sk = s.(string)
	} else {
		return err
	}
	if pub, err = nostr.GetPublicKey(sk); err != nil {
		return err
	}

	// get timeline
	filters := nostr.Filters{
		{
			Kinds:   []int{nostr.KindEncryptedDirectMessage},
			Authors: []string{pub},
			Limit:   9999,
		},
		{
			Kinds: []int{nostr.KindEncryptedDirectMessage},
			Tags:  nostr.TagMap{"p": []string{pub}},
			Limit: 9999,
		},
		{
			Kinds: []int{1059},
			Tags:  nostr.TagMap{"p": []string{pub}},
			Limit: 9999,
		},
	}

	evs, err := cfg.QueryEvents(filters)
	if err != nil {
		return err
	}

	if cfg.verbose {
		fmt.Fprintf(os.Stderr, "Total events received: %d\n", len(evs))
		for i, ev := range evs {
			fmt.Fprintf(os.Stderr, "Event %d: kind=%d, author=%s, tags=%v\n", i, ev.Kind, ev.PubKey, ev.Tags)
		}
	}

	type entry struct {
		Name   string `json:"name"`
		Pubkey string `json:"pubkey"`
	}
	users := []entry{}
	m := map[string]struct{}{}
	for _, ev := range evs {
		var p string
		if ev.PubKey == pub {
			tag := ev.Tags.GetFirst([]string{"p"})
			if tag == nil {
				continue
			}
			p = tag.Value()
		} else {
			p = ev.PubKey
		}
		if _, ok := m[p]; ok {
			continue
		}
		m[p] = struct{}{}
		npub, err := nip19.EncodePublicKey(p)
		if err != nil {
			continue
		}
		profile, err := cfg.GetProfile(npub)
		if err == nil {
			name := profile.DisplayName
			if name == "" && profile.Name != "" {
				name = profile.Name
			}
			users = append(users, entry{
				Name:   name,
				Pubkey: npub,
			})
		} else {
			users = append(users, entry{
				Name:   npub,
				Pubkey: npub,
			})
		}
	}

	if j {
		for _, user := range users {
			json.NewEncoder(os.Stdout).Encode(user)
		}
		return nil
	}

	for _, user := range users {
		color.Set(color.FgHiBlue)
		fmt.Print(user.Pubkey)
		color.Set(color.Reset)
		fmt.Print(": ")
		color.Set(color.FgHiRed)
		fmt.Println(user.Name)
		color.Set(color.Reset)
	}
	return nil
}

func doDMTimeline(cCtx *cli.Context) error {
	u := cCtx.String("u")
	n := cCtx.Int("n")
	j := cCtx.Bool("json")
	extra := cCtx.Bool("extra")

	cfg := cCtx.App.Metadata["config"].(*Config)

	var sk string
	var pk string
	var err error
	if _, s, err := nip19.Decode(cfg.PrivateKey); err == nil {
		sk = s.(string)
	} else {
		return err
	}
	if pk, err = nostr.GetPublicKey(sk); err != nil {
		return err
	}

	var pub string
	if u == "me" {
		pub = pk
	} else if pp := sdk.InputToProfile(context.TODO(), u); pp != nil {
		pub = pp.PublicKey
	} else {
		return fmt.Errorf("failed to parse pubkey from '%s'", u)
	}

	var evs []*nostr.Event

	// get timeline with combined filters for both kind 4 and 1059
	filters := nostr.Filters{
		{
			Kinds:   []int{nostr.KindEncryptedDirectMessage},
			Authors: []string{pub},
			Tags:    nostr.TagMap{"p": []string{pk}},
			Limit:   9999,
		},
	}
	// Collect all events, then sort and display top n
	if eevs, err := cfg.QueryEvents(filters); err != nil {
		return err
	} else {
		for _, ev := range eevs {
			evs = append(evs, ev)
		}
	}

	// get timeline with combined filters for both kind 4 and 1059
	filters = nostr.Filters{
		{
			Kinds:   []int{nostr.KindEncryptedDirectMessage},
			Authors: []string{pk},
			Tags:    nostr.TagMap{"p": []string{pub}},
			Limit:   9999,
		},
	}
	// Collect all events, then sort and display top n
	if eevs, err := cfg.QueryEvents(filters); err != nil {
		return err
	} else {
		for _, ev := range eevs {
			evs = append(evs, ev)
		}
	}

	// Query for kind 1059 (encrypted) events with strict participant check
	filters = nostr.Filters{
		{
			Kinds: []int{1059},
			Tags:  nostr.TagMap{"p": []string{pk}},
			Limit: 9999,
		},
	}
	if eevs, err := cfg.QueryEvents(filters); err == nil {
		for _, ev := range eevs {
			// Validate participants for kind 1059
			if ev.Kind != 14 {
				continue
			}
			if !((ev.PubKey == pub && ev.Tags.GetFirst([]string{"p"}).Value() == pk) ||
				(ev.PubKey == pk && ev.Tags.GetFirst([]string{"p"}).Value() == pub)) {
				continue
			}
			evs = append(evs, ev)
		}
	}

	// Sort by timestamp descending (newest first)
	sort.Slice(evs, func(i, j int) bool {
		return evs[j].CreatedAt.Time().After(evs[i].CreatedAt.Time())
	})

	if len(evs) > n {
		evs = evs[len(evs)-n:]
	}

	// Display only top n events
	for _, ev := range evs {
		cfg.PrintEvent(ev, j, extra)
	}
	return nil
}

func doDMPost(cCtx *cli.Context) error {
	u := cCtx.String("u")
	stdin := cCtx.Bool("stdin")
	if !stdin && cCtx.Args().Len() == 0 {
		return cli.ShowSubcommandHelp(cCtx)
	}
	sensitive := cCtx.String("sensitive")
	useNip04 := cCtx.Bool("nip04")

	cfg := cCtx.App.Metadata["config"].(*Config)

	var sk string
	if _, s, err := nip19.Decode(cfg.PrivateKey); err == nil {
		sk = s.(string)
	} else {
		return err
	}
	ev := nostr.Event{}
	clientTag(&ev)

	if npub, err := nostr.GetPublicKey(sk); err == nil {
		if _, err := nip19.EncodePublicKey(npub); err != nil {
			return err
		}
		ev.PubKey = npub
	} else {
		return err
	}

	if stdin {
		b, err := io.ReadAll(os.Stdin)
		if err != nil {
			return err
		}
		ev.Content = string(b)
	} else {
		ev.Content = strings.Join(cCtx.Args().Slice(), "\n")
	}
	if strings.TrimSpace(ev.Content) == "" {
		return errors.New("content is empty")
	}

	if sensitive != "" {
		ev.Tags = ev.Tags.AppendUnique(nostr.Tag{"content-warning", sensitive})
	}

	if u == "me" {
		u = ev.PubKey
	}
	var pub string
	if pp := sdk.InputToProfile(context.TODO(), u); pp != nil {
		pub = pp.PublicKey
	} else {
		return fmt.Errorf("failed to parse pubkey from '%s'", u)
	}

	ev.Tags = ev.Tags.AppendUnique(nostr.Tag{"p", pub})
	ev.CreatedAt = nostr.Now()

	if useNip04 {
		ev.Kind = nostr.KindEncryptedDirectMessage
		ss, err := nip04.ComputeSharedSecret(pub, sk)
		if err != nil {
			return err
		}
		ev.Content, err = nip04.Encrypt(ev.Content, ss)
		if err != nil {
			return err
		}
		if err := ev.Sign(sk); err != nil {
			return err
		}
	} else {
		ev.Kind = 14
		eev, err := nip59.GiftWrap(ev, pub,
			func(plaintext string) (string, error) {
				conversationKey, err := nip44.GenerateConversationKey(pub, sk)
				if err != nil {
					return "", err
				}
				encrypted, err := nip44.Encrypt(plaintext, conversationKey)
				if err != nil {
					return "", err
				}
				return encrypted, nil
			},
			func(ev *nostr.Event) error {
				return ev.Sign(sk)
			},
			nil,
		)
		if err != nil {
			return err
		}
		ev = eev
	}

	var success atomic.Int64
	cfg.Do(Relay{Write: true, DM: true}, func(ctx context.Context, relay *nostr.Relay) bool {
		err := relay.Publish(ctx, ev)
		if err != nil {
			fmt.Fprintln(os.Stderr, relay.URL, err)
		} else {
			success.Add(1)
		}
		return true
	})
	if success.Load() == 0 {
		return errors.New("cannot post")
	}
	return nil
}
