// Package main est un CLI de bootstrap qui émet un JWT signé par le Signer
// pokclock-api, sans passer par le Worker /verify. Utile pour générer
// le JWT super-admin initial (à coller dans le secret Swarm
// pokclock_api_admin_jwt utilisé par pokclock-ops).
//
// Usage typique (sur le VPS, dans le container pokclock-api) :
//
//	docker exec $(docker ps -q -f name=pokclock-api_api | head -1) \
//	  /app/issue-jwt \
//	    --license=POK-BOOTSTRAP-0001 \
//	    --role=superadmin \
//	    --ttl=720h \
//	    > /tmp/admin.jwt
//
// Le binaire lit la clé privée RSA depuis JWT_PRIVATE_KEY_PATH (même chemin
// que le serveur HTTP) et utilise le même Signer, donc le JWT produit est
// vérifiable par /api/admin/* sans modification.
package main

import (
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/cedricleblond35/pokclock-api/internal/domain/auth"
)

func main() {
	var (
		privPath = flag.String("priv", os.Getenv("JWT_PRIVATE_KEY_PATH"), "path to JWT private key PEM (default: $JWT_PRIVATE_KEY_PATH)")
		pubPath  = flag.String("pub", os.Getenv("JWT_PUBLIC_KEY_PATH"), "path to JWT public key PEM (default: $JWT_PUBLIC_KEY_PATH)")
		license  = flag.String("license", "", "license key to embed in the JWT (e.g. POK-BOOTSTRAP)")
		hwid     = flag.String("hwid", "bootstrap", "hardware id claim")
		tier     = flag.String("tier", "Pro", "tier claim: Trial | Solo | Club | Pro")
		role     = flag.String("role", "superadmin", "role claim: member | admin | superadmin")
		clubID   = flag.String("club", "", "club_id claim (uuid) — empty for superadmin")
		ttl      = flag.Duration("ttl", 720*time.Hour, "JWT lifetime, e.g. 24h, 30d, 720h")
	)
	flag.Parse()

	if *license == "" {
		fmt.Fprintln(os.Stderr, "error: --license is required (any string, will be used as the 'sub' claim)")
		flag.Usage()
		os.Exit(2)
	}
	if *privPath == "" || *pubPath == "" {
		fmt.Fprintln(os.Stderr, "error: JWT_PRIVATE_KEY_PATH and JWT_PUBLIC_KEY_PATH must be set")
		os.Exit(2)
	}
	r := auth.Role(*role)
	if !r.IsValid() {
		fmt.Fprintf(os.Stderr, "error: invalid --role %q (must be member|admin|superadmin)\n", *role)
		os.Exit(2)
	}

	signer, err := auth.NewSigner(*privPath, *pubPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "signer init: %v\n", err)
		os.Exit(1)
	}

	token, err := signer.Issue(auth.IssueParams{
		LicenseKey: *license,
		HardwareID: *hwid,
		Tier:       auth.Tier(*tier),
		Role:       r,
		ClubID:     *clubID,
		TTL:        *ttl,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "sign: %v\n", err)
		os.Exit(1)
	}

	// Stdout = JWT brut, prêt à être pipé vers docker secret create.
	// Stderr = info pour l'opérateur.
	fmt.Println(token)
	fmt.Fprintf(os.Stderr,
		"issued JWT (license=%s role=%s tier=%s ttl=%s kid=%s)\n",
		*license, *role, *tier, *ttl, signer.PublicKID(),
	)
}
