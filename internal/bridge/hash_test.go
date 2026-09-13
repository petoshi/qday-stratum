package bridge

import "golang.org/x/crypto/blake2b"

func blakeSum(data []byte) [32]byte { return blake2b.Sum256(data) }
