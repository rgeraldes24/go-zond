package pqcrypto

import (
	"fmt"

	"github.com/theQRL/go-qrllib/crypto/ml_dsa_87"
)

func Sign(ctx []byte, digestHash []byte, d *ml_dsa_87.MLDSA87) ([]byte, error) {
	if len(digestHash) != DigestLength {
		return nil, fmt.Errorf("hash is required to be exactly %d bytes (%d)", DigestLength, len(digestHash))
	}
	signature, err := d.Sign(ctx, digestHash)
	if err != nil {
		return nil, err
	}
	return signature[:], nil
}
