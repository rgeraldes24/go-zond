package pqcrypto

import (
	"fmt"

	walletmldsa87 "github.com/theQRL/go-qrllib/wallet/ml_dsa_87"
)

func Sign(digestHash []byte, w *walletmldsa87.Wallet) ([]byte, error) {
	if len(digestHash) != DigestLength {
		return nil, fmt.Errorf("hash is required to be exactly %d bytes (%d)", DigestLength, len(digestHash))
	}
	signature, err := w.Sign(digestHash)
	if err != nil {
		return nil, err
	}
	return signature[:], nil
}
