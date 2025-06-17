package cypher

import (
	"bytes"
	"crypto/rand"
	"io"
	"testing"
)

func TestEncryptDecryptGCM(t *testing.T) {
	key := []byte("AES256Key-32Characters1234567890")
	plaintext := []byte("exampleplaintext")

	nonce := make([]byte, GCMNonceSize)
	_, err := io.ReadFull(rand.Reader, nonce)
	if err != nil {
		t.Fatal(err)
	}

	cyphertext, err := EncryptGCM(nil, key, nonce, plaintext, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("Ciphertext %x, nonce %x\n", cyphertext, nonce)

	p, err := DecryptGCM(key, nonce, cyphertext, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("Plaintext %v\n", string(p))
	if !bytes.Equal(plaintext, p) {
		t.Errorf("Failed: expected plaintext recovery, got %v expected %v", string(plaintext), string(p))
	}
}
