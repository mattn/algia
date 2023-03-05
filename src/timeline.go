package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/fatih/color"
	"github.com/nbd-wtf/go-nostr"
	"github.com/urfave/cli/v2"
)

func doTimeline(cCtx *cli.Context) error {
	n := cCtx.Int("n")
	j := cCtx.Bool("json")
	extra := cCtx.Bool("extra")

	cfg := cCtx.App.Metadata["config"].(*Config)
	relay := cfg.FindRelay(Relay{Read: true})
	if relay == nil {
		return errors.New("cannot connect relays")
	}
	defer relay.Close()

	// get followers
	follows := []string{}
	if cfg.Updated.Add(3*time.Hour).Before(time.Now()) || len(cfg.Follows) == 0 {
		cfg.Follows = map[string]Profile{}
		for _, ev := range relay.QuerySync(context.Background(), nostr.Filter{Kinds: []int{nostr.KindContactList}}) {
			follows = append(follows, ev.PubKey)
		}
		if cfg.verbose {
			fmt.Printf("found %d followers\n", len(follows))
		}
		if len(follows) > 0 {
			// get follower's desecriptions
			evs := relay.QuerySync(context.Background(), nostr.Filter{
				Kinds:   []int{nostr.KindSetMetadata},
				Authors: follows,
			})

			for _, ev := range evs {
				var profile Profile
				err := json.Unmarshal([]byte(ev.Content), &profile)
				if err == nil {
					cfg.Follows[ev.PubKey] = profile
				}
			}
		}

		cfg.Updated = time.Now()
		if err := cfg.save(cCtx.String("a")); err != nil {
			return err
		}
	} else {
		for k := range cfg.Follows {
			follows = append(follows, k)
		}
	}

	// get timeline
	filters := []nostr.Filter{}
	filters = append(filters, nostr.Filter{
		Kinds:   []int{nostr.KindTextNote},
		Authors: follows,
		Limit:   n,
	})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	sub := relay.Subscribe(ctx, filters)
	go func() {
		<-sub.EndOfStoredEvents
		cancel()
	}()

	if j {
		for ev := range sub.Events {
			if extra {
				profile, ok := cfg.Follows[ev.PubKey]
				if ok {
					eev := Event{
						Event:   ev,
						Profile: profile,
					}
					json.NewEncoder(os.Stdout).Encode(eev)
				}
			} else {
				json.NewEncoder(os.Stdout).Encode(ev)
			}
		}
		return nil
	}

	for ev := range sub.Events {
		profile, ok := cfg.Follows[ev.PubKey]
		if ok {
			color.Set(color.FgHiRed)
			fmt.Print(profile.Name)
		} else {
			color.Set(color.FgRed)
			fmt.Print(ev.PubKey)
		}
		color.Set(color.Reset)
		fmt.Print(": ")
		color.Set(color.FgHiBlue)
		fmt.Println(ev.PubKey)
		color.Set(color.Reset)
		fmt.Println(ev.Content)
	}

	return nil
}
