// Package aescbc wraps AES-CBC with PKCS#7 padding, the one cipher mode every
// part of the site's delivery chain uses.
package aescbc

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"errors"
)

// Decrypt decrypts ciphertext and removes its PKCS#7 padding.
func Decrypt(key, iv, ciphertext []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	if len(iv) != aes.BlockSize {
		return nil, errors.New("aescbc: IV must be 16 bytes")
	}
	if len(ciphertext) == 0 || len(ciphertext)%aes.BlockSize != 0 {
		return nil, errors.New("aescbc: ciphertext is not a multiple of the block size")
	}
	out := make([]byte, len(ciphertext))
	cipher.NewCBCDecrypter(block, iv).CryptBlocks(out, ciphertext)

	pad := int(out[len(out)-1])
	if pad == 0 || pad > aes.BlockSize {
		return nil, errors.New("aescbc: invalid padding")
	}
	for _, b := range out[len(out)-pad:] {
		if int(b) != pad {
			return nil, errors.New("aescbc: invalid padding")
		}
	}
	return out[:len(out)-pad], nil
}

// Encrypt pads plaintext with PKCS#7 and encrypts it.
func Encrypt(key, iv, plaintext []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	if len(iv) != aes.BlockSize {
		return nil, errors.New("aescbc: IV must be 16 bytes")
	}
	pad := aes.BlockSize - len(plaintext)%aes.BlockSize
	padded := append(bytes.Clone(plaintext), bytes.Repeat([]byte{byte(pad)}, pad)...)
	out := make([]byte, len(padded))
	cipher.NewCBCEncrypter(block, iv).CryptBlocks(out, padded)
	return out, nil
}
