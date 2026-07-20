// SPDX-License-Identifier: AGPL-3.0-only

package auth

import (
	"crypto/aes"
	"crypto/cipher"
	"fmt"
)

type cfb8 struct {
	block   cipher.Block
	iv      []byte
	decrypt bool
	tmp     []byte
}

func NewCFB8(key []byte, decrypt bool) (cipher.Stream, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("create AES cipher: %w", err)
	}
	return &cfb8{block: block, iv: append([]byte(nil), key...), decrypt: decrypt, tmp: make([]byte, block.BlockSize())}, nil
}

func (x *cfb8) XORKeyStream(dst, src []byte) {
	if len(dst) < len(src) {
		panic("cfb8: output smaller than input")
	}
	for i := range src {
		in := src[i]
		x.block.Encrypt(x.tmp, x.iv)
		out := in ^ x.tmp[0]
		dst[i] = out
		copy(x.iv, x.iv[1:])
		if x.decrypt {
			x.iv[len(x.iv)-1] = in
		} else {
			x.iv[len(x.iv)-1] = out
		}
	}
}
