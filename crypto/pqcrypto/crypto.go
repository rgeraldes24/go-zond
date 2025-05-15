package pqcrypto

import (
	"bufio"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/theQRL/go-qrllib/crypto/ml_dsa_87"
	wallet "github.com/theQRL/go-qrllib/wallet/ml_dsa_87"
	"github.com/theQRL/go-zond/common"
)

const MLDSA87SignatureLength = ml_dsa_87.CryptoBytes

const MLDSA87PublicKeyLength = ml_dsa_87.CryptoPublicKeyBytes

// DigestLength sets the signature digest exact length
const DigestLength = 32

// LoadMLDSA87 loads MLDSA87 from the given file having hex seed (not extended hex seed).
func LoadMLDSA87(file string) (*ml_dsa_87.MLDSA87, error) {
	fd, err := os.Open(file)
	if err != nil {
		return nil, err
	}
	defer fd.Close()

	r := bufio.NewReader(fd)
	buf := make([]byte, ml_dsa_87.SeedBytes*2)
	n, err := readASCII(buf, r)
	if err != nil {
		return nil, err
	} else if n != len(buf) {
		return nil, fmt.Errorf("key file too short, want %v hex characters", ml_dsa_87.SeedBytes*2)
	}
	if err := checkKeyFileEnd(r); err != nil {
		return nil, err
	}

	return HexToMLDSA87(string(buf))
}

func GenerateMLDSA87Key() (*ml_dsa_87.MLDSA87, error) {
	return ml_dsa_87.New()
}

// readASCII reads into 'buf', stopping when the buffer is full or
// when a non-printable control character is encountered.
func readASCII(buf []byte, r *bufio.Reader) (n int, err error) {
	for ; n < len(buf); n++ {
		buf[n], err = r.ReadByte()
		switch {
		case err == io.EOF || buf[n] < '!':
			return n, nil
		case err != nil:
			return n, err
		}
	}
	return n, nil
}

// checkKeyFileEnd skips over additional newlines at the end of a key file.
func checkKeyFileEnd(r *bufio.Reader) error {
	for i := 0; ; i++ {
		b, err := r.ReadByte()
		switch {
		case err == io.EOF:
			return nil
		case err != nil:
			return err
		case b != '\n' && b != '\r':
			return fmt.Errorf("invalid character %q at end of key file", b)
		case i >= 2:
			return errors.New("key file too long, want 64 hex characters")
		}
	}
}

// ToMLDSA87Unsafe blindly converts a binary blob to a private key. It should almost
// never be used unless you are sure the input is valid and want to avoid hitting
// errors due to bad origin encoding (0 prefixes cut off).
func ToMLDSA87Unsafe(seed []byte) *ml_dsa_87.MLDSA87 {
	var sizedSeed [ml_dsa_87.SeedBytes]uint8
	copy(sizedSeed[:], seed)
	d, err := ml_dsa_87.NewMLDSA87FromSeed(sizedSeed)
	if err != nil {
		return nil
	}
	return d
}

// HexToMLDSA87 parses a hex seed (not extended hex seed).
func HexToMLDSA87(hexSeedStr string) (*ml_dsa_87.MLDSA87, error) {
	b, err := hex.DecodeString(hexSeedStr)
	if byteErr, ok := err.(hex.InvalidByteError); ok {
		return nil, fmt.Errorf("invalid hex character %q in seed", byte(byteErr))
	} else if err != nil {
		return nil, errors.New("invalid hex data for seed")
	}

	var hexSeed [ml_dsa_87.SeedBytes]uint8
	copy(hexSeed[:], b)

	return ml_dsa_87.NewMLDSA87FromSeed(hexSeed)
}

func MLDSA87PublicKeyToAddress(publicKey []byte) common.Address {
	var pk [MLDSA87PublicKeyLength]uint8
	copy(pk[:], publicKey)
	addr, _ := wallet.GetMLDSA87Address(pk, wallet.NewMLDSA87Descriptor())
	return addr
}

func MLDSA87ToAddress(k *ml_dsa_87.MLDSA87) common.Address {
	addr, _ := wallet.GetMLDSA87Address(k.GetPK(), wallet.NewMLDSA87Descriptor())
	return addr
}
