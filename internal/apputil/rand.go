package apputil

import (
	"crypto/rand"
	rand_v2 "math/rand/v2"
)

func NewFastRand() *rand_v2.ChaCha8 {
	var seed [32]byte
	_, _ = rand.Read(seed[:])
	return rand_v2.NewChaCha8(seed)
}
