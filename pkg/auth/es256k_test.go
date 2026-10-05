package auth

import (
	"crypto"
	"crypto/rand"
	"crypto/sha256"
	"strings"
	"testing"

	"github.com/golang-jwt/jwt/v4"
	"gitlab.com/yawning/secp256k1-voi/secec"
)

func signES256K(t *testing.T, key *secec.PrivateKey, claims jwt.Claims) string {
	t.Helper()
	unsigned, err := jwt.NewWithClaims(es256k, claims).SigningString()
	if err != nil {
		t.Fatal(err)
	}
	h := sha256.Sum256([]byte(unsigned))
	sig, err := key.Sign(rand.Reader, h[:], &secec.ECDSAOptions{Hash: crypto.SHA256, Encoding: secec.EncodingCompact})
	if err != nil {
		t.Fatal(err)
	}
	return unsigned + "." + jwt.EncodeSegment(sig)
}

func TestES256KParse(t *testing.T) {
	key, err := secec.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	other, err := secec.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	token := signES256K(t, key, &jwt.StandardClaims{Issuer: "did:plc:test", Audience: "did:web:svc"})
	parser := jwt.Parser{ValidMethods: []string{es256k.Alg()}}

	for _, tc := range []struct {
		name  string
		token string
		key   *secec.PublicKey
		ok    bool
	}{
		{"valid", token, key.PublicKey(), true},
		{"wrong key", token, other.PublicKey(), false},
		{"tampered", strings.Replace(token, ".", ".e30", 1), key.PublicKey(), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			claims := jwt.StandardClaims{}
			_, err := parser.ParseWithClaims(tc.token, &claims, func(*jwt.Token) (any, error) { return tc.key, nil })
			if (err == nil) != tc.ok {
				t.Fatalf("err = %v, want ok=%v", err, tc.ok)
			}
			if tc.ok && (claims.Issuer != "did:plc:test" || claims.Audience != "did:web:svc") {
				t.Fatalf("claims = %+v", claims)
			}
		})
	}
}
