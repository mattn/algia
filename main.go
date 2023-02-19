package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/ioutil"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/urfave/cli/v2"

	"github.com/fatih/color"
	"github.com/nbd-wtf/go-nostr"
	"github.com/nbd-wtf/go-nostr/nip19"
)

const name = "algia"

const version = "0.0.1"

var revision = "HEAD"

type Relay struct {
	Read  bool
	Write bool
}

type Config struct {
	Relays     map[string]Relay   `json:"relays"`
	Follows    map[string]Profile `json:"follows"`
	PrivateKey string             `json:"privatekey"`
	Updated    time.Time          `json:"updated"`
}

type Event struct {
	Event   *nostr.Event `json:"event"`
	Profile Profile      `json:"profile"`
}

type Profile struct {
	Website     string `json:"website"`
	Nip05       string `json:"nip05"`
	Picture     string `json:"picture"`
	Lud16       string `json:"lud16"`
	DisplayName string `json:"display_name"`
	About       string `json:"about"`
	Name        string `json:"name"`
}

func loadConfig(profile string) (*Config, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return nil, err
	}
	dir = filepath.Join(dir, "algia")

	var fp string
	if profile == "" {
		fp = filepath.Join(dir, "config.json")
	} else if profile == "?" {
		names, err := filepath.Glob(filepath.Join(dir, "config-*.json"))
		if err != nil {
			return nil, err
		}
		for _, name := range names {
			name = filepath.Base(name)
			name = strings.TrimLeft(name[6:len(name)-5], "-")
			fmt.Println(name)
		}
		os.Exit(0)
	} else {
		fp = filepath.Join(dir, "config-"+profile+".json")
	}
	os.MkdirAll(filepath.Dir(fp), 0700)

	b, err := ioutil.ReadFile(fp)
	if err != nil {
		return nil, err
	}
	var cfg Config
	err = json.Unmarshal(b, &cfg)
	if err != nil {
		return nil, err
	}
	return &cfg, nil
}

func (cfg *Config) FindRelay(write bool) *nostr.Relay {
	for k, v := range cfg.Relays {
		if write && !v.Write {
			continue
		}
		if !write && !v.Read {
			continue
		}
		ctx := context.WithValue(context.Background(), "url", k)
		relay, err := nostr.RelayConnect(ctx, k)
		if err != nil {
			continue
		}
		return relay
	}
	return nil
}

func (cfg *Config) Do(write bool, f func(*nostr.Relay) bool) {
	for k, v := range cfg.Relays {
		if write && !v.Write {
			continue
		}
		if !write && !v.Read {
			continue
		}
		ctx := context.WithValue(context.Background(), "url", k)
		relay, err := nostr.RelayConnect(ctx, k)
		if err != nil {
			continue
		}
		ret := f(relay)
		relay.Close()
		if !ret {
			break
		}
	}
}

func (cfg *Config) save() error {
	dir, err := os.UserConfigDir()
	if err != nil {
		return err
	}
	fp := filepath.Join(dir, "algia")
	fp = filepath.Join(fp, "config.json")
	b, err := json.MarshalIndent(&cfg, "", "  ")
	if err != nil {
		return err
	}
	return ioutil.WriteFile(fp, b, 0644)
}

func doPost(cCtx *cli.Context) error {
	stdin := cCtx.Bool("stdin")
	verbose := cCtx.Bool("verbose")
	if !stdin && cCtx.Args().Len() == 0 {
		return cli.ShowSubcommandHelp(cCtx)
	}

	cfg := cCtx.App.Metadata["config"].(*Config)

	var sk string
	if _, s, err := nip19.Decode(cfg.PrivateKey); err != nil {
		return err
	} else {
		sk = s.(string)
	}
	ev := nostr.Event{}
	if pub, err := nostr.GetPublicKey(sk); err == nil {
		if _, err := nip19.EncodePublicKey(pub); err != nil {
			return err
		}
		ev.PubKey = pub
	} else {
		return err
	}

	if stdin {
		b, err := ioutil.ReadAll(os.Stdin)
		if err != nil {
			return err
		}
		ev.Content = string(b)
	} else {
		ev.Content = strings.Join(cCtx.Args().Slice(), "\n")
	}

	ev.CreatedAt = time.Now()
	ev.Kind = nostr.KindTextNote
	ev.Sign(sk)

	success := 0
	cfg.Do(true, func(relay *nostr.Relay) bool {
		status := relay.Publish(context.Background(), ev)
		if verbose {
			fmt.Println(relay.URL, status)
		}
		if status != nostr.PublishStatusFailed {
			success++
		}
		return true
	})
	if success == 0 {
		return errors.New("cannot post")
	}
	return nil
}

func doReply(cCtx *cli.Context) error {
	stdin := cCtx.Bool("stdin")
	verbose := cCtx.Bool("verbose")
	id := cCtx.String("id")
	quote := cCtx.Bool("quote")
	if !stdin && cCtx.Args().Len() == 0 {
		return cli.ShowSubcommandHelp(cCtx)
	}

	cfg := cCtx.App.Metadata["config"].(*Config)

	var sk string
	if _, s, err := nip19.Decode(cfg.PrivateKey); err != nil {
		return err
	} else {
		sk = s.(string)
	}
	ev := nostr.Event{}
	if pub, err := nostr.GetPublicKey(sk); err == nil {
		if _, err := nip19.EncodePublicKey(pub); err != nil {
			return err
		}
		ev.PubKey = pub
	} else {
		return err
	}

	if _, tmp, err := nip19.Decode(id); err == nil {
		id = tmp.(string)
	}

	ev.CreatedAt = time.Now()
	ev.Kind = nostr.KindTextNote
	if stdin {
		b, err := ioutil.ReadAll(os.Stdin)
		if err != nil {
			return err
		}
		ev.Content = string(b)
	} else {
		ev.Content = strings.Join(cCtx.Args().Slice(), "\n")
	}

	success := 0
	cfg.Do(true, func(relay *nostr.Relay) bool {
		if !quote {
			ev.Tags = ev.Tags.AppendUnique(nostr.Tag{"e", id, relay.URL, "reply"})
		} else {
			ev.Tags = ev.Tags.AppendUnique(nostr.Tag{"e", id, relay.URL, "mention"})
		}
		ev.Sign(sk)
		status := relay.Publish(context.Background(), ev)
		if verbose {
			fmt.Println(relay.URL, status)
		}
		if status != nostr.PublishStatusFailed {
			success++
		}
		return true
	})
	if success == 0 {
		return errors.New("cannot reply")
	}
	return nil
}

func doRepost(cCtx *cli.Context) error {
	verbose := cCtx.Bool("verbose")
	id := cCtx.String("id")

	cfg := cCtx.App.Metadata["config"].(*Config)

	ev := nostr.Event{}
	var sk string
	if _, s, err := nip19.Decode(cfg.PrivateKey); err != nil {
		return err
	} else {
		sk = s.(string)
	}
	if pub, err := nostr.GetPublicKey(sk); err == nil {
		if _, err := nip19.EncodePublicKey(pub); err != nil {
			return err
		}
		ev.PubKey = pub
	} else {
		return err
	}

	if _, tmp, err := nip19.Decode(id); err == nil {
		ev.Tags = ev.Tags.AppendUnique(nostr.Tag{"e", tmp.(string)})
	} else {
		ev.Tags = ev.Tags.AppendUnique(nostr.Tag{"e", id})
	}
	filter := nostr.Filter{
		Kinds: []int{nostr.KindTextNote},
		IDs:   []string{id},
	}

	ev.CreatedAt = time.Now()
	ev.Kind = nostr.KindBoost
	ev.Content = ""

	success := 0
	first := true
	cfg.Do(true, func(relay *nostr.Relay) bool {
		if first {
			for _, tmp := range relay.QuerySync(context.Background(), filter) {
				ev.Tags = ev.Tags.AppendUnique(nostr.Tag{"p", tmp.ID})
			}
			first = false
			ev.Sign(sk)
		}
		status := relay.Publish(context.Background(), ev)
		if verbose {
			fmt.Println(relay.URL, status)
		}
		if status != nostr.PublishStatusFailed {
			success++
		}
		return true
	})
	if success == 0 {
		return errors.New("cannot repost")
	}
	return nil
}

func doLike(cCtx *cli.Context) error {
	verbose := cCtx.Bool("verbose")
	id := cCtx.String("id")

	cfg := cCtx.App.Metadata["config"].(*Config)

	ev := nostr.Event{}
	var sk string
	if _, s, err := nip19.Decode(cfg.PrivateKey); err != nil {
		return err
	} else {
		sk = s.(string)
	}
	if pub, err := nostr.GetPublicKey(sk); err == nil {
		if _, err := nip19.EncodePublicKey(pub); err != nil {
			return err
		}
		ev.PubKey = pub
	} else {
		return err
	}

	if _, tmp, err := nip19.Decode(id); err == nil {
		ev.Tags = ev.Tags.AppendUnique(nostr.Tag{"e", tmp.(string)})
	} else {
		ev.Tags = ev.Tags.AppendUnique(nostr.Tag{"e", id})
	}
	filter := nostr.Filter{
		Kinds: []int{nostr.KindTextNote},
		IDs:   []string{id},
	}

	ev.CreatedAt = time.Now()
	ev.Kind = nostr.KindReaction
	ev.Content = "+"

	success := 0
	first := true
	cfg.Do(true, func(relay *nostr.Relay) bool {
		if first {
			for _, tmp := range relay.QuerySync(context.Background(), filter) {
				ev.Tags = ev.Tags.AppendUnique(nostr.Tag{"p", tmp.ID})
			}
			ev.Sign(sk)
		}
		status := relay.Publish(context.Background(), ev)
		if verbose {
			fmt.Println(relay.URL, status)
		}
		if status != nostr.PublishStatusFailed {
			success++
		}
		return true
	})
	if success == 0 {
		return errors.New("cannot like")
	}
	return nil
}

func doDelete(cCtx *cli.Context) error {
	verbose := cCtx.Bool("verbose")
	id := cCtx.String("id")

	cfg := cCtx.App.Metadata["config"].(*Config)

	ev := nostr.Event{}
	var sk string
	if _, s, err := nip19.Decode(cfg.PrivateKey); err != nil {
		return err
	} else {
		sk = s.(string)
	}
	if pub, err := nostr.GetPublicKey(sk); err == nil {
		if _, err := nip19.EncodePublicKey(pub); err != nil {
			return err
		}
		ev.PubKey = pub
	} else {
		return err
	}

	if _, tmp, err := nip19.Decode(id); err == nil {
		ev.Tags = ev.Tags.AppendUnique(nostr.Tag{"e", tmp.(string)})
	} else {
		ev.Tags = ev.Tags.AppendUnique(nostr.Tag{"e", id})
	}

	ev.CreatedAt = time.Now()
	ev.Kind = nostr.KindDeletion
	ev.Content = "+"
	ev.Sign(sk)

	success := 0
	cfg.Do(true, func(relay *nostr.Relay) bool {
		status := relay.Publish(context.Background(), ev)
		if verbose {
			fmt.Println(relay.URL, status)
		}
		if status != nostr.PublishStatusFailed {
			success++
		}
		return true
	})
	if success == 0 {
		return errors.New("cannot delete")
	}
	return nil
}

func doTimeline(cCtx *cli.Context) error {
	verbose := cCtx.Bool("verbose")
	n := cCtx.Int("n")
	j := cCtx.Bool("json")
	extra := cCtx.Bool("extra")

	cfg := cCtx.App.Metadata["config"].(*Config)
	relay := cfg.FindRelay(false)
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
		if verbose {
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
		if err := cfg.save(); err != nil {
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
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
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
			color.Set(color.Reset)
			fmt.Print(": ")
			color.Set(color.FgHiBlue)
			fmt.Println(ev.PubKey)
			color.Set(color.Reset)
			fmt.Println(ev.Content)
		}
	}

	return nil
}

func main() {
	app := &cli.App{
		Description: "A cli application for nostr",
		Flags: []cli.Flag{
			&cli.StringFlag{Name: "a", Usage: "profile name"},
			&cli.BoolFlag{Name: "V", Usage: "verbose"},
		},
		Commands: []*cli.Command{
			{
				Name:    "timeline",
				Aliases: []string{"tl"},
				Usage:   "show timeline",
				Flags: []cli.Flag{
					&cli.IntFlag{Name: "n", Value: 30, Usage: "number of items"},
					&cli.BoolFlag{Name: "json", Usage: "output JSON"},
					&cli.BoolFlag{Name: "extra", Usage: "extra JSON"},
				},
				Action: doTimeline,
			},
			{
				Name:    "post",
				Aliases: []string{"n"},
				Flags: []cli.Flag{
					&cli.BoolFlag{Name: "stdin"},
				},
				Usage:     "post new note",
				UsageText: "algia post [note text]",
				HelpName:  "post",
				ArgsUsage: "[note text]",
				Action:    doPost,
			},
			{
				Name:    "reply",
				Aliases: []string{"r"},
				Flags: []cli.Flag{
					&cli.BoolFlag{Name: "stdin"},
					&cli.StringFlag{Name: "id", Required: true},
					&cli.BoolFlag{Name: "quote"},
				},
				Usage:     "reply to the note",
				UsageText: "algia reply --id [id] [note text]",
				HelpName:  "reply",
				ArgsUsage: "[note text]",
				Action:    doReply,
			},
			{
				Name:    "repost",
				Aliases: []string{"b"},
				Flags: []cli.Flag{
					&cli.StringFlag{Name: "id", Required: true},
				},
				Usage:     "repost the note",
				UsageText: "algia repost --id [id]",
				HelpName:  "repost",
				Action:    doRepost,
			},
			{
				Name:    "like",
				Aliases: []string{"l"},
				Flags: []cli.Flag{
					&cli.StringFlag{Name: "id", Required: true},
				},
				Usage:     "like the note",
				UsageText: "algia like --id [id]",
				HelpName:  "lite",
				Action:    doLike,
			},
			{
				Name:    "delete",
				Aliases: []string{"d"},
				Flags: []cli.Flag{
					&cli.StringFlag{Name: "id", Required: true},
				},
				Usage:     "delete the note",
				UsageText: "algia delete --id [id]",
				HelpName:  "delete",
				Action:    doDelete,
			},
		},
		Before: func(cCtx *cli.Context) error {
			profile := cCtx.String("a")
			cfg, err := loadConfig(profile)
			if err != nil {
				return err
			}
			cCtx.App.Metadata = map[string]any{
				"config": cfg,
			}
			return nil
		},
	}

	if err := app.Run(os.Args); err != nil {
		log.Fatal(err)
	}
}
