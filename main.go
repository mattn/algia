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

func loadConfig() (*Config, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return nil, err
	}

	fp := filepath.Join(dir, "algia")
	os.MkdirAll(fp, 0700)
	fp = filepath.Join(fp, "config.json")

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

func (cfg *Config) findRelay() *nostr.Relay {
	for k := range cfg.Relays {
		ctx := context.WithValue(context.Background(), "url", k)
		relay, err := nostr.RelayConnect(ctx, k)
		if err == nil {
			return relay
		}
	}
	return nil
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

func newNote(cCtx *cli.Context) error {
	stdin := cCtx.Bool("stdin")
	rid := cCtx.String("rid")
	qid := cCtx.String("qid")

	cfg := cCtx.App.Metadata["config"].(*Config)
	relay := cfg.findRelay()
	if relay == nil {
		return errors.New("cannot connect relays")
	}
	defer relay.Close()

	var sk string
	if _, s, err := nip19.Decode(cfg.PrivateKey); err != nil {
		return err
	} else {
		sk = s.(string)
	}
	ev := nostr.Event{}
	if pub, err := nostr.GetPublicKey(sk); err == nil {
		ev.PubKey = pub
		if _, err := nip19.EncodePublicKey(pub); err != nil {
			return err
		}
	} else {
		return err
	}

	if rid != "" {
		ev.Tags = ev.Tags.AppendUnique(nostr.Tag{"e", rid, relay.URL, "reply"})
	}
	if qid != "" {
		ev.Tags = ev.Tags.AppendUnique(nostr.Tag{"e", qid, relay.URL, "mention"})
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
	ev.Sign(sk)
	status := relay.Publish(context.Background(), ev)
	fmt.Println(status)

	return nil
}

func boost(cCtx *cli.Context) error {
	id := cCtx.String("id")
	pid := cCtx.String("pid")

	cfg := cCtx.App.Metadata["config"].(*Config)
	relay := cfg.findRelay()
	if relay == nil {
		return errors.New("cannot connect relays")
	}
	defer relay.Close()

	ev := nostr.Event{}
	ev.Tags = ev.Tags.AppendUnique(nostr.Tag{"e", id})
	var sk string
	if _, s, err := nip19.Decode(cfg.PrivateKey); err != nil {
		return err
	} else {
		sk = s.(string)
	}
	// FIXME
	ev.Tags = ev.Tags.AppendUnique(nostr.Tag{"p", pid})
	ev.CreatedAt = time.Now()
	ev.Kind = nostr.KindBoost
	ev.Content = ""
	ev.Sign(sk)
	status := relay.Publish(context.Background(), ev)
	fmt.Println(status)

	return nil
}

func vote(cCtx *cli.Context) error {
	id := cCtx.String("id")
	pid := cCtx.String("pid")

	cfg := cCtx.App.Metadata["config"].(*Config)
	relay := cfg.findRelay()
	if relay == nil {
		return errors.New("cannot connect relays")
	}
	defer relay.Close()

	ev := nostr.Event{}
	ev.Tags = ev.Tags.AppendUnique(nostr.Tag{"e", id})
	var sk string
	if _, s, err := nip19.Decode(cfg.PrivateKey); err != nil {
		return err
	} else {
		sk = s.(string)
	}
	// FIXME
	ev.Tags = ev.Tags.AppendUnique(nostr.Tag{"p", pid})
	ev.CreatedAt = time.Now()
	ev.Kind = nostr.KindReaction
	ev.Content = "+"
	ev.Sign(sk)
	status := relay.Publish(context.Background(), ev)
	fmt.Println(status)

	return nil
}

func timeline(cCtx *cli.Context) error {
	j := cCtx.Bool("json")

	cfg := cCtx.App.Metadata["config"].(*Config)
	relay := cfg.findRelay()
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
		println(len(follows))
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
		Limit:   30,
	})
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	sub := relay.Subscribe(ctx, filters)
	go func() {
		<-sub.EndOfStoredEvents
		cancel()
	}()

	if j {
		for ev := range sub.Events {
			profile, ok := cfg.Follows[ev.PubKey]
			if ok {
				eev := Event{
					Event:   ev,
					Profile: profile,
				}
				if prefix, value, err := nip19.Decode(ev.PubKey); err != nil {
					log.Fatal(err)
				} else {
					println(prefix + value.(string))
				}
				json.NewEncoder(os.Stdout).Encode(eev)
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
	cfg, err := loadConfig()
	if err != nil {
		log.Fatal(err)
	}

	app := &cli.App{
		Commands: []*cli.Command{
			{
				Name:    "note",
				Aliases: []string{"n"},
				Usage:   "post new note",
				Flags: []cli.Flag{
					&cli.BoolFlag{Name: "stdin"},
					&cli.StringFlag{Name: "rid"},
					&cli.StringFlag{Name: "qid"},
				},
				Action: newNote,
			},
			{
				Name:    "timeline",
				Aliases: []string{"tl"},
				Usage:   "show timeline",
				Flags: []cli.Flag{
					&cli.BoolFlag{Name: "json"},
				},
				Action: timeline,
			},
			{
				Name:    "boost",
				Aliases: []string{"b"},
				Usage:   "boost the note",
				Flags: []cli.Flag{
					&cli.StringFlag{Name: "id", Required: true},
					&cli.StringFlag{Name: "pid", Required: true},
				},
				Action: boost,
			},
			{
				Name:    "vote",
				Aliases: []string{"v"},
				Usage:   "vote the note",
				Flags: []cli.Flag{
					&cli.StringFlag{Name: "id", Required: true},
					&cli.StringFlag{Name: "pid", Required: true},
				},
				Action: vote,
			},
		},
	}
	app.Metadata = map[string]interface{}{
		"config": cfg,
	}

	if err := app.Run(os.Args); err != nil {
		log.Fatal(err)
	}
}
