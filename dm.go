package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync/atomic"

	"github.com/urfave/cli/v2"

	"github.com/fatih/color"
	"github.com/nbd-wtf/go-nostr"
	"github.com/nbd-wtf/go-nostr/nip04"
	"github.com/nbd-wtf/go-nostr/nip19"
	"github.com/nbd-wtf/nostr-sdk"
)

func doDMList(cCtx *cli.Context) error {
	j := cCtx.Bool("json")

	cfg := cCtx.App.Metadata["config"].(*Config)

	// get followers
	followsMap, err := cfg.GetFollows(cCtx.String("a"))
	if err != nil {
		return err
	}

	var sk string
	var npub string
	if _, s, err := nip19.Decode(cfg.PrivateKey); err == nil {
		sk = s.(string)
	} else {
		return err
	}
	if npub, err = nostr.GetPublicKey(sk); err != nil {
		return err
	}

	// get timeline
	filter := nostr.Filter{
		Kinds:   []int{nostr.KindEncryptedDirectMessage},
		Authors: []string{npub},
	}

	evs := cfg.Events(filter)
	type entry struct {
		name   string
		pubkey string
	}
	users := []entry{}
	m := map[string]struct{}{}
	for _, ev := range evs {
		p := ev.Tags.GetFirst([]string{"p"}).Value()
		if _, ok := m[p]; ok {
			continue
		}
		if profile, ok := followsMap[p]; ok {
			m[p] = struct{}{}
			p, _ = nip19.EncodePublicKey(p)
			users = append(users, entry{
				name:   profile.DisplayName,
				pubkey: p,
			})
		} else {
			users = append(users, entry{
				name:   p,
				pubkey: p,
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
		color.Set(color.FgHiRed)
		fmt.Print(user.name)
		color.Set(color.Reset)
		fmt.Print(": ")
		color.Set(color.FgHiBlue)
		fmt.Println(user.pubkey)
		color.Set(color.Reset)
	}
	return nil
}

func doDMTimeline(cCtx *cli.Context) error {
	u := cCtx.String("u")
	j := cCtx.Bool("json")
	extra := cCtx.Bool("extra")

	cfg := cCtx.App.Metadata["config"].(*Config)

	var sk string
	var npub string
	var err error
	if _, s, err := nip19.Decode(cfg.PrivateKey); err == nil {
		sk = s.(string)
	} else {
		return err
	}
	if npub, err = nostr.GetPublicKey(sk); err != nil {
		return err
	}

	if u == "me" {
		u = npub
	}
	var pub string
	if pp := sdk.InputToProfile(context.TODO(), u); pp != nil {
		pub = pp.PublicKey
	} else {
		return fmt.Errorf("failed to parse pubkey from '%s'", u)
	}
	// get followers
	followsMap, err := cfg.GetFollows(cCtx.String("a"))
	if err != nil {
		return err
	}

	// get timeline
	filter := nostr.Filter{
		Kinds:   []int{nostr.KindEncryptedDirectMessage},
		Authors: []string{npub, pub},
		Tags:    nostr.TagMap{"p": []string{npub, pub}},
		Limit:   9999,
	}

	evs := cfg.Events(filter)
	cfg.PrintEvents(evs, followsMap, j, extra)
	return nil
}

func doDMPost(cCtx *cli.Context) error {
	u := cCtx.String("u")
	stdin := cCtx.Bool("stdin")
	if !stdin && cCtx.Args().Len() == 0 {
		return cli.ShowSubcommandHelp(cCtx)
	}
	sensitive := cCtx.String("sensitive")

	cfg := cCtx.App.Metadata["config"].(*Config)

	var sk string
	if _, s, err := nip19.Decode(cfg.PrivateKey); err == nil {
		sk = s.(string)
	} else {
		return err
	}
	ev := nostr.Event{}
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

	var success atomic.Int64
	cfg.Do(Relay{Write: true}, func(ctx context.Context, relay *nostr.Relay) bool {
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
