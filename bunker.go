package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand"
	"os"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/nbd-wtf/go-nostr"
	"github.com/nbd-wtf/go-nostr/nip04"
	"github.com/nbd-wtf/go-nostr/nip44"
)

// bunkerClient is a minimal NIP-46 client. go-nostr ships one, but in v0.52
// its auth_url challenges never reach the caller and keyer.BunkerSigner's
// Decrypt calls nip44_encrypt, so algia carries its own.
type bunkerClient struct {
	pool     *nostr.SimplePool
	cancel   context.CancelFunc
	clientSk string
	clientPk string
	remote   string
	relays   []string
	ck       [32]byte // NIP-44 conversation key with the remote signer
	ss       []byte   // NIP-04 shared secret (fallback for old signers)
	idPrefix string
	serial   atomic.Uint64

	mu      sync.Mutex
	waiters map[string]chan bunkerResponse
}

type bunkerRequest struct {
	ID     string   `json:"id"`
	Method string   `json:"method"`
	Params []string `json:"params"`
}

type bunkerResponse struct {
	ID     string `json:"id"`
	Result string `json:"result"`
	Error  string `json:"error,omitempty"`
}

// bunkerSubscribeWait bounds how long we wait for the response subscription to
// be live on the relays before sending the first request.
const bunkerSubscribeWait = 5 * time.Second

func newBunkerClient(ctx context.Context, clientSk, remote string, relays []string) (*bunkerClient, error) {
	clientPk, err := nostr.GetPublicKey(clientSk)
	if err != nil {
		return nil, err
	}
	ck, err := nip44.GenerateConversationKey(remote, clientSk)
	if err != nil {
		return nil, err
	}
	ss, err := nip04.ComputeSharedSecret(remote, clientSk)
	if err != nil {
		return nil, err
	}

	ctx, cancel := context.WithCancel(ctx)
	bc := &bunkerClient{
		pool:     nostr.NewSimplePool(ctx),
		cancel:   cancel,
		clientSk: clientSk,
		clientPk: clientPk,
		remote:   remote,
		relays:   relays,
		ck:       ck,
		ss:       ss,
		idPrefix: "algia-" + strconv.Itoa(rand.Intn(1<<20)),
		waiters:  map[string]chan bunkerResponse{},
	}

	// Subscribe before sending anything so no response can be missed.
	now := nostr.Now()
	eose := make(chan struct{})
	events := bc.pool.SubscribeManyNotifyEOSE(ctx, relays, nostr.Filter{
		Kinds:   []int{nostr.KindNostrConnect},
		Authors: []string{remote},
		Tags:    nostr.TagMap{"p": []string{clientPk}},
		Since:   &now,
	}, eose)
	go bc.listen(events)
	select {
	case <-eose:
	case <-time.After(bunkerSubscribeWait):
	}
	return bc, nil
}

func (bc *bunkerClient) close() {
	bc.cancel()
}

func (bc *bunkerClient) listen(events chan nostr.RelayEvent) {
	for ie := range events {
		if ie.Event == nil || ie.PubKey != bc.remote {
			continue
		}
		plain, err := nip44.Decrypt(ie.Content, bc.ck)
		if err != nil {
			if plain, err = nip04.Decrypt(ie.Content, bc.ss); err != nil {
				continue
			}
		}
		var resp bunkerResponse
		if err := json.Unmarshal([]byte(plain), &resp); err != nil {
			continue
		}
		bc.mu.Lock()
		ch, ok := bc.waiters[resp.ID]
		bc.mu.Unlock()
		if ok {
			// Responses can arrive more than once (one per relay); never block.
			select {
			case ch <- resp:
			default:
			}
		}
	}
}

// rpc sends a request to the remote signer and waits for its response. An
// auth_url response is shown to the user and the wait continues, as NIP-46
// specifies that the real response follows once the user has approved.
func (bc *bunkerClient) rpc(ctx context.Context, method string, params []string) (string, error) {
	if params == nil {
		params = []string{}
	}
	id := bc.idPrefix + "-" + strconv.FormatUint(bc.serial.Add(1), 10)
	req, err := json.Marshal(bunkerRequest{ID: id, Method: method, Params: params})
	if err != nil {
		return "", err
	}
	content, err := nip44.Encrypt(string(req), bc.ck)
	if err != nil {
		return "", err
	}
	ev := nostr.Event{
		Kind:      nostr.KindNostrConnect,
		CreatedAt: nostr.Now(),
		Tags:      nostr.Tags{{"p", bc.remote}},
		Content:   content,
	}
	if err := ev.Sign(bc.clientSk); err != nil {
		return "", err
	}

	ch := make(chan bunkerResponse, 4)
	bc.mu.Lock()
	bc.waiters[id] = ch
	bc.mu.Unlock()
	defer func() {
		bc.mu.Lock()
		delete(bc.waiters, id)
		bc.mu.Unlock()
	}()

	published := false
	var lastErr error
	for res := range bc.pool.PublishMany(ctx, bc.relays, ev) {
		if res.Error == nil {
			published = true
		} else {
			lastErr = fmt.Errorf("%s: %w", res.RelayURL, res.Error)
		}
	}
	if !published {
		if lastErr == nil {
			lastErr = errors.New("no relays")
		}
		return "", fmt.Errorf("cannot send request to remote signer: %w", lastErr)
	}

	shownAuth := map[string]bool{}
	for {
		select {
		case <-ctx.Done():
			return "", fmt.Errorf("%s: no response from remote signer: %w", method, ctx.Err())
		case resp := <-ch:
			if resp.Result == "auth_url" {
				if !shownAuth[resp.Error] {
					shownAuth[resp.Error] = true
					fmt.Fprintln(os.Stderr, "remote signer requests authorization, open:", resp.Error)
				}
				continue
			}
			if resp.Error != "" {
				return "", errors.New(resp.Error)
			}
			return resp.Result, nil
		}
	}
}
