package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync/atomic"
	"time"

	"github.com/nbd-wtf/go-nostr"
	"github.com/nbd-wtf/go-nostr/nip19"
	"github.com/urfave/cli/v2"
)

func doDelete(cCtx *cli.Context) error {
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

	var success atomic.Int64
	cfg.Do(Relay{Write: true}, func(relay *nostr.Relay) {
		status := relay.Publish(context.Background(), ev)
		if cfg.verbose {
			fmt.Fprintln(os.Stderr, relay.URL, status)
		}
		if status != nostr.PublishStatusFailed {
			success.Add(1)
		}
	})
	if success.Load() == 0 {
		return errors.New("cannot delete")
	}
	return nil
}
