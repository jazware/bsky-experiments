package auth

import (
	"crypto"
	"crypto/sha256"
	"errors"

	"github.com/golang-jwt/jwt/v4"
	"gitlab.com/yawning/secp256k1-voi/secec"
)

// signingMethodES256K verifies ES256K (secp256k1, R || S) JWT signatures.
// Kept in-tree because github.com/ericvolp12/jwt-go-secp256k1 is built on
// golang-jwt v3, which has an unfixed vulnerability (GO-2025-3553).
type signingMethodES256K struct{}

var es256k = &signingMethodES256K{}

func init() {
	jwt.RegisterSigningMethod(es256k.Alg(), func() jwt.SigningMethod { return es256k })
}

var (
	errWrongKeyFormat = errors.New("wrong key type")
	errBadSignature   = errors.New("bad signature")
	errVerification   = errors.New("signature verification failed")
	errSigning        = errors.New("signing not supported")
)

func (*signingMethodES256K) Alg() string { return "ES256K" }

func (*signingMethodES256K) Verify(signingString, signature string, key any) error {
	pub, ok := key.(*secec.PublicKey)
	if !ok {
		return errWrongKeyFormat
	}
	sig, err := jwt.DecodeSegment(signature)
	if err != nil {
		return err
	}
	if len(sig) != 64 {
		return errBadSignature
	}
	h := sha256.Sum256([]byte(signingString))
	if !pub.Verify(h[:], sig, &secec.ECDSAOptions{Hash: crypto.SHA256, Encoding: secec.EncodingCompact}) {
		return errVerification
	}
	return nil
}

func (*signingMethodES256K) Sign(string, any) (string, error) {
	return "", errSigning
}
