package main

import (
	"errors"

	"github.com/urfave/cli/v2"

	"github.com/nbd-wtf/go-nostr"
	"github.com/nbd-wtf/go-nostr/nip19"
)

func doBMList(cCtx *cli.Context) error {
	n := cCtx.Int("n")
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

	// get timeline
	filter := nostr.Filter{
		Kinds:   []int{nostr.KindCategorizedBookmarksList},
		Authors: []string{npub},
		Tags:    nostr.TagMap{"d": []string{"bookmark"}},
		Limit:   n,
	}

	be := []string{}
	evs, err := cfg.QueryEvents(nostr.Filters{filter})
	if err != nil {
		return err
	}
	for _, ev := range evs {
		for _, tag := range ev.Tags {
			if len(tag) > 1 && tag[0] == "e" {
				be = append(be, tag[1:]...)
			}
		}
	}
	filter = nostr.Filter{
		Kinds: []int{nostr.KindTextNote},
		IDs:   be,
	}
	eevs, err := cfg.QueryEvents(nostr.Filters{filter})
	if err != nil {
		return err
	}
	cfg.PrintEvents(eevs, nil, j, extra)
	return nil
}

func doBMPost(cCtx *cli.Context) error {
	return errors.New("Not Implemented")
}
