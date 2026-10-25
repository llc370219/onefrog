package id

import (
	"crypto/rand"
	"encoding/hex"
)

func New() string {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		panic(err)
	}
	return hex.EncodeToString(buf)
}
