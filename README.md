# pokclock-api

Backend Go pour les features cloud de PokClock. Couvre l'auth license desktop, l'admin super-admin / club, les tournois publiés, les inscriptions online, les comptes joueurs et le leaderboard.

Déployé sur `https://api.pokclock.com` via Docker Swarm + Traefik (Cloudflare DNS-01 wildcard).
Stack : **Echo v4**, **pgx/v5**, **golang-jwt/v5**, **goose** pour les migrations, **Resend** HTTP pour les emails.

## État au 2026-05-27

| Phase | État | Détail |
|---|---|---|
| 0.A — Foundation | livré 2026-05-24 | Postgres + JWT + Worker /verify + R2 backup |
| 0.B — RBAC + ops.pokclock.com | livré 2026-05-25 | claims `role` + `club_id`, `/api/admin/*`, `pokclock-ops` Next.js |
| 0.C — Club ops baseline | livré 2026-05-27 | members CRUD, licenses club-scoped, audit, structures partagées |
| 0.D — Tournois publiés + inscriptions | livré 2026-05-27 | `/public/clubs/:slug/tournaments`, prize structure, barème points |
| 0.E — Comptes joueurs | livré 2026-05-27 | magic link, leaderboard, dialog résultats WPF |
| 0.E.1.e Google OAuth login | livré 2026-05-27 | attente config GCP côté VPS |
| 0.E.5 Google Calendar sync | livré 2026-05-27 | attente config GCP côté VPS |
| 0.F — Adhésions joueurs + tournois réservés | livré 2026-05-27 | members_only + WPF onglet MEMBRES JOUEURS |
| RGPD — droit à l'effacement | livré 2026-05-27 | DELETE /api/players/me + email confirmation |

Détail phase par phase : `~/workspace/PHASE-0B-OPS-ROADMAP.md` § "Bilan 2026-05-27".

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

### Publics (anonymes, rate-limit en amont)

| Méthode | Chemin | Description |
|---|---|---|
| GET | `/api/health` | Healthcheck JSON, retourne le build SHA |
| GET | `/.well-known/jwks.json` | Clé publique au format JWKS |
| POST | `/api/auth/issue` | Forward la licence au Worker, émet un JWT desktop (audience `pokclock-desktop`) |
| POST | `/api/auth/refresh` | Rotation du JWT + refresh token |
| GET | `/public/clubs/:slug/tournaments` | Liste publique des tournois ouverts du club |
| GET | `/public/clubs/:slug/tournaments/:id` | Détail tournoi (incl. joueurs, prize structure, barème points) |
| POST | `/public/clubs/:slug/tournaments/:id/register` | Inscription anonyme (firstName/lastName/email/phone), renvoie cancel_token |
| GET | `/public/registrations/:token/cancel` | Annulation via magic link email |
| GET | `/public/clubs/:slug/tournaments/:id/results` | Résultats finaux d'un tournoi |
| GET | `/public/clubs/:slug/leaderboard` | Classement cumulé cross-tournois |

### Comptes joueurs (Phase 0.E)

| Méthode | Chemin | Auth | Description |
|---|---|---|---|
| POST | `/api/players/auth/magic-link` | non | Envoie un lien de connexion par email (15 min, single-use). Réponse toujours 202 (anti-énumération). |
| GET | `/api/players/auth/magic-link/verify?token=…` | non | Consomme le token, upsert player, pose cookie `pokclock_player` JWT (audience `pokclock-player`), redirige vers `/joueurs/dashboard` |
| GET | `/api/players/auth/google/start` | non | OAuth Google login flow (scope basic). Redirige Google authorize. |
| GET | `/api/players/auth/google/callback` | non | Callback OAuth login, upsert player par google_id ou email, pose cookie. |
| POST | `/api/players/auth/logout` | cookie | Efface le cookie de session |
| GET | `/api/players/me` | cookie | Profil joueur |
| PATCH | `/api/players/me` | cookie | Modifie firstName/lastName |
| DELETE | `/api/players/me` | cookie | RGPD : anonymise registrations + results, supprime compte + magic_links + tokens Google, email confirmation |
| POST | `/api/players/me/clubs/:slug/tournaments/:id/register` | cookie | Inscription en un clic (prefille depuis le profil) |
| DELETE | `/api/players/me/registrations/:id` | cookie | Annule sa propre inscription |
| GET | `/api/players/me/registrations` | cookie | Liste les inscriptions du joueur (passées + à venir) |
| GET / POST `:slug/join` / DELETE `:slug` | `/api/players/me/clubs` | cookie | Phase 0.F : list mes memberships / demander adhésion / quitter un club |
| GET | `/api/players/me/google-calendar` | cookie | Status connexion Calendar (available + connected + connectedAt) |
| GET | `/api/players/me/google-calendar/start` | cookie | OAuth flow Calendar (scope calendar.events, offline + prompt consent) |
| GET | `/api/players/me/google-calendar/callback` | cookie | Callback Calendar, upsert player_google_tokens |
| DELETE | `/api/players/me/google-calendar` | cookie | Révoque côté Google + DELETE row tokens locale |

### Club admin (`/api/club/*`, JWT desktop avec role admin ou superadmin)

| Méthode | Chemin | Description |
|---|---|---|
| GET | `/api/club/info` | Métadonnées du club |
| PATCH | `/api/club/online-registrations` | Toggle opt-in inscriptions online |
| GET / POST / PATCH / DELETE | `/api/club/members` | CRUD membres |
| POST | `/api/club/licenses/:id/assign-member` | Lier license à un membre |
| GET / POST / POST `:id/revoke` | `/api/club/licenses` | Licenses du club |
| GET | `/api/club/audit` | Audit log scope club |
| GET / POST `:id/approve` / `:id/reject` / `:id/revoke` | `/api/club/memberships` | Phase 0.F : modération adhésions joueurs ↔ club |
| GET / POST | `/api/club/tournaments` | Tournois publiés (UPSERT à chaque publish, incluant audience public/members_only) |
| POST | `/api/club/tournaments/:id/close` | Clôt les inscriptions |
| POST | `/api/club/tournaments/:id/cancel` | Annule le tournoi publié |
| POST | `/api/club/tournaments/:id/results` | Publie le classement final (avec ou sans calcul auto via barème) |
| GET | `/api/club/tournaments/:tid/registrations` | Liste les inscriptions à modérer |
| POST | `/api/club/registrations/:id/confirm` | Confirme une inscription pending |
| POST | `/api/club/registrations/:id/reject` | Refuse une inscription pending |
| GET / PUT / DELETE | `/api/club/point-scheme` | Barème de points du club (linear / fixed / proportional) |

### Super-admin (`/api/admin/*`, JWT avec role superadmin)

| Méthode | Chemin | Description |
|---|---|---|
| GET / POST / GET `:id` / PATCH / DELETE | `/api/admin/clubs` | CRUD clubs |
| POST | `/api/admin/clubs/:id/suspend` (et unsuspend) | État club |
| GET | `/api/admin/clubs/:id/members` | Visibilité super-admin |
| GET / POST / POST `:id/revoke` | `/api/admin/licenses` | Licenses cross-clubs |
| GET | `/api/admin/audit` | Audit log cross-clubs |

### Structures partagées (Phase 0.C)

`/api/structures` (JWT scope club). CRUD modèles de tournoi partagés entre PCs du même club.

## Flow auth desktop

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
  │                      │ { valid, tier, role,    │
  │                      │   clubId }              │
  │                      │ Sign RS256 JWT          │
  │ ◄────────────────────┤                         │
  │ { jwt, refreshToken }│                         │
```

## Flow auth joueur (magic link)

```
Browser             pokclock-react        pokclock-api          Resend
  │                       │                    │                  │
  │ POST /joueurs/login   │                    │                  │
  ├─────────────────────► │                    │                  │
  │                       │ POST magic-link    │                  │
  │                       ├──────────────────► │                  │
  │                       │                    │ Send email       │
  │                       │                    ├────────────────► │
  │                       │ ◄────────────────  │ 202 Accepted     │
  │                       │                    │                  │
  │ User clicks email link → GET /api/players/auth/magic-link/verify?token=…
  │                                            │
  │                                            │ DELETE+UPDATE token used_at
  │                                            │ UPSERT players row
  │                                            │ Issue JWT (audience pokclock-player)
  │                                            │ Set-Cookie pokclock_player
  │                                            │   Domain=.pokclock.com SameSite=None Secure
  │ ◄────────────────────────────────────────  │ 302 → /joueurs/dashboard
  │                                            │
```

## Variables d'environnement

| Variable | Requis | Description |
|---|---|---|
| `DATABASE_URL` | oui | Connexion Postgres |
| `JWT_PRIVATE_KEY_PATH` | oui | Chemin vers la clé privée RSA (en prod : `/run/secrets/jwt_private`) |
| `JWT_PUBLIC_KEY_PATH` | oui | Chemin vers la clé publique RSA |
| `WORKER_VERIFY_URL` | oui | URL du Worker Cloudflare `/verify` |
| `WORKER_ADMIN_URL` | non | URL du Worker pour propagation licenses ops |
| `WORKER_ADMIN_TOKEN_FILE` | non | Path d'un secret Swarm contenant le Bearer ADMIN_TOKEN |
| `RESEND_API_KEY_FILE` | non | Path d'un secret Swarm contenant la clé Resend (sinon `RESEND_API_KEY` direct) |
| `EMAIL_FROM` | non | Sender Resend, défaut `PokClock <noreply@pokclock.com>` |
| `PUBLIC_SITE_URL` | non | URL frontend, défaut `https://pokclock.com` (utilisé pour les redirects post-magic-link) |
| `API_BASE_URL` | non | URL self, défaut `https://api.pokclock.com` (utilisé pour les liens dans les emails magic link) |
| `PLAYER_COOKIE_DOMAIN` | non | Domaine du cookie de session joueur. En prod `.pokclock.com` pour cross-subdomain (`pokclock.com` ↔ `api.pokclock.com`). Vide en dev. |
| `GOOGLE_OAUTH_CLIENT_ID` | non | Client ID OAuth GCP (Phase 0.E.1.e). Si vide, bouton "Continuer avec Google" et "Connecter mon agenda" inactifs. |
| `GOOGLE_OAUTH_CLIENT_SECRET_FILE` | non | Path d'un secret Swarm contenant le Client Secret OAuth. Prioritaire sur `GOOGLE_OAUTH_CLIENT_SECRET` direct. |
| `ALLOWED_ORIGINS` | non | CSV pour CORS (`Allow-Credentials: true` est toujours appliqué pour le cookie joueur) |
| `SUPERADMIN_LICENSE_KEYS` | non | CSV de bootstrap pour Phase 0.B (à retirer une fois le Worker /verify expose `role`) |
| `PORT` | non | Défaut 8080 |
| `LOG_LEVEL` | non | `debug`, `info` (défaut), `warn`, `error` |

## Structure du projet

```
pokclock-api/
├── cmd/api/main.go          # Entry point
├── cmd/issue-jwt/main.go    # CLI bootstrap : émet un JWT superadmin local
├── internal/
│   ├── api/                  # Handlers HTTP, router, middleware (jwt desktop + cookie joueur)
│   ├── clients/
│   │   ├── resend/           # Client HTTP Resend pour les emails
│   │   └── workeradmin/      # Client HTTP du Worker /admin (propagation licenses)
│   ├── domain/auth/          # JWT signing (desktop + player), claims, Worker client
│   └── db/migrations/        # 001-018, runtime goose au démarrage
├── Dockerfile                # Multi-stage (alpine final)
├── docker-compose.swarm.prod.yml
└── Makefile
```

Tables Postgres au 2026-05-27 :
`structures`, `structures_audit`, `clubs`, `licenses`, `members`,
`published_tournaments`, `tournament_registrations`, `point_schemes`,
`players`, `player_magic_links`, `tournament_results`,
`player_club_memberships`, `player_google_tokens`.

Voir [`docs/DATABASE_SCHEMA.md`](docs/DATABASE_SCHEMA.md) pour le détail
(colonnes, contraintes, consommateurs par table) et [`docs/PLAYERS_DATA_STORAGE.md`](docs/PLAYERS_DATA_STORAGE.md) pour le focus joueurs + RGPD.

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

Migrations goose appliquées au démarrage du container. Rollback : `docker service update --rollback pokclock-api_api`.

## Voir aussi

- [PHASE-0A-PROGRESS.md](../PHASE-0A-PROGRESS.md) — kanban Sprint 0
- [PHASE-0B-OPS-ROADMAP.md](../PHASE-0B-OPS-ROADMAP.md) — bilan Phases 0.B / 0.C / 0.D / 0.E
- `pokclock-infra/` — Traefik + monitoring (repo séparé)
