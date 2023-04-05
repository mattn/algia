package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"

	"github.com/urfave/cli/v2"

	"github.com/nbd-wtf/go-nostr"
	"github.com/nbd-wtf/go-nostr/nip19"
)

func doProfile(cCtx *cli.Context) error {
	user := cCtx.String("u")
	j := cCtx.Bool("json")

	cfg := cCtx.App.Metadata["config"].(*Config)
	relay := cfg.FindRelay(Relay{Read: true})
	if relay == nil {
		return errors.New("cannot connect relays")
	}
	defer relay.Close()

	var pub string
	if user == "" {
		if _, s, err := nip19.Decode(cfg.PrivateKey); err != nil {
			return err
		} else {
			if pub, err = nostr.GetPublicKey(s.(string)); err != nil {
				return err
			}
		}
	} else {
		if _, s, err := nip19.Decode(user); err != nil {
			pub = user
		} else {
			pub = s.(string)
		}
	}

	// get set-metadata
	filter := nostr.Filter{
		Kinds:   []int{nostr.KindSetMetadata},
		Authors: []string{pub},
		Limit:   1,
	}

	evs := cfg.Events(filter)
	if len(evs) == 0 {
		return errors.New("cannot find user")
	}
	var profile Profile
	err := json.Unmarshal([]byte(evs[0].Content), &profile)
	if err != nil {
		return err
	}
	if j {
		json.NewEncoder(os.Stdout).Encode(profile)
		return nil
	}
	fmt.Printf("Name: %v\n", profile.Name)
	fmt.Printf("DisplayName: %v\n", profile.DisplayName)
	fmt.Printf("WebSite: %v\n", profile.Website)
	fmt.Printf("Picture: %v\n", profile.Picture)
	fmt.Printf("NIP-05: %v\n", profile.Nip05)
	fmt.Printf("LUC16: %v\n", profile.Lud16)
	fmt.Printf("About: %v\n", profile.About)
	return nil
}
