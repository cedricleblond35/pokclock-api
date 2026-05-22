package auth_test

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/cedricleblond35/pokclock-api/internal/domain/auth"
)

// generateKeypairFiles génère une paire RSA-2048 et écrit private + public en
// PEM dans dir. Retourne les chemins. Utilisé par les tests pour ne pas
// dépendre d'OpenSSL ni d'une paire pré-générée.
func generateKeypairFiles(t *testing.T, dir string) (privPath, pubPath string) {
	t.Helper()

	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate rsa: %v", err)
	}

	privBytes := x509.MarshalPKCS1PrivateKey(priv)
	privPEM := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: privBytes})
	privPath = filepath.Join(dir, "private.pem")
	if err := os.WriteFile(privPath, privPEM, 0o600); err != nil {
		t.Fatalf("write priv: %v", err)
	}

	pubBytes, err := x509.MarshalPKIXPublicKey(&priv.PublicKey)
	if err != nil {
		t.Fatalf("marshal pub: %v", err)
	}
	pubPEM := pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: pubBytes})
	pubPath = filepath.Join(dir, "public.pem")
	if err := os.WriteFile(pubPath, pubPEM, 0o600); err != nil {
		t.Fatalf("write pub: %v", err)
	}
	return
}

func TestSigner_IssueAndVerify(t *testing.T) {
	dir := t.TempDir()
	privPath, pubPath := generateKeypairFiles(t, dir)

	signer, err := auth.NewSigner(privPath, pubPath)
	if err != nil {
		t.Fatalf("NewSigner: %v", err)
	}

	token, err := signer.Issue("POK-AAAA-BBBB-CCCC-DDDD", "hwid123", auth.TierClub, 1*time.Hour)
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	if token == "" {
		t.Fatal("expected non-empty token")
	}

	claims, err := signer.Verify(token)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if claims.HardwareID != "hwid123" {
		t.Errorf("hwid: got %q", claims.HardwareID)
	}
	if claims.Tier != auth.TierClub {
		t.Errorf("tier: got %q", claims.Tier)
	}
	if claims.LicenseKey != "POK-AAAA-BBBB-CCCC-DDDD" {
		t.Errorf("license: got %q", claims.LicenseKey)
	}
	if claims.Subject != "POK-AAAA-BBBB-CCCC-DDDD" {
		t.Errorf("subject: got %q", claims.Subject)
	}
}

func TestSigner_VerifyRejectsTamperedToken(t *testing.T) {
	dir := t.TempDir()
	privPath, pubPath := generateKeypairFiles(t, dir)
	signer, err := auth.NewSigner(privPath, pubPath)
	if err != nil {
		t.Fatalf("NewSigner: %v", err)
	}

	token, err := signer.Issue("POK-1", "hw", auth.TierSolo, time.Hour)
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}

	// Modifie un caractère au milieu de la signature pour casser sans risquer
	// le padding base64URL du tout dernier caractère.
	idx := len(token) - 20
	tampered := token[:idx] + flipChar(token[idx]) + token[idx+1:]
	if _, err := signer.Verify(tampered); err == nil {
		t.Fatal("expected verify to fail on tampered token")
	}
}

func TestSigner_JWKS(t *testing.T) {
	dir := t.TempDir()
	privPath, pubPath := generateKeypairFiles(t, dir)
	signer, err := auth.NewSigner(privPath, pubPath)
	if err != nil {
		t.Fatalf("NewSigner: %v", err)
	}

	jwks := signer.JWKS()
	keys, ok := jwks["keys"].([]map[string]any)
	if !ok || len(keys) != 1 {
		t.Fatalf("expected single key in JWKS, got %T", jwks["keys"])
	}
	k := keys[0]
	if k["kty"] != "RSA" || k["alg"] != "RS256" || k["use"] != "sig" {
		t.Errorf("unexpected key shape: %+v", k)
	}
	if k["kid"] != signer.PublicKID() {
		t.Errorf("kid mismatch: %v vs %v", k["kid"], signer.PublicKID())
	}
}

func flipChar(c byte) string {
	// On vise un autre caractère base64URL valide pour rester sur une chaîne
	// décodable, mais avec une signature qui ne correspond plus.
	if c == 'A' {
		return "B"
	}
	return "A"
}
