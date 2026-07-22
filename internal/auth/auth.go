// SPDX-License-Identifier: AGPL-3.0-only

// package auth implements Minecrafts mandatory online mode authentication
package auth

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha1"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync/atomic"
	"time"
)

const DefaultSessionURL = "https://sessionserver.mojang.com/session/minecraft/hasJoined"

type Property struct {
	Name      string `json:"name"`
	Value     string `json:"value"`
	Signature string `json:"signature,omitempty"`
}
type Identity struct {
	UUID       [16]byte
	Username   string
	Properties []Property
}

type Verifier interface {
	Verify(context.Context, string, string) (Identity, error)
	Healthy() bool
}

type MojangVerifier struct {
	endpoint string
	client   *http.Client
	healthy  atomic.Bool
}

func NewMojangVerifier(timeout time.Duration) *MojangVerifier {
	return NewMojangVerifierAt(DefaultSessionURL, timeout)
}
func NewMojangVerifierAt(endpoint string, timeout time.Duration) *MojangVerifier {
	return &MojangVerifier{endpoint: endpoint, client: &http.Client{Timeout: timeout}}
}
func (v *MojangVerifier) Healthy() bool { return v.healthy.Load() }

func (v *MojangVerifier) Verify(ctx context.Context, username, serverHash string) (Identity, error) {
	q := url.Values{"username": {username}, "serverId": {serverHash}}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, v.endpoint+"?"+q.Encode(), nil)
	if err != nil {
		return Identity{}, err
	}
	resp, err := v.client.Do(req)
	if err != nil {
		v.healthy.Store(false)
		return Identity{}, fmt.Errorf("contact Mojang session service: %w", err)
	}
	defer resp.Body.Close()
	v.healthy.Store(resp.StatusCode < 500)
	if resp.StatusCode == http.StatusNoContent {
		return Identity{}, fmt.Errorf("Mojang session service rejected the player session")
	}
	if resp.StatusCode != http.StatusOK {
		io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		return Identity{}, fmt.Errorf("Mojang session service returned HTTP %d", resp.StatusCode)
	}
	var wire struct {
		ID         string     `json:"id"`
		Name       string     `json:"name"`
		Properties []Property `json:"properties"`
	}
	dec := json.NewDecoder(io.LimitReader(resp.Body, 1<<20))
	if err := dec.Decode(&wire); err != nil {
		return Identity{}, fmt.Errorf("decode Mojang session response: %w", err)
	}
	id, err := ParseUUID(wire.ID)
	if err != nil {
		return Identity{}, fmt.Errorf("invalid authenticated UUID: %w", err)
	}
	if !strings.EqualFold(wire.Name, username) {
		return Identity{}, fmt.Errorf("authenticated name %q does not match login name %q", wire.Name, username)
	}
	return Identity{UUID: id, Username: wire.Name, Properties: wire.Properties}, nil
}

func GenerateKey() (*rsa.PrivateKey, []byte, error) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return nil, nil, err
	}
	der, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	if err != nil {
		return nil, nil, err
	}
	return key, der, nil
}

func NewVerifyToken() ([]byte, error) {
	b := make([]byte, 4)
	_, err := io.ReadFull(rand.Reader, b)
	return b, err
}

func Decrypt(key *rsa.PrivateKey, ciphertext []byte) ([]byte, error) {
	return rsa.DecryptPKCS1v15(rand.Reader, key, ciphertext)
}

func ServerHash(serverID string, sharedSecret, publicKey []byte) string {
	h := sha1.New()
	h.Write([]byte(serverID))
	h.Write(sharedSecret)
	h.Write(publicKey)
	sum := h.Sum(nil)
	negative := sum[0]&0x80 != 0
	if negative {
		for i := range sum {
			sum[i] = ^sum[i]
		}
		for i := len(sum) - 1; i >= 0; i-- {
			sum[i]++
			if sum[i] != 0 {
				break
			}
		}
	}
	s := strings.TrimLeft(hex.EncodeToString(sum), "0")
	if s == "" {
		s = "0"
	}
	if negative {
		s = "-" + s
	}
	return s
}

func ParseUUID(s string) ([16]byte, error) {
	var out [16]byte
	s = strings.ReplaceAll(s, "-", "")
	if len(s) != 32 {
		return out, fmt.Errorf("UUID must contain 32 hexadecimal digits")
	}
	b, err := hex.DecodeString(s)
	if err != nil {
		return out, err
	}
	copy(out[:], b)
	return out, nil
}

func FormatUUID(id [16]byte) string {
	h := hex.EncodeToString(id[:])
	return h[:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:]
}
