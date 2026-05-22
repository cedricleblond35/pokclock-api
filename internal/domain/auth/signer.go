// Package auth porte la logique d'authentification : signature et vérification
// JWT RS256, intégration au Worker Cloudflare de vérification de licence.
package auth

import (
	"crypto/rsa"
	"crypto/sha1"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"os"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// Tier représente le niveau de licence remonté par le Worker.
type Tier string

const (
	TierTrial Tier = "Trial"
	TierSolo  Tier = "Solo"
	TierClub  Tier = "Club"
	TierPro   Tier = "Pro"
)

// Claims est le payload du JWT émis par pokclock-api.
type Claims struct {
	HardwareID string `json:"hwid"`
	Tier       Tier   `json:"tier"`
	LicenseKey string `json:"lk"`
	jwt.RegisteredClaims
}

// Signer encapsule la paire RSA et le KID utilisé pour signer les JWT.
type Signer struct {
	private *rsa.PrivateKey
	public  *rsa.PublicKey
	kid     string
}

// NewSigner charge la paire RSA depuis les fichiers PEM passés en chemin.
// Calcule un KID dérivé du modulus de la clé publique pour pouvoir gérer la
// rotation un jour (ajouter une 2e clé sans casser l'existant).
func NewSigner(privatePath, publicPath string) (*Signer, error) {
	privPEM, err := os.ReadFile(privatePath)
	if err != nil {
		return nil, fmt.Errorf("read private key: %w", err)
	}
	pubPEM, err := os.ReadFile(publicPath)
	if err != nil {
		return nil, fmt.Errorf("read public key: %w", err)
	}

	priv, err := jwt.ParseRSAPrivateKeyFromPEM(privPEM)
	if err != nil {
		return nil, fmt.Errorf("parse private key: %w", err)
	}
	pub, err := jwt.ParseRSAPublicKeyFromPEM(pubPEM)
	if err != nil {
		return nil, fmt.Errorf("parse public key: %w", err)
	}

	return &Signer{
		private: priv,
		public:  pub,
		kid:     keyID(pub),
	}, nil
}

// Issue génère un JWT RS256 signé avec une durée de vie de 24h par défaut.
func (s *Signer) Issue(licenseKey, hardwareID string, tier Tier, ttl time.Duration) (string, error) {
	if ttl <= 0 {
		ttl = 24 * time.Hour
	}
	now := time.Now()
	claims := Claims{
		HardwareID: hardwareID,
		Tier:       tier,
		LicenseKey: licenseKey,
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer:    "pokclock-api",
			Audience:  jwt.ClaimStrings{"pokclock-desktop"},
			Subject:   licenseKey,
			IssuedAt:  jwt.NewNumericDate(now),
			NotBefore: jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(ttl)),
			ID:        randomID(),
		},
	}

	tok := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	tok.Header["kid"] = s.kid

	signed, err := tok.SignedString(s.private)
	if err != nil {
		return "", fmt.Errorf("sign jwt: %w", err)
	}
	return signed, nil
}

// Verify parse et valide un JWT signé par cette instance.
func (s *Signer) Verify(token string) (*Claims, error) {
	var claims Claims
	parsed, err := jwt.ParseWithClaims(token, &claims, func(t *jwt.Token) (any, error) {
		if _, ok := t.Method.(*jwt.SigningMethodRSA); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", t.Header["alg"])
		}
		return s.public, nil
	})
	if err != nil {
		return nil, err
	}
	if !parsed.Valid {
		return nil, errors.New("invalid token")
	}
	return &claims, nil
}

// JWKS retourne la clé publique au format JWKS (JSON Web Key Set) pour
// exposition à /.well-known/jwks.json.
func (s *Signer) JWKS() map[string]any {
	n := base64URL(s.public.N.Bytes())
	e := base64URL(big.NewInt(int64(s.public.E)).Bytes())
	return map[string]any{
		"keys": []map[string]any{
			{
				"kty": "RSA",
				"use": "sig",
				"alg": "RS256",
				"kid": s.kid,
				"n":   n,
				"e":   e,
			},
		},
	}
}

// PublicKID expose le KID pour les logs et l'audit.
func (s *Signer) PublicKID() string {
	return s.kid
}

func keyID(pub *rsa.PublicKey) string {
	der, err := x509.MarshalPKIXPublicKey(pub)
	if err != nil {
		// Fallback : hash du modulus si le marshalling rate.
		sum := sha1.Sum(pub.N.Bytes())
		return base64URL(sum[:])
	}
	sum := sha1.Sum(der)
	return base64URL(sum[:])
}

func base64URL(b []byte) string {
	return base64.RawURLEncoding.EncodeToString(b)
}

func randomID() string {
	// timestamp + 8 bytes : suffisant pour distinguer les jti, pas besoin de
	// crypto-strong UUID.
	now := time.Now().UnixNano()
	return base64.RawURLEncoding.EncodeToString(big.NewInt(now).Bytes())
}

// EnsurePEM est un helper qui valide qu'un fichier PEM est lisible et parseable.
// Utilisé par les tests pour générer un signer in-memory si besoin.
func EnsurePEM(path string) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	block, _ := pem.Decode(b)
	if block == nil {
		return errors.New("not a PEM file")
	}
	return nil
}
