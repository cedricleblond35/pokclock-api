# pokclock-api

Backend Go pour les features cloud de PokClock : Passeport (licence), Live, Inscriptions, Multi-Canaux.

Déployé sur `https://api.pokclock.com` via Docker Swarm + Traefik (DNS-01 wildcard OVH).
Stack : **Echo v4**, **pgx/v5**, **golang-jwt/v5**, **goose** pour les migrations.

## Démarrage rapide

```bash
# 1. Générer une paire JWT pour le dev local
make keypair

# 2. Postgres local (Docker)
docker run --rm -d --name pokclock-pg \
  -e POSTGRES_PASSWORD=dev -e POSTGRES_DB=pokclock \
  -p 5432:5432 postgres:16-alpine

# 3. Variables d'env
export DATABASE_URL="postgres://postgres:dev@localhost:5432/pokclock?sslmode=disable"
export JWT_PRIVATE_KEY_PATH=./secrets-dev/jwt_private.pem
export JWT_PUBLIC_KEY_PATH=./secrets-dev/jwt_public.pem
export WORKER_VERIFY_URL=https://pokclock-license.dynamidoxa.workers.dev/verify
export PORT=8080

# 4. Démarrer
make run
```

## Endpoints

| Méthode | Chemin | Auth | Description |
|---|---|---|---|
| GET | `/api/health` | non | Healthcheck JSON, retourne le build SHA |
| POST | `/api/auth/issue` | non | Forward la licence au Worker Cloudflare, émet un JWT RS256 24h |
| POST | `/api/auth/refresh` | refresh token | Rotation du JWT + nouveau refresh token |
| POST | `/api/structures` | JWT | Créer une structure (club, casino, etc.) |
| GET | `/api/structures/{slug}` | JWT | Lire une structure |
| PATCH | `/api/structures/{slug}` | JWT | Modifier une structure (avec audit log) |
| GET | `/.well-known/jwks.json` | non | Clé publique au format JWKS pour validation côté client |

## Flow auth

```
App WPF             pokclock-api              Worker Cloudflare
  │                      │                         │
  │ POST /auth/issue     │                         │
  │ { licenseKey, hwid,  │                         │
  │   workerToken }      │                         │
  ├────────────────────► │                         │
  │                      │  POST /verify           │
  │                      ├────────────────────────►│
  │                      │ ◄───────────────────────┤
  │                      │ { valid, tier, exp }    │
  │                      │                         │
  │                      │ Sign RS256 JWT          │
  │ ◄────────────────────┤                         │
  │ { jwt, refreshToken }│                         │
```

Le `workerToken` est obligatoire côté Worker : c'est le HMAC issu de `/activate`, persisté dans `license.json` côté app WPF.

## Variables d'environnement

| Variable | Requis | Description |
|---|---|---|
| `DATABASE_URL` | oui | Connexion Postgres |
| `JWT_PRIVATE_KEY_PATH` | oui | Chemin vers la clé privée RSA (en prod : `/run/secrets/jwt_private`) |
| `JWT_PUBLIC_KEY_PATH` | oui | Chemin vers la clé publique RSA |
| `WORKER_VERIFY_URL` | oui | URL du Worker Cloudflare `/verify` |
| `PORT` | non | Défaut 8080 |
| `LOG_LEVEL` | non | `debug`, `info` (défaut), `warn`, `error` |
| `ALLOWED_ORIGINS` | non | CSV pour CORS (défaut : aucune origine) |

## Structure du projet

```
pokclock-api/
├── cmd/api/main.go          # Entry point
├── internal/
│   ├── api/                  # Handlers HTTP, router, middleware
│   ├── domain/
│   │   ├── auth/             # JWT signing, claims, Worker client
│   │   └── structures/       # Logique métier structures
│   └── db/                   # Pool pgx, migrations runtime
├── migrations/               # SQL goose (001_*.sql, 002_*.sql, …)
├── .github/workflows/        # CI + deploy
├── Dockerfile                # Multi-stage (alpine final)
├── docker-compose.swarm.prod.yml
└── Makefile
```

## Tests

```bash
make test                          # Tests + coverage
go test ./internal/domain/auth/... # Cible un package
```

## Production

Déploiement automatique sur push `main` via `.github/workflows/deploy.yml` :
1. CI (vet + test + build)
2. Build image, push GHCR
3. SSH VPS, `docker stack deploy --with-registry-auth -c docker-compose.swarm.prod.yml pokclock-api`

Rollback : `docker service update --rollback pokclock-api_api`.

## Voir aussi

- [PHASE-0A-PROGRESS.md](../PHASE-0A-PROGRESS.md) — kanban Sprint 0
- `pokclock-infra/` — Traefik + monitoring (repo séparé)
