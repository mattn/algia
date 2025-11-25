package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"

	"github.com/mdp/qrterminal/v3"
	"github.com/urfave/cli/v2"

	"github.com/nbd-wtf/go-nostr"
	"github.com/nbd-wtf/go-nostr/nip04"
	"github.com/nbd-wtf/go-nostr/nip19"
)

// Lnurlp is
type Lnurlp struct {
	Callback       string `json:"callback"`
	MaxSendable    int64  `json:"maxSendable"`
	MinSendable    int    `json:"minSendable"`
	Metadata       string `json:"metadata"`
	CommentAllowed int    `json:"commentAllowed"`
	Tag            string `json:"tag"`
	AllowsNostr    bool   `json:"allowsNostr"`
	NostrPubkey    string `json:"nostrPubkey"`
}

// Invoice is
type Invoice struct {
	PR string `json:"pr"`
}

// PayRequest is
type PayRequest struct {
	Method string `json:"method"`
	Params struct {
		Invoice string   `json:"invoice"`
		Routes  []string `json:"routes:"`
	} `json:"params"`
}

// PayResponse is
type PayResponse struct {
	ResultType *string `json:"result_type"`
	Err        *struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
	Result *struct {
		Preimage string `json:"preimage"`
	} `json:"result"`
}

func pay(cfg *Config, invoice string) error {
	uri, err := url.Parse(cfg.NwcURI)
	if err != nil {
		return err
	}
	wallet := uri.Host
	host := uri.Query().Get("relay")
	secret := uri.Query().Get("secret")
	pub, err := nostr.GetPublicKey(secret)
	if err != nil {
		return err
	}

	relay, err := nostr.RelayConnect(context.Background(), host)
	if err != nil {
		return err
	}
	defer relay.Close()

	ss, err := nip04.ComputeSharedSecret(wallet, secret)
	if err != nil {
		return err
	}
	var req PayRequest
	req.Method = "pay_invoice"
	req.Params.Invoice = invoice
	b, err := json.Marshal(req)
	if err != nil {
		return err
	}
	content, err := nip04.Encrypt(string(b), ss)
	if err != nil {
		return err
	}

	ev := nostr.Event{
		PubKey:    pub,
		CreatedAt: nostr.Now(),
		Kind:      nostr.KindNWCWalletRequest,
		Tags:      nostr.Tags{nostr.Tag{"p", wallet}},
		Content:   content,
	}
	err = ev.Sign(secret)
	if err != nil {
		return err
	}

	filters := []nostr.Filter{{
		Tags: nostr.TagMap{
			"p": []string{pub},
			"e": []string{ev.ID},
		},
		Kinds: []int{nostr.KindNWCWalletInfo, nostr.KindNWCWalletResponse, nostr.KindNWCWalletRequest},
		Limit: 1,
	}}
	sub, err := relay.Subscribe(context.Background(), filters)
	if err != nil {
		return err
	}

	err = relay.Publish(context.Background(), ev)
	if err != nil {
		return err
	}

	er := <-sub.Events
	content, err = nip04.Decrypt(er.Content, ss)
	if err != nil {
		return err
	}
	var resp PayResponse
	err = json.Unmarshal([]byte(content), &resp)
	if err != nil {
		return err
	}
	if resp.Err != nil {
		return fmt.Errorf(resp.Err.Message)
	}
	json.NewEncoder(os.Stdout).Encode(resp)
	return nil
}

// ZapInfo is
func (cfg *Config) ZapInfo(pub string) (*Lnurlp, error) {
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
		return nil, err
	}
	if len(evs) == 0 {
		return nil, errors.New("cannot find user")
	}

	var profile Profile
	err = json.Unmarshal([]byte(evs[0].Content), &profile)
	if err != nil {
		return nil, err
	}

	tok := strings.SplitN(profile.Lud16, "@", 2)
	if err != nil {
		return nil, err
	}
	if len(tok) != 2 {
		return nil, errors.New("receipt address is not valid")
	}

	resp, err := http.Get("https://" + tok[1] + "/.well-known/lnurlp/" + tok[0])
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var lp Lnurlp
	err = json.NewDecoder(resp.Body).Decode(&lp)
	if err != nil {
		return nil, err
	}
	return &lp, nil
}

func doZap(cCtx *cli.Context) error {
	if cCtx.Args().Len() == 0 {
		return cli.ShowSubcommandHelp(cCtx)
	}
	return callZap(&zapArg{
		cfg:     cCtx.App.Metadata["config"].(*Config),
		amount:  cCtx.Uint64("amount"),
		comment: cCtx.String("comment"),
		id:      cCtx.Args().First(),
	})
}

type zapArg struct {
	cfg     *Config
	amount  uint64
	comment string
	id      string
}

func callZap(arg *zapArg) error {
	var sk string
	if _, s, err := nip19.Decode(arg.cfg.PrivateKey); err == nil {
		sk = s.(string)
	} else {
		return err
	}

	receipt := ""
	zr := nostr.Event{}
	zr.Tags = nostr.Tags{}
	clientTag(&zr)

	if pub, err := nostr.GetPublicKey(sk); err == nil {
		if _, err := nip19.EncodePublicKey(pub); err != nil {
			return err
		}
		zr.PubKey = pub
	} else {
		return err
	}

	zr.Tags = zr.Tags.AppendUnique(nostr.Tag{"amount", fmt.Sprint(arg.amount * 1000)})
	relays := nostr.Tag{"relays"}
	for k, v := range arg.cfg.Relays {
		if v.Write {
			relays = append(relays, k)
		}
	}
	zr.Tags = zr.Tags.AppendUnique(relays)
	if prefix, s, err := nip19.Decode(arg.id); err == nil {
		switch prefix {
		case "nevent":
			receipt = s.(nostr.EventPointer).Author
			zr.Tags = zr.Tags.AppendUnique(nostr.Tag{"p", receipt})
			zr.Tags = zr.Tags.AppendUnique(nostr.Tag{"e", s.(nostr.EventPointer).ID})
		case "note":
			filters := nostr.Filters{
				{
					IDs: []string{s.(string)},
				},
			}
			evs, err := arg.cfg.QueryEvents(filters)
			if err != nil {
				return err
			}
			if len(evs) != 0 {
				receipt = evs[0].PubKey
				zr.Tags = zr.Tags.AppendUnique(nostr.Tag{"p", receipt})
			}
			zr.Tags = zr.Tags.AppendUnique(nostr.Tag{"e", s.(string)})
		case "npub":
			receipt = s.(string)
			zr.Tags = zr.Tags.AppendUnique(nostr.Tag{"p", receipt})
		default:
			return errors.New("invalid argument")
		}
	}

	zr.Kind = nostr.KindZapRequest // 9734
	zr.CreatedAt = nostr.Now()
	zr.Content = arg.comment
	if err := zr.Sign(sk); err != nil {
		return err
	}
	b, err := zr.MarshalJSON()
	if err != nil {
		return err
	}

	zi, err := arg.cfg.ZapInfo(receipt)
	if err != nil {
		return err
	}
	u, err := url.Parse(zi.Callback)
	if err != nil {
		return err
	}
	param := url.Values{}
	param.Set("amount", fmt.Sprint(arg.amount*1000))
	param.Set("nostr", string(b))
	u.RawQuery = param.Encode()
	resp, err := http.Get(u.String())
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	var iv Invoice
	err = json.NewDecoder(resp.Body).Decode(&iv)
	if err != nil {
		return err
	}

	if arg.cfg.NwcURI == "" {
		config := qrterminal.Config{
			HalfBlocks: false,
			Level:      qrterminal.L,
			Writer:     os.Stdout,
			WhiteChar:  qrterminal.WHITE,
			BlackChar:  qrterminal.BLACK,
			QuietZone:  2,
			WithSixel:  true,
		}
		fmt.Println("lightning:" + iv.PR)
		qrterminal.GenerateWithConfig("lightning:"+iv.PR, config)
	} else {
		pay(arg.cfg, iv.PR)
	}
	return nil
}
