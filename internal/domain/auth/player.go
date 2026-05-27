package auth

import (
	"errors"
	"fmt"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// AudiencePlayer : claim aud des JWT joueur (Phase 0.E.1). Permet de
// distinguer un token desktop ("pokclock-desktop") d'un token joueur dans
// les middlewares — un token desktop ne doit jamais authentifier un joueur
// et inversement.
const AudiencePlayer = "pokclock-player"

// PlayerClaims est le payload du JWT émis pour un joueur connecté via magic
// link ou Google OAuth. Pas de license / hardware ID : pertinent uniquement
// côté app desktop.
type PlayerClaims struct {
	PlayerID string `json:"pid"`
	Email    string `json:"email"`
	jwt.RegisteredClaims
}

// PlayerIssueParams porte les champs nécessaires à l'émission d'un JWT joueur.
type PlayerIssueParams struct {
	PlayerID string
	Email    string
	TTL      time.Duration
}

// IssuePlayer génère un JWT RS256 signé pour un joueur. TTL par défaut 30 jours
// (le cookie HttpOnly côté navigateur reste actif au moins ce temps).
func (s *Signer) IssuePlayer(p PlayerIssueParams) (string, error) {
	if p.PlayerID == "" {
		return "", errors.New("player id required")
	}
	ttl := p.TTL
	if ttl <= 0 {
		ttl = 30 * 24 * time.Hour
	}
	now := time.Now()
	claims := PlayerClaims{
		PlayerID: p.PlayerID,
		Email:    p.Email,
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer:    "pokclock-api",
			Audience:  jwt.ClaimStrings{AudiencePlayer},
			Subject:   p.PlayerID,
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
		return "", fmt.Errorf("sign player jwt: %w", err)
	}
	return signed, nil
}

// VerifyPlayer parse et valide un JWT joueur. Refuse les tokens qui n'ont
// pas l'audience AudiencePlayer (protection contre confusion desktop/player).
func (s *Signer) VerifyPlayer(token string) (*PlayerClaims, error) {
	var claims PlayerClaims
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
	hasAud := false
	for _, a := range claims.Audience {
		if a == AudiencePlayer {
			hasAud = true
			break
		}
	}
	if !hasAud {
		return nil, errors.New("wrong audience")
	}
	return &claims, nil
}
