package token

import (
	"crypto/ed25519"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"time"
)

type eddsaKeyMeta struct {
	Status    KeyStatus `json:"status"`
	CreatedAt time.Time `json:"created_at"`
	RotatedAt time.Time `json:"rotated_at"`
	ExpiresAt time.Time `json:"expires_at"`
}

func encodeEd25519PrivateKeyToPEM(key ed25519.PrivateKey) (string, error) {
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return "", fmt.Errorf("marshal pkcs8 private key: %w", err)
	}
	block := &pem.Block{Type: "PRIVATE KEY", Bytes: der}
	return string(pem.EncodeToMemory(block)), nil
}

func encodeEd25519PublicKeyToPEM(key ed25519.PublicKey) (string, error) {
	der, err := x509.MarshalPKIXPublicKey(key)
	if err != nil {
		return "", fmt.Errorf("marshal pkix public key: %w", err)
	}
	block := &pem.Block{Type: "PUBLIC KEY", Bytes: der}
	return string(pem.EncodeToMemory(block)), nil
}
