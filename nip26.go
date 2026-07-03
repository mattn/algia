package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/btcsuite/btcd/btcec/v2"
	"github.com/btcsuite/btcd/btcec/v2/schnorr"
	"github.com/nbd-wtf/go-nostr"
	"github.com/nbd-wtf/go-nostr/nip19"
	"github.com/urfave/cli/v2"
)

// Delegation is
type Delegation struct {
	Delegator  string `json:"delegator"`
	Conditions string `json:"conditions"`
	Token      string `json:"token"`
}

type delegationConditions struct {
	kinds  []int
	after  int64
	before int64
}

func parseDelegationConditions(q string) (*delegationConditions, error) {
	c := &delegationConditions{}
	for _, cond := range strings.Split(q, "&") {
		switch {
		case strings.HasPrefix(cond, "kind="):
			kind, err := strconv.Atoi(cond[len("kind="):])
			if err != nil {
				return nil, fmt.Errorf("invalid delegation condition %q", cond)
			}
			c.kinds = append(c.kinds, kind)
		case strings.HasPrefix(cond, "created_at>"):
			ts, err := strconv.ParseInt(cond[len("created_at>"):], 10, 64)
			if err != nil {
				return nil, fmt.Errorf("invalid delegation condition %q", cond)
			}
			c.after = ts
		case strings.HasPrefix(cond, "created_at<"):
			ts, err := strconv.ParseInt(cond[len("created_at<"):], 10, 64)
			if err != nil {
				return nil, fmt.Errorf("invalid delegation condition %q", cond)
			}
			c.before = ts
		default:
			return nil, fmt.Errorf("unsupported delegation condition %q", cond)
		}
	}
	return c, nil
}

func (c *delegationConditions) allow(kind int, createdAt int64) error {
	if len(c.kinds) > 0 {
		found := false
		for _, k := range c.kinds {
			if k == kind {
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("delegation does not permit kind=%d", kind)
		}
	}
	if c.after != 0 && createdAt <= c.after {
		return fmt.Errorf("delegation is not valid before %v", time.Unix(c.after, 0))
	}
	if c.before != 0 && createdAt >= c.before {
		return fmt.Errorf("delegation expired at %v", time.Unix(c.before, 0))
	}
	return nil
}

func delegationHash(delegateePub, conditions string) []byte {
	h := sha256.Sum256([]byte("nostr:delegation:" + delegateePub + ":" + conditions))
	return h[:]
}

func createDelegationToken(delegatorSk, delegateePub, conditions string) (string, error) {
	b, err := hex.DecodeString(delegatorSk)
	if err != nil {
		return "", err
	}
	priv, _ := btcec.PrivKeyFromBytes(b)
	sig, err := schnorr.Sign(priv, delegationHash(delegateePub, conditions))
	if err != nil {
		return "", err
	}
	return hex.EncodeToString(sig.Serialize()), nil
}

func verifyDelegationToken(delegatorPub, delegateePub, conditions, token string) bool {
	pb, err := hex.DecodeString(delegatorPub)
	if err != nil {
		return false
	}
	pub, err := schnorr.ParsePubKey(pb)
	if err != nil {
		return false
	}
	sb, err := hex.DecodeString(token)
	if err != nil {
		return false
	}
	sig, err := schnorr.ParseSignature(sb)
	if err != nil {
		return false
	}
	return sig.Verify(delegationHash(delegateePub, conditions), pub)
}

// signEvent signs ev with the profile's private key. When the profile is a
// delegated one, the delegation conditions are enforced and the delegation
// tag is attached before signing.
func (cfg *Config) signEvent(ev *nostr.Event) error {
	var sk string
	if _, s, err := nip19.Decode(cfg.PrivateKey); err == nil {
		sk = s.(string)
	} else {
		return err
	}
	if d := cfg.Delegation; d != nil {
		c, err := parseDelegationConditions(d.Conditions)
		if err != nil {
			return err
		}
		createdAt := ev.CreatedAt
		if createdAt == 0 {
			createdAt = nostr.Now()
			ev.CreatedAt = createdAt
		}
		if err := c.allow(ev.Kind, int64(createdAt)); err != nil {
			return err
		}
		ev.Tags = append(ev.Tags, nostr.Tag{"delegation", d.Delegator, d.Conditions, d.Token})
	}
	return ev.Sign(sk)
}

// delegationDisplayPubKey returns the delegator's public key when ev carries
// a valid NIP-26 delegation tag, otherwise the event's own public key.
func delegationDisplayPubKey(ev *nostr.Event) (string, bool) {
	tag := ev.Tags.GetFirst([]string{"delegation"})
	if tag == nil || len(*tag) < 4 {
		return ev.PubKey, false
	}
	delegator, conditions, token := (*tag)[1], (*tag)[2], (*tag)[3]
	c, err := parseDelegationConditions(conditions)
	if err != nil {
		return ev.PubKey, false
	}
	if err := c.allow(ev.Kind, int64(ev.CreatedAt)); err != nil {
		return ev.PubKey, false
	}
	if !verifyDelegationToken(delegator, ev.PubKey, conditions, token) {
		return ev.PubKey, false
	}
	return delegator, true
}

func delegationCommand() *cli.Command {
	return &cli.Command{
		Name:  "delegation",
		Usage: "manage NIP-26 delegated signing",
		Subcommands: []*cli.Command{
			{
				Name: "create",
				Flags: []cli.Flag{
					&cli.StringFlag{Name: "profile", Usage: "profile name to create for the delegatee"},
					&cli.StringFlag{Name: "kinds", Value: "1,6,7", Usage: "comma separated kinds to permit"},
					&cli.IntFlag{Name: "days", Value: 30, Usage: "days until the delegation expires"},
					&cli.StringFlag{Name: "delegatee", Usage: "npub of an external delegatee (print token as JSON instead of creating a profile)"},
				},
				Usage:     "delegate signing to a new keypair",
				UsageText: "algia delegation create --profile [name]",
				HelpName:  "create",
				Action:    doDelegationCreate,
			},
			{
				Name:      "show",
				Usage:     "show delegation status of current profile",
				UsageText: "algia -a [name] delegation show",
				HelpName:  "show",
				Action:    doDelegationShow,
			},
			{
				Name:      "import",
				Usage:     "import delegation JSON from stdin into current profile",
				UsageText: "algia -a [name] delegation import < delegation.json",
				HelpName:  "import",
				Action:    doDelegationImport,
			},
		},
	}
}

func doDelegationCreate(cCtx *cli.Context) error {
	cfg := cCtx.App.Metadata["config"].(*Config)
	if cfg.Delegation != nil {
		return errors.New("current profile is already delegated: cannot delegate again")
	}

	var delegatorSk string
	if _, s, err := nip19.Decode(cfg.PrivateKey); err == nil {
		delegatorSk = s.(string)
	} else {
		return err
	}
	delegatorPub, err := nostr.GetPublicKey(delegatorSk)
	if err != nil {
		return err
	}

	days := cCtx.Int("days")
	if days <= 0 {
		return errors.New("--days must be positive")
	}
	conditions := []string{}
	for _, s := range strings.Split(cCtx.String("kinds"), ",") {
		kind, err := strconv.Atoi(strings.TrimSpace(s))
		if err != nil {
			return fmt.Errorf("invalid kind %q", s)
		}
		conditions = append(conditions, fmt.Sprintf("kind=%d", kind))
	}
	now := time.Now().Unix()
	conditions = append(conditions,
		fmt.Sprintf("created_at>%d", now),
		fmt.Sprintf("created_at<%d", now+int64(days)*86400))
	conds := strings.Join(conditions, "&")

	var delegateePub, delegateeNsec string
	if npub := cCtx.String("delegatee"); npub != "" {
		if prefix, s, err := nip19.Decode(npub); err == nil && prefix == "npub" {
			delegateePub = s.(string)
		} else {
			return fmt.Errorf("invalid delegatee %q", npub)
		}
	} else {
		delegateeSk := nostr.GeneratePrivateKey()
		if delegateePub, err = nostr.GetPublicKey(delegateeSk); err != nil {
			return err
		}
		if delegateeNsec, err = nip19.EncodePrivateKey(delegateeSk); err != nil {
			return err
		}
	}

	token, err := createDelegationToken(delegatorSk, delegateePub, conds)
	if err != nil {
		return err
	}
	delegation := Delegation{
		Delegator:  delegatorPub,
		Conditions: conds,
		Token:      token,
	}

	// External delegatee: print the delegation for `delegation import`
	if delegateeNsec == "" {
		return json.NewEncoder(os.Stdout).Encode(delegation)
	}

	profile := cCtx.String("profile")
	if profile == "" {
		return errors.New("--profile is required")
	}
	dir, err := configDir()
	if err != nil {
		return err
	}
	fp := filepath.Join(dir, "algia", "config-"+profile+".json")
	if _, err := os.Stat(fp); err == nil {
		return fmt.Errorf("profile already exists: %s", fp)
	}

	ncfg := Config{
		Relays:     cfg.Relays,
		FollowList: cfg.FollowList,
		Updated:    cfg.Updated,
		PrivateKey: delegateeNsec,
		Delegation: &delegation,
	}
	if err := ncfg.saveConfig(profile); err != nil {
		return err
	}

	delegateeNpub, _ := nip19.EncodePublicKey(delegateePub)
	delegatorNpub, _ := nip19.EncodePublicKey(delegatorPub)
	fmt.Println("delegatee:", delegateeNpub)
	fmt.Println("delegator:", delegatorNpub)
	fmt.Println("conditions:", conds)
	fmt.Println("wrote", fp)
	return nil
}

func doDelegationShow(cCtx *cli.Context) error {
	cfg := cCtx.App.Metadata["config"].(*Config)
	d := cfg.Delegation
	if d == nil {
		fmt.Println("no delegation configured for this profile")
		return nil
	}

	if npub, err := nip19.EncodePublicKey(d.Delegator); err == nil {
		fmt.Println("delegator:", npub)
	} else {
		fmt.Println("delegator:", d.Delegator)
	}

	c, err := parseDelegationConditions(d.Conditions)
	if err != nil {
		return err
	}
	kinds := []string{}
	for _, k := range c.kinds {
		kinds = append(kinds, strconv.Itoa(k))
	}
	fmt.Println("kinds:", strings.Join(kinds, ", "))
	if c.before != 0 {
		expire := time.Unix(c.before, 0)
		if left := time.Until(expire); left > 0 {
			fmt.Printf("expires: %v (%d days left)\n", expire, int(left.Hours()/24))
		} else {
			fmt.Printf("expires: %v (EXPIRED)\n", expire)
		}
	} else {
		fmt.Println("expires: never")
	}

	var delegateePub string
	if _, s, err := nip19.Decode(cfg.PrivateKey); err == nil {
		if delegateePub, err = nostr.GetPublicKey(s.(string)); err != nil {
			return err
		}
	} else {
		return err
	}
	if verifyDelegationToken(d.Delegator, delegateePub, d.Conditions, d.Token) {
		fmt.Println("token: valid")
	} else {
		fmt.Println("token: INVALID")
	}
	return nil
}

func doDelegationImport(cCtx *cli.Context) error {
	cfg := cCtx.App.Metadata["config"].(*Config)
	profile := cCtx.App.Metadata["profile"].(string)

	var d Delegation
	if err := json.NewDecoder(os.Stdin).Decode(&d); err != nil {
		return err
	}
	if _, err := parseDelegationConditions(d.Conditions); err != nil {
		return err
	}
	var delegateePub string
	if _, s, err := nip19.Decode(cfg.PrivateKey); err == nil {
		if delegateePub, err = nostr.GetPublicKey(s.(string)); err != nil {
			return err
		}
	} else {
		return err
	}
	if !verifyDelegationToken(d.Delegator, delegateePub, d.Conditions, d.Token) {
		return errors.New("delegation token is not valid for this profile's key")
	}
	cfg.Delegation = &d
	if err := cfg.saveConfig(profile); err != nil {
		return err
	}
	fmt.Println("delegation imported")
	return nil
}
