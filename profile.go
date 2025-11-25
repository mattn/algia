package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sync/atomic"

	"github.com/urfave/cli/v2"

	"github.com/nbd-wtf/go-nostr"
	"github.com/nbd-wtf/go-nostr/nip19"
	"github.com/nbd-wtf/go-nostr/sdk"
)

func doProfile(cCtx *cli.Context) error {
	user := cCtx.String("u")
	j := cCtx.Bool("json")

	cfg := cCtx.App.Metadata["config"].(*Config)

	var pub string
	if user == "" {
		if _, s, err := nip19.Decode(cfg.PrivateKey); err == nil {
			if pub, err = nostr.GetPublicKey(s.(string)); err != nil {
				return err
			}
			if user, err = nip19.EncodePublicKey(pub); err != nil {
				return err
			}
		} else {
			return err
		}
	} else {
		if pp := sdk.InputToProfile(context.TODO(), user); pp != nil {
			pub = pp.PublicKey
		} else {
			pub = user
		}
	}

	profile, err := cfg.GetProfile(user)
	if err != nil {
		return err
	}

	if j {
		fmt.Fprintln(os.Stdout, profile)
		return nil
	}
	npub, err := nip19.EncodePublicKey(pub)
	if err != nil {
		return err
	}
	fmt.Printf("Pubkey: %v\n", npub)
	fmt.Printf("Name: %v\n", profile.Name)
	fmt.Printf("DisplayName: %v\n", profile.DisplayName)
	fmt.Printf("WebSite: %v\n", profile.Website)
	fmt.Printf("Picture: %v\n", profile.Picture)
	fmt.Printf("NIP-05: %v\n", profile.Nip05)
	fmt.Printf("LUD-16: %v\n", profile.Lud16)
	fmt.Printf("About: %v\n", profile.About)
	fmt.Printf("Bot: %v\n", profile.Bot)
	return nil
}

func doUpdateProfile(cCtx *cli.Context) error {
	cfg := cCtx.App.Metadata["config"].(*Config)

	var pub string
	if _, s, err := nip19.Decode(cfg.PrivateKey); err == nil {
		if pub, err = nostr.GetPublicKey(s.(string)); err != nil {
			return err
		}
	} else {
		return err
	}

	// get set-metadata
	filters := nostr.Filters{
		{
			Kinds:   []int{nostr.KindProfileMetadata},
			Authors: []string{pub},
			Limit:   1,
		},
	}

	evs, err := cfg.QueryEvents(filters)
	if err != nil {
		return err
	}

	var ev *nostr.Event
	if len(evs) > 0 {
		ev = evs[0]
	} else {
		ev = &nostr.Event{
			Kind: nostr.KindProfileMetadata,
		}
		profile, err := cfg.GetProfile("")
		if err == nil {
			if b, err := json.MarshalIndent(profile, "", "  "); err == nil {
				ev.Content = string(b)
			}
		}
	}

	var profile map[string]any
	err = json.Unmarshal([]byte(ev.Content), &profile)
	if err != nil {
		return err
	}

	for _, arg := range cCtx.Args().Slice() {
		var set map[string]any
		err := json.Unmarshal([]byte(arg), &set)
		if err != nil {
			return err
		}
		for k, v := range set {
			if v == nil {
				delete(profile, k)
			} else {
				profile[k] = v
			}
		}
	}
	b, err := json.Marshal(profile)
	if err != nil {
		return err
	}
	ev.Content = string(b)

	var sk string
	if _, s, err := nip19.Decode(cfg.PrivateKey); err == nil {
		sk = s.(string)
	} else {
		return err
	}

	clientTag(ev)
	ev.CreatedAt = nostr.Now()
	if err := ev.Sign(sk); err != nil {
		return err
	}

	var success atomic.Int64
	cfg.Do(Relay{Write: true}, func(ctx context.Context, relay *nostr.Relay) bool {
		err := relay.Publish(ctx, *ev)
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
	if cfg.verbose {
		if id, err := nip19.EncodeNote(ev.ID); err == nil {
			fmt.Println(id)
		}
	}
	return nil
}
