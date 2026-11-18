package main

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/nbd-wtf/go-nostr"
	"github.com/nbd-wtf/go-nostr/keyer"
	"github.com/nbd-wtf/go-nostr/nip04"
	"github.com/nbd-wtf/go-nostr/nip05"
	"github.com/nbd-wtf/go-nostr/nip19"
)

// Signer performs every operation that needs the user's private key. It is
// backed either by a local key (nsec) or by a NIP-46 remote signer (bunker).
// nostr.Keyer covers signing and NIP-44; NIP-04 is still needed for legacy
// kind 4 DMs and NIP-51 private list items.
type Signer interface {
	nostr.Keyer
	NIP04Encrypt(ctx context.Context, plaintext, recipient string) (string, error)
	NIP04Decrypt(ctx context.Context, ciphertext, sender string) (string, error)
}

// errNeedLocalKey is returned by operations that cannot be delegated to a
// remote signer (e.g. NIP-26 delegation tokens, which sign a raw hash).
var errNeedLocalKey = errors.New("this operation requires a local private key (nsec); it is not supported with a remote signer (bunker)")

// signTimeout bounds each signer operation. It is generous because a remote
// signer may wait for the user to approve the request.
const signTimeout = 2 * time.Minute

// localSigner signs with an in-memory private key.
type localSigner struct {
	keyer.KeySigner
	sk string
}

func newLocalSigner(sk string) (*localSigner, error) {
	ks, err := keyer.NewPlainKeySigner(sk)
	if err != nil {
		return nil, err
	}
	return &localSigner{KeySigner: ks, sk: sk}, nil
}

func (s *localSigner) NIP04Encrypt(_ context.Context, plaintext, recipient string) (string, error) {
	ss, err := nip04.ComputeSharedSecret(recipient, s.sk)
	if err != nil {
		return "", err
	}
	return nip04.Encrypt(plaintext, ss)
}

func (s *localSigner) NIP04Decrypt(_ context.Context, ciphertext, sender string) (string, error) {
	ss, err := nip04.ComputeSharedSecret(sender, s.sk)
	if err != nil {
		return "", err
	}
	return nip04.Decrypt(ciphertext, ss)
}

// isRemoteSigner reports whether the configured key is a NIP-46 remote signer
// reference (bunker:// URL or NIP-05 identifier) rather than a local key.
func isRemoteSigner(key string) bool {
	return strings.HasPrefix(key, "bunker://") || nip05.IsValidIdentifier(key)
}

// decodeLocalKey decodes an nsec into a hex private key.
func decodeLocalKey(key string) (string, error) {
	prefix, s, err := nip19.Decode(key)
	if err != nil {
		return "", err
	}
	if prefix != "nsec" {
		return "", fmt.Errorf("privatekey must be an nsec, an ncryptsec, a bunker:// URL or a NIP-05 identifier, got %q", prefix)
	}
	return s.(string), nil
}

// bunkerSession persists what is needed to reconnect to a remote signer
// without the one-time connect secret: the client key the signer authorized,
// and the user's public key so read-only commands need not contact the signer.
type bunkerSession struct {
	Bunker    string `json:"bunker"`
	ClientKey string `json:"client-key"`
	PubKey    string `json:"pubkey,omitempty"`
}

// bunkerSessionKey identifies the configured signer with the connect secret
// stripped, so the session survives the secret being removed from config.
func bunkerSessionKey(input string) string {
	u, err := url.Parse(input)
	if err != nil || u.Scheme != "bunker" {
		return input
	}
	q := u.Query()
	q.Del("secret")
	u.RawQuery = q.Encode()
	return u.String()
}

func bunkerSessionPath(profile string) (string, error) {
	dir, err := configDir()
	if err != nil {
		return "", err
	}
	name := "bunker-session.json"
	if profile != "" {
		name = "bunker-session-" + profile + ".json"
	}
	return filepath.Join(dir, "algia", name), nil
}

// loadBunkerSession returns the stored session for input, or nil when there is
// none or it belongs to a different signer.
func loadBunkerSession(profile, input string) *bunkerSession {
	fp, err := bunkerSessionPath(profile)
	if err != nil {
		return nil
	}
	b, err := os.ReadFile(fp)
	if err != nil {
		return nil
	}
	var s bunkerSession
	if err := json.Unmarshal(b, &s); err != nil {
		return nil
	}
	if s.Bunker != bunkerSessionKey(input) || !validHexKey(s.ClientKey) {
		return nil
	}
	return &s
}

func saveBunkerSession(profile string, s *bunkerSession) error {
	fp, err := bunkerSessionPath(profile)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(fp), 0700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(fp, b, 0600)
}

func validHexKey(s string) bool {
	if len(s) != 64 {
		return false
	}
	_, err := hex.DecodeString(s)
	return err == nil
}

// signer returns the Signer for this profile, connecting to the remote signer
// on first use. The result is memoized for the rest of the run.
func (cfg *Config) signer() (Signer, error) {
	cfg.signerMu.Lock()
	defer cfg.signerMu.Unlock()
	if cfg.signerCache != nil {
		return cfg.signerCache, nil
	}
	var s Signer
	var err error
	if isRemoteSigner(cfg.PrivateKey) {
		s, err = cfg.connectBunker()
	} else if isEncryptedKey(cfg.PrivateKey) {
		var sk string
		if sk, err = decryptKey(cfg.PrivateKey); err == nil {
			var ls *localSigner
			if ls, err = newLocalSigner(sk); err == nil {
				s = ls
				pub, _ := ls.GetPublicKey(context.Background())
				if loadCachedPubkey(cfg.profileName, cfg.PrivateKey) != pub {
					if err := saveCachedPubkey(cfg.profileName, cfg.PrivateKey, pub); err != nil {
						fmt.Fprintln(os.Stderr, "warning: cannot save public key cache:", err)
					}
				}
			}
		}
	} else {
		var sk string
		if sk, err = decodeLocalKey(cfg.PrivateKey); err == nil {
			s, err = newLocalSigner(sk)
		}
	}
	if err != nil {
		return nil, err
	}
	cfg.signerCache = s
	return s, nil
}

// connectBunker connects to the configured NIP-46 remote signer, reusing the
// stored session when there is one.
func (cfg *Config) connectBunker() (Signer, error) {
	ctx, cancel := context.WithTimeout(context.Background(), signTimeout)
	defer cancel()

	remote, relays, secret, err := parseBunkerInput(ctx, cfg.PrivateKey)
	if err != nil {
		return nil, err
	}

	sess := loadBunkerSession(cfg.profileName, cfg.PrivateKey)
	fresh := sess == nil
	if fresh {
		sess = &bunkerSession{
			Bunker:    bunkerSessionKey(cfg.PrivateKey),
			ClientKey: nostr.GeneratePrivateKey(),
		}
	}

	bc, err := newBunkerClient(context.Background(), sess.ClientKey, remote, relays)
	if err != nil {
		return nil, err
	}
	if cfg.verbose {
		fmt.Fprintln(os.Stderr, "connecting to remote signer", remote, relays)
	}
	if fresh {
		if _, err := bc.rpc(ctx, "connect", connectParams(remote, secret)); err != nil {
			bc.close()
			return nil, fmt.Errorf("remote signer: connect: %w", err)
		}
	}
	if sess.PubKey == "" {
		pub, err := bc.rpc(ctx, "get_public_key", nil)
		if err != nil {
			bc.close()
			if !fresh {
				return nil, fmt.Errorf("remote signer: %w (if the signer no longer knows this client, remove the session file and reconnect with a fresh bunker URL)", err)
			}
			return nil, fmt.Errorf("remote signer: get_public_key: %w", err)
		}
		if !nostr.IsValidPublicKey(pub) {
			bc.close()
			return nil, fmt.Errorf("remote signer returned an invalid public key %q", pub)
		}
		sess.PubKey = pub
	}
	if fresh || cfg.sessionDirty(sess) {
		if err := saveBunkerSession(cfg.profileName, sess); err != nil {
			fmt.Fprintln(os.Stderr, "warning: cannot save bunker session:", err)
		}
	}
	return &bunkerSigner{bc: bc, pub: sess.PubKey}, nil
}

// sessionDirty reports whether sess differs from what is on disk.
func (cfg *Config) sessionDirty(sess *bunkerSession) bool {
	old := loadBunkerSession(cfg.profileName, cfg.PrivateKey)
	return old == nil || *old != *sess
}

func connectParams(remote, secret string) []string {
	if secret == "" {
		return []string{remote}
	}
	return []string{remote, secret}
}

// parseBunkerInput resolves a bunker:// URL or NIP-05 identifier into the
// remote signer's public key, its relays and the optional connect secret.
func parseBunkerInput(ctx context.Context, input string) (remote string, relays []string, secret string, err error) {
	if strings.HasPrefix(input, "bunker://") {
		u, err := url.Parse(input)
		if err != nil {
			return "", nil, "", fmt.Errorf("invalid bunker URL: %w", err)
		}
		remote = u.Host
		relays = u.Query()["relay"]
		secret = u.Query().Get("secret")
	} else {
		res, name, err := nip05.Fetch(ctx, input)
		if err != nil {
			return "", nil, "", fmt.Errorf("resolve %s: %w", input, err)
		}
		remote = res.Names[name]
		relays = res.NIP46[remote]
	}
	if !nostr.IsValidPublicKey(remote) {
		return "", nil, "", fmt.Errorf("invalid remote signer public key %q", remote)
	}
	if len(relays) == 0 {
		return "", nil, "", fmt.Errorf("no relays given for remote signer %s", input)
	}
	for i, r := range relays {
		relays[i] = nostr.NormalizeURL(r)
	}
	return remote, relays, secret, nil
}

// pubkey returns the user's public key. With a remote signer or an encrypted
// key the cached public key is used, so read-only commands neither contact the
// signer nor ask for the password.
func (cfg *Config) pubkey() (string, error) {
	if isRemoteSigner(cfg.PrivateKey) {
		if sess := loadBunkerSession(cfg.profileName, cfg.PrivateKey); sess != nil && sess.PubKey != "" {
			return sess.PubKey, nil
		}
	} else if isEncryptedKey(cfg.PrivateKey) {
		if pub := loadCachedPubkey(cfg.profileName, cfg.PrivateKey); pub != "" {
			return pub, nil
		}
	}
	s, err := cfg.signer()
	if err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(context.Background(), signTimeout)
	defer cancel()
	return s.GetPublicKey(ctx)
}

// localSecretKey returns the raw private key for the few operations that
// cannot go through a Signer.
func (cfg *Config) localSecretKey() (string, error) {
	if isRemoteSigner(cfg.PrivateKey) {
		return "", errNeedLocalKey
	}
	s, err := cfg.signer()
	if err != nil {
		return "", err
	}
	return s.(*localSigner).sk, nil
}

// sign signs ev with the profile's signer.
func (cfg *Config) sign(ev *nostr.Event) error {
	s, err := cfg.signer()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), signTimeout)
	defer cancel()
	return s.SignEvent(ctx, ev)
}

// bunkerSigner implements Signer over a NIP-46 remote signer.
type bunkerSigner struct {
	bc  *bunkerClient
	pub string
}

func (s *bunkerSigner) GetPublicKey(context.Context) (string, error) { return s.pub, nil }

func (s *bunkerSigner) SignEvent(ctx context.Context, ev *nostr.Event) error {
	ev.PubKey = s.pub
	resp, err := s.bc.rpc(ctx, "sign_event", []string{ev.String()})
	if err != nil {
		return fmt.Errorf("remote signer: sign_event: %w", err)
	}
	var signed nostr.Event
	if err := json.Unmarshal([]byte(resp), &signed); err != nil {
		return fmt.Errorf("remote signer: sign_event: %w", err)
	}
	// The signer must sign exactly the event we asked for.
	if signed.PubKey != s.pub || signed.ID != ev.GetID() || !signed.CheckID() {
		return errors.New("remote signer: sign_event returned a different event")
	}
	if ok, _ := signed.CheckSignature(); !ok {
		return errors.New("remote signer: sign_event returned an invalid signature")
	}
	*ev = signed
	return nil
}

func (s *bunkerSigner) Encrypt(ctx context.Context, plaintext, recipient string) (string, error) {
	return s.bc.rpc(ctx, "nip44_encrypt", []string{recipient, plaintext})
}

func (s *bunkerSigner) Decrypt(ctx context.Context, ciphertext, sender string) (string, error) {
	return s.bc.rpc(ctx, "nip44_decrypt", []string{sender, ciphertext})
}

func (s *bunkerSigner) NIP04Encrypt(ctx context.Context, plaintext, recipient string) (string, error) {
	return s.bc.rpc(ctx, "nip04_encrypt", []string{recipient, plaintext})
}

func (s *bunkerSigner) NIP04Decrypt(ctx context.Context, ciphertext, sender string) (string, error) {
	return s.bc.rpc(ctx, "nip04_decrypt", []string{sender, ciphertext})
}
