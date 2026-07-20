// SPDX-License-Identifier: AGPL-3.0-only

package auth

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestCFB8RoundTrip(t *testing.T) {
	key := []byte("0123456789abcdef")
	plain := []byte("minecraft stream encryption crosses block boundaries")
	enc, _ := NewCFB8(key, false)
	dec, _ := NewCFB8(key, true)
	ciphertext := make([]byte, len(plain))
	enc.XORKeyStream(ciphertext, plain)
	got := make([]byte, len(plain))
	dec.XORKeyStream(got[:7], ciphertext[:7])
	dec.XORKeyStream(got[7:], ciphertext[7:])
	if !bytes.Equal(got, plain) {
		t.Fatalf("got %q", got)
	}
}

func TestVerifier(t *testing.T) {
	v := NewMojangVerifierAt("https://session.invalid/hasJoined", time.Second)
	v.client.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Query().Get("username") != "Steve" {
			t.Error("missing username")
		}
		if _, present := r.URL.Query()["ip"]; present {
			t.Error("unexpected IP-bound session verification")
		}
		var b bytes.Buffer
		_ = json.NewEncoder(&b).Encode(map[string]any{"id": "8667ba71b85a4004af54457a9734eed7", "name": "Steve"})
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(b.String())), Header: make(http.Header)}, nil
	})
	id, err := v.Verify(context.Background(), "Steve", "hash")
	if err != nil {
		t.Fatal(err)
	}
	if FormatUUID(id.UUID) != "8667ba71-b85a-4004-af54-457a9734eed7" || !v.Healthy() {
		t.Fatalf("got %+v", id)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestServerHash(t *testing.T) {
	tests := map[string]string{
		"Notch": "4ed1f46bbe04bc756bcb17c0c7ce3e4632f06a48",
		"jeb_":  "-7c9d5b0044c130109a5d7b5fb5c317c02b4e28c1",
	}
	for input, want := range tests {
		if got := ServerHash(input, nil, nil); got != want {
			t.Errorf("ServerHash(%q) = %q, want %q", input, got, want)
		}
	}
}
