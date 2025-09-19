package main

import (
	"encoding/hex"
	"fmt"
	"log"

	"github.com/theQRL/go-qrllib/wallet/ml_dsa_87"
)

func main() {
	msg := []uint8{'Q', 'R', 'L'}

	// Create wallet
	wallet, err := ml_dsa_87.NewWallet()
	if err != nil {
		log.Fatal(err)
	}

	// Sign message
	sig, err := wallet.Sign(msg)
	if err != nil {
		log.Fatal(err)
	}

	pk := wallet.GetPK()
	sigBytes := sig[:]
	buf := append(pk[:], sigBytes...)
	buf = append(buf, msg...)
	fmt.Println(hex.EncodeToString(buf))
}
