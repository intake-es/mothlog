package webauth

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
)

// jwk es una clave pública del JWKS del emisor.
type jwk struct {
	kid string
	alg string
	key crypto.PublicKey
}

func parseJWK(raw json.RawMessage) (jwk, bool) {
	var k struct {
		Kty string `json:"kty"`
		Kid string `json:"kid"`
		Use string `json:"use"`
		Alg string `json:"alg"`
		N   string `json:"n"`
		E   string `json:"e"`
		Crv string `json:"crv"`
		X   string `json:"x"`
		Y   string `json:"y"`
	}
	if json.Unmarshal(raw, &k) != nil || (k.Use != "" && k.Use != "sig") {
		return jwk{}, false
	}
	out := jwk{kid: k.Kid, alg: k.Alg}
	b := func(s string) []byte {
		v, err := base64.RawURLEncoding.DecodeString(s)
		if err != nil {
			return nil
		}
		return v
	}
	switch k.Kty {
	case "RSA":
		n, e := b(k.N), b(k.E)
		if len(n) < 256 || len(e) == 0 || len(e) > 4 {
			return jwk{}, false
		}
		out.key = &rsa.PublicKey{N: new(big.Int).SetBytes(n), E: int(new(big.Int).SetBytes(e).Int64())}
	case "EC":
		var curve elliptic.Curve
		switch k.Crv {
		case "P-256":
			curve = elliptic.P256()
		case "P-384":
			curve = elliptic.P384()
		case "P-521":
			curve = elliptic.P521()
		default:
			return jwk{}, false
		}
		x, y := b(k.X), b(k.Y)
		size := (curve.Params().BitSize + 7) / 8
		if len(x) != size || len(y) != size {
			return jwk{}, false
		}
		// Sin comprimir: 0x04 || X || Y; comprueba que el punto esté en la curva.
		pub, err := ecdsa.ParseUncompressedPublicKey(curve, append(append([]byte{4}, x...), y...))
		if err != nil {
			return jwk{}, false
		}
		out.key = pub
	case "OKP":
		x := b(k.X)
		if k.Crv != "Ed25519" || len(x) != ed25519.PublicKeySize {
			return jwk{}, false
		}
		out.key = ed25519.PublicKey(x)
	default:
		return jwk{}, false
	}
	return out, true
}

// verify comprueba la firma con el alg de la cabecera, que tiene que casar
// con el tipo de clave y con su alg si lo trae. none y HS* no valen.
func (k jwk) verify(alg string, signed, sig []byte) error {
	if k.alg != "" && k.alg != alg {
		return fmt.Errorf("la clave es %s, el token %s", k.alg, alg)
	}
	hashOf := func(h crypto.Hash) []byte {
		w := h.New()
		w.Write(signed)
		return w.Sum(nil)
	}
	switch pub := k.key.(type) {
	case *rsa.PublicKey:
		var h crypto.Hash
		switch alg {
		case "RS256", "PS256":
			h = crypto.SHA256
		case "RS384", "PS384":
			h = crypto.SHA384
		case "RS512", "PS512":
			h = crypto.SHA512
		default:
			return fmt.Errorf("alg %q con clave RSA", alg)
		}
		if alg[0] == 'P' {
			return rsa.VerifyPSS(pub, h, hashOf(h), sig, &rsa.PSSOptions{SaltLength: rsa.PSSSaltLengthEqualsHash})
		}
		return rsa.VerifyPKCS1v15(pub, h, hashOf(h), sig)
	case *ecdsa.PublicKey:
		var h crypto.Hash
		var curve elliptic.Curve
		switch alg {
		case "ES256":
			h, curve = crypto.SHA256, elliptic.P256()
		case "ES384":
			h, curve = crypto.SHA384, elliptic.P384()
		case "ES512":
			h, curve = crypto.SHA512, elliptic.P521()
		default:
			return fmt.Errorf("alg %q con clave EC", alg)
		}
		size := (curve.Params().BitSize + 7) / 8
		if pub.Curve != curve || len(sig) != 2*size {
			return errors.New("firma EC de otra curva")
		}
		r, s := new(big.Int).SetBytes(sig[:size]), new(big.Int).SetBytes(sig[size:])
		if !ecdsa.Verify(pub, hashOf(h), r, s) {
			return errors.New("firma EC no válida")
		}
		return nil
	case ed25519.PublicKey:
		if alg != "EdDSA" && alg != "Ed25519" {
			return fmt.Errorf("alg %q con clave Ed25519", alg)
		}
		if !ed25519.Verify(pub, signed, sig) {
			return errors.New("firma Ed25519 no válida")
		}
		return nil
	}
	return errors.New("tipo de clave desconocido")
}
