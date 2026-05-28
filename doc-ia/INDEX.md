# PokClock — Index documentation pour IA

> Hub d'orientation pour assistants IA travaillant sur PokClock. Lis ce fichier en premier.
> Version humaine illustrée (HTML + Mermaid) : `../docs/docs.html`.

## TL;DR projet

PokClock = système de gestion de tournois de poker, **3 repos** :

| Repo | Rôle | Stack | Path local |
|---|---|---|---|
| `pokclock-api` | Backend cloud Go | Echo + pgx + JWT RS256 + goose | `~/workspace/pokclock-api/` (WSL) |
| `pokclock-react` | Site public + dashboards joueurs | Next.js 14 App Router + next-intl | `~/workspace/pokclock-react/` (WSL) |
| `PokClock` (alias StackPilot) | App desktop Windows | WPF .NET 8 + EF Core SQLite + Velopack | `C:\Users\cédric\source\repos\StackPilot\` |

**Service en prod** : `https://api.pokclock.com` (VPS OVH Swarm), `https://pokclock.com` (site), Worker Cloudflare `pokclock-license.dynamidoxa.workers.dev`.

Phases livrées en mai 2026 : `0.A` foundation → `0.G` saisons. Détail dans `PHASE-0B-OPS-ROADMAP.md`.

## Fichiers de ce dossier

| Fichier | Contenu | Quand le lire |
|---|---|---|
| [`DATABASE_SCHEMA.md`](DATABASE_SCHEMA.md) | 13 tables Postgres, colonnes, FK, index, qui-tape-quoi par endpoint | Avant toute modif DB ou nouveau handler |
| [`PLAYERS_DATA_STORAGE.md`](PLAYERS_DATA_STORAGE.md) | Stockage joueurs + RGPD (DELETE /me, anonymisation, backups) | Quand le sujet touche aux comptes joueurs, à la conformité, ou aux backups |
| [`PHASE-0B-OPS-ROADMAP.md`](PHASE-0B-OPS-ROADMAP.md) | Bilans cumulés des phases 0.B → 0.G, état config VPS, reste à faire | Pour comprendre l'historique récent et identifier les chantiers ouverts |

## Memory shortcuts

Quand un humain (Cédric) te pose une question PokClock, vérifie d'abord :
- **Mémoire personnelle Claude** : `MEMORY.md` (notes feedback + project) pour les conventions et la règle "demander avant push"
- **CLAUDE.md du repo StackPilot** : règles d'opération sur les 3 repos (autorisations, périmètre)
- **Ce dossier** pour le détail technique

## Convention de naming des phases

Les phases sont nommées `0.X[.lettre[.chiffre]]` :
- `0.A` : Phase initiale (foundation)
- `0.B` : Sub-phase majeure
- `0.B.γ.1` : Sub-sub-phase (lettre grecque pour les milestones, chiffre pour l'incrément)
- Convention bilingue FR/EN : commits + commentaires code en FR principalement, identifiants code en EN.

## Routes API — vue cathédrale

Backend Go (`pokclock-api`) expose 4 groupes de routes :

```
/api/health                          GET    public
/.well-known/jwks.json               GET    public

/api/auth/issue                      POST   public (license desktop → JWT)
/api/auth/refresh                    POST   refresh token

/api/players/auth/*                  GET/POST  public (magic link + Google OAuth login)
/api/players/me/*                    GET/PATCH/DELETE  cookie pokclock_player
/api/players/me/clubs/*              POST/DELETE  adhésions clubs
/api/players/me/google-calendar/*    OAuth scope calendar.events

/public/clubs/:slug/*                GET/POST  public anonyme (lecture + inscription)
/public/clubs/:slug/seasons/*        GET       leaderboard scopé saison
/public/registrations/:token/cancel  GET       magic link annulation email

/api/club/*                          GET/POST/PATCH/DELETE  JWT desktop + role admin
                                     (members, licenses, tournaments, registrations,
                                      point-scheme, memberships, seasons, audit)

/api/admin/*                         GET/POST/PATCH/DELETE  JWT role superadmin (cross-clubs)
```

Inventaire complet dans `../README.md` du repo (section Endpoints).

## Tables Postgres au 2026-05-28

13 tables au total :

```
clubs ─┬─ licenses (+ members FK)
       ├─ members
       ├─ structures ─── structures_audit
       ├─ point_schemes
       ├─ seasons
       └─ published_tournaments ─┬─ tournament_registrations (player_id?)
                                  └─ tournament_results (player_id?)

players ─┬─ player_magic_links (by email)
         ├─ player_club_memberships (× clubs)
         └─ player_google_tokens (1-to-1)
```

Voir [`DATABASE_SCHEMA.md`](DATABASE_SCHEMA.md) pour le détail colonne par colonne.

## Conventions code

### Go (pokclock-api)
- `internal/api/*.go` : un fichier par sous-domaine REST (`club_tournaments.go`, `players_auth.go`, etc.)
- `internal/api/middleware.go` : `jwtAuthMiddleware`, `requireRole`, `requireClubScope`, `playerAuthMiddleware`
- Migrations goose dans `internal/db/migrations/NNN_description.sql` (appliquées au démarrage)
- `internal/clients/{resend,workeradmin,googlecal}/` : clients HTTP tiers
- Pattern `defer tx.Rollback(...) //nolint:errcheck` pour les transactions
- Tests rares (auth/signer seulement) — la CI lance `go vet` + `golangci-lint`

### TypeScript (pokclock-react)
- `src/app/[locale]/.../page.tsx` : RSC server components
- `src/app/[locale]/.../actions.ts` : server actions (formulaires)
- `src/lib/pokclock-api.ts` + `src/lib/player-auth.ts` : clients HTTP server-side
- `src/components/players/*` : composants client interactifs
- Cookie forward manuel via `cookies()` de `next/headers`

### C# WPF (StackPilot)
- Clean Architecture stricte : Domain ← Application ← Infrastructure ← Presentation
- MVVM avec `CommunityToolkit.Mvvm`, `[ObservableProperty]`, `AsyncRelayCommand`
- Code en EN, commentaires FR
- Dialogs en code-behind (pas MVVM-purs) : `MemberDialog`, `PublishSettingsDialog`, `PointSchemeDialog`, `PublishResultsDialog`, `SeasonDialog`
- API client : `PokClock.Infrastructure/Security/PokClockApiClient.cs`

## Règles d'opération (extraites de CLAUDE.md du repo StackPilot)

- ✅ Autorisé sans demander : créer/modifier des fichiers, créer commits locaux, lancer dotnet build/test/run
- ⛔ Demander avant : `git push`, `git push --force`, suppression de fichiers, drop DB, modif `.gitignore` pour exposer des secrets
- ⛔ Jamais : modifier le `SECRET_SALT` license, paste de secrets dans le chat

## État des chantiers (mai 2026)

**Shippé** : 0.A foundation, 0.B RBAC + ops, 0.C members, 0.D tournois publiés (+ γ.1 barème, γ.3 formule custom), 0.E joueurs (magic link + Google OAuth + Calendar + RGPD), 0.F adhésions + members_only, 0.G saisons.

**Pending** :
- Stripe Checkout (besoin compte + clés API)
- F2 PokClock Live (WebSocket realtime, scoping à faire)
- F4 Multi-Canaux Auto (Discord/Telegram/Insta/FB, clés API requises)
- Setup VPS env vars : `PLAYER_COOKIE_DOMAIN=.pokclock.com`, `GOOGLE_OAUTH_CLIENT_ID/SECRET`
- Push WPF depuis Windows (12 commits accumulés au 2026-05-27)

## Pour creuser davantage

- Code source : https://github.com/cedricleblond35/pokclock-api
- README repo : `../README.md`
- Mémoires Claude (cross-session) : `~/.claude/projects/-home-cedric/memory/`
- Roadmap "haute" (4 sprints F1-F4) : `C:\Users\cédric\source\repos\StackPilot\CLOUD_EVOLUTIONS_ROADMAP.md`
- Notes session datée par Claude : `C:\Users\cédric\source\repos\StackPilot\ROADMAP.md` § "Notes de session"
