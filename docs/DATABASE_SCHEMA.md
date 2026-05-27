# Schéma base de données PokClock

> Référence technique exhaustive : toutes les tables Postgres du backend `pokclock-api`, leur structure, les relations entre elles, et la liste des consommateurs (handlers HTTP + apps clientes).

À jour au **2026-05-27**, après migration 023.

Postgres 16-alpine, service Swarm `pokclock-api_postgres` sur VPS OVH `vps-eb94174b`. Toutes les tables vivent dans une seule base `pokclock`. Migrations gérées par `goose` au boot de l'API.

---

## Vue d'ensemble — diagramme des relations

```
                  ┌──────────┐
                  │  clubs   │
                  └────┬─────┘
                       │ 1
        ┌──────────────┼──────────────┬────────────────┐
        │              │              │                │
        │ N            │ N            │ N              │ 0..1
        ▼              ▼              ▼                ▼
   ┌─────────┐  ┌──────────┐  ┌──────────────────┐  ┌────────────────┐
   │ members │  │ licenses │  │ published_       │  │ point_schemes  │
   │         │  │          │  │   tournaments    │  │ (1-to-1 club)  │
   └────┬────┘  └──────────┘  └────────┬─────────┘  └────────────────┘
        │ 0..1                          │ 1
        │ (assign member to license)    │
        └─► licenses.member_id          ├──────────────┐
                                        │ N            │ N
                                        ▼              ▼
                            ┌──────────────────┐  ┌────────────────────┐
                            │ tournament_      │  │ tournament_results │
                            │   registrations  │  │                    │
                            └────────┬─────────┘  └──────────┬─────────┘
                                     │ 0..1                  │ 0..1
                                     │                       │
                                     ▼                       ▼
                                ┌────────────────────────────────┐
                                │           players              │
                                └──────────────┬─────────────────┘
                                               │ logique
                                               │ (pas de FK, keyé par email)
                                               ▼
                                  ┌─────────────────────────────┐
                                  │   player_magic_links        │
                                  └─────────────────────────────┘


               ┌────────────┐         ┌──────────────────┐
               │ structures │ ─ N ──► │ structures_audit │
               └────────────┘         └──────────────────┘
                     ▲
                     │ 0..1 (club_id, nullable rétro-compat Phase 0.A)
                     │
               ┌─────┴────┐
               │  clubs   │
               └──────────┘
```

`clubs` est l'agrégat racine. Tout le reste (sauf `players` et ses dépendances) y est rattaché par FK avec `ON DELETE CASCADE`.

---

## Inventaire des tables

| Table | Rôle | Volumétrie attendue |
|---|---|---|
| [`clubs`](#clubs) | Organisation/casino/association | ~10-100 |
| [`licenses`](#licenses) | Licence desktop activée sur un PC | ~10-1000 |
| [`members`](#members) | Personnes d'un club (croupiers, admins) | ~100-1000 |
| [`structures`](#structures) | Modèles de tournoi partagés cross-PC du club | ~10-100 par club |
| [`structures_audit`](#structures_audit) | Audit log des changements sur `structures` | grossit linéairement |
| [`published_tournaments`](#published_tournaments) | Tournoi publié sur le site public | ~100-10000 |
| [`tournament_registrations`](#tournament_registrations) | Inscription joueur à un tournoi publié | ~1000-100000 |
| [`tournament_results`](#tournament_results) | Classement final d'un tournoi | ~position_count par tournoi |
| [`point_schemes`](#point_schemes) | Barème de points du club (1-to-1) | = nombre de clubs |
| [`players`](#players) | Compte joueur (magic link auth) | ~100-10000 |
| [`player_magic_links`](#player_magic_links) | Tokens login passwordless temporaires | auto-purgé /6h |
| [`player_club_memberships`](#player_club_memberships) | Adhésion joueur ↔ club (Phase 0.F) | ~1000-50000 |
| [`player_google_tokens`](#player_google_tokens) | Tokens OAuth Calendar par joueur (Phase 0.E.5) | sous-ensemble des players |

---

## clubs

> L'organisation racine. Un club = un casino, une association, un club amateur. Toutes les autres ressources (sauf joueurs) lui appartiennent.

| Colonne | Type | Notes |
|---|---|---|
| `id` | uuid PK | `uuid_generate_v4()` |
| `name` | text NOT NULL UNIQUE | Affichage utilisateur |
| `slug` | text NOT NULL UNIQUE | URL-safe, généré côté admin. Utilisé dans `/public/clubs/:slug/*`. Ajouté en 008 |
| `plan` | text NOT NULL | `solo` / `club` / `pro`, default `solo` |
| `email` | text NULL | Email du club (activation licenses desktop) |
| `status` | text NOT NULL | `active` / `suspended` / `deleted`, default `active` |
| `notes` | text NULL | Notes internes admin |
| `online_registrations_enabled` | bool NOT NULL | Opt-in pour les inscriptions en ligne. Ajouté en 008. Default `false` |
| `created_at`, `updated_at` | timestamptz | Trigger `set_updated_at` |
| `suspended_at` | timestamptz NULL | Renseigné au passage `active → suspended` |

Index : `idx_clubs_status`, `idx_clubs_plan`, `idx_clubs_slug` (UNIQUE).

Migrations : 003 (create), 008 (slug + online_registrations_enabled).

### Qui tape sur `clubs`

| Acteur | Endpoint / opération | Lecture | Écriture |
|---|---|---|---|
| Super-admin | `GET /api/admin/clubs` (list paginée + filtres) | x | |
| Super-admin | `POST /api/admin/clubs` (create) | | x |
| Super-admin | `GET /api/admin/clubs/:id` | x | |
| Super-admin | `PATCH /api/admin/clubs/:id` | | x |
| Super-admin | `POST /api/admin/clubs/:id/suspend` / `unsuspend` | | x |
| Super-admin | `DELETE /api/admin/clubs/:id` (soft delete) | | x |
| Club admin | `GET /api/club/info` | x | |
| Club admin | `PATCH /api/club/online-registrations` | | x |
| Public | `GET /public/clubs/:slug/tournaments` (resolveClub par slug) | x | |
| Public | `GET /public/clubs/:slug/tournaments/:id` (resolveClub) | x | |
| Public | `GET /public/clubs/:slug/leaderboard` (resolveClub) | x | |
| Worker `/verify` | JOIN pour récupérer `club.id` au moment d'émettre le JWT | x | |

Handler : [`internal/api/admin_clubs.go`](../internal/api/admin_clubs.go), [`internal/api/club_audit_info.go`](../internal/api/club_audit_info.go).

---

## licenses

> Une license desktop activée sur un PC. Liée à un club (FK CASCADE) et optionnellement à un member (FK SET NULL).

| Colonne | Type | Notes |
|---|---|---|
| `id` | uuid PK | |
| `club_id` | uuid NOT NULL FK → clubs.id ON DELETE CASCADE | |
| `license_key` | text NOT NULL UNIQUE | Format `POK-XXXX-XXXX-XXXX-XXXX` |
| `worker_token_hash` | text NOT NULL | SHA-256 hex du token Worker. Original jamais persisté |
| `hardware_id` | text NULL | Set par le Worker au premier activate |
| `role` | text NOT NULL | `member` / `admin` / `superadmin`, default `member` |
| `status` | text NOT NULL | `active` / `revoked` / `expired`, default `active` |
| `expires_at` | timestamptz NULL | NULL = perpétuel |
| `last_seen_at` | timestamptz NULL | Maj à chaque `/api/auth/issue` |
| `created_at`, `updated_at` | timestamptz | Trigger |
| `revoked_at` | timestamptz NULL | Renseigné au revoke |
| `member_id` | uuid NULL FK → members.id ON DELETE SET NULL | Ajouté en 007. Optionnel |

Index : `idx_licenses_club_id`, `idx_licenses_status`, `idx_licenses_hardware_id`, `idx_licenses_member_id`.

Migrations : 004 (create), 007 (member_id FK).

### Qui tape sur `licenses`

| Acteur | Endpoint / opération | Lecture | Écriture |
|---|---|---|---|
| Super-admin | `GET /api/admin/licenses` (list + filtres clubId/status/role) | x | |
| Super-admin | `POST /api/admin/licenses` (génère key + token, hash en SHA-256, propage au Worker D1) | | x |
| Super-admin | `POST /api/admin/licenses/:id/revoke` | | x |
| Club admin | `GET /api/club/licenses` (limité à `claims.ClubID`) | x | |
| Club admin | `POST /api/club/licenses` (idem) | | x |
| Club admin | `POST /api/club/licenses/:id/revoke` | | x |
| Club admin | `POST /api/club/licenses/:id/assign-member` (set/clear `member_id`) | | x |
| API auth | `POST /api/auth/issue` (lecture du `worker_token_hash` pour validation) | x | |

Handler : [`internal/api/admin_licenses.go`](../internal/api/admin_licenses.go), [`internal/api/club_licenses.go`](../internal/api/club_licenses.go), [`internal/api/auth.go`](../internal/api/auth.go).

---

## members

> Personnes rattachées à un club (croupier, dealer, admin de club). Indépendant des licenses : un membre peut exister sans license, une license peut être non assignée.

| Colonne | Type | Notes |
|---|---|---|
| `id` | uuid PK | |
| `club_id` | uuid NOT NULL FK → clubs.id ON DELETE CASCADE | |
| `first_name`, `last_name` | text NOT NULL | |
| `email` | text NULL | Contact direct, indépendant de l'email d'activation license |
| `notes` | text NULL | |
| `status` | text NOT NULL | `active` / `inactive`, default `active` |
| `created_at`, `updated_at` | timestamptz | Trigger |

Index : `idx_members_club_id`, `idx_members_status`.

Migration : 007.

### Qui tape sur `members`

| Acteur | Endpoint / opération | Lecture | Écriture |
|---|---|---|---|
| Club admin | `GET /api/club/members` | x | |
| Club admin | `POST /api/club/members` | | x |
| Club admin | `PATCH /api/club/members/:id` | | x |
| Club admin | `DELETE /api/club/members/:id` | | x |
| Super-admin | `GET /api/admin/clubs/:id/members` (visibilité cross-club) | x | |
| Club admin via licenses | LEFT JOIN dans `GET /api/club/licenses` pour afficher le nom du titulaire | x | |

Handler : [`internal/api/club_members.go`](../internal/api/club_members.go), [`internal/api/admin_clubs.go`](../internal/api/admin_clubs.go).

---

## structures

> Modèle de tournoi (TournamentPreset) sérialisé en jsonb. Partage cross-PC d'un même club. `kind` indique l'origine (club / casino / home / streamer / other), `settings` contient le payload structuré.

| Colonne | Type | Notes |
|---|---|---|
| `id` | uuid PK | |
| `club_id` | uuid NULL FK → clubs.id ON DELETE CASCADE | Nullable en 001/005 pour rétro-compat Phase 0.A, devenu NOT NULL en 006 |
| `slug` | text NOT NULL UNIQUE par club | Index UNIQUE composite `(club_id, slug)` |
| `name` | text NOT NULL | |
| `kind` | text NOT NULL CHECK | `club` / `casino` / `home` / `streamer` / `other` |
| `settings` | jsonb NOT NULL DEFAULT `{}` | Payload : `ClubStructureSettings` côté C# |
| `created_at`, `updated_at` | timestamptz | Trigger |

Index : `idx_structures_kind`, UNIQUE `(club_id, slug)`.

Migrations : 001 (create), 005 (club_id nullable), 006 (NOT NULL + index unique).

### Qui tape sur `structures`

| Acteur | Endpoint / opération | Lecture | Écriture |
|---|---|---|---|
| App WPF (license `member`/`admin`/`superadmin`) | `GET /api/structures` | x | |
| App WPF | `POST /api/structures` | | x |
| App WPF | `GET /api/structures/:slug` | x | |
| App WPF | `PATCH /api/structures/:slug` (admin/superadmin only) | | x |
| App WPF | `DELETE /api/structures/:slug` | | x |

Handler : [`internal/api/structures.go`](../internal/api/structures.go).

---

## structures_audit

> Audit log immutable des changements sur `structures`. Action = `created` / `updated` / `deleted`, payload diff dans `changes` (jsonb).

| Colonne | Type | Notes |
|---|---|---|
| `id` | uuid PK | |
| `structure_id` | uuid NOT NULL FK → structures.id ON DELETE CASCADE | |
| `action` | text NOT NULL CHECK | `created` / `updated` / `deleted` |
| `changes` | jsonb NOT NULL | Diff structuré selon l'action |
| `actor` | text NOT NULL | Typiquement la license_key ou le subject du JWT |
| `created_at` | timestamptz | |

Index : `idx_structures_audit_structure_id`, `idx_structures_audit_created_at DESC`.

Migration : 002.

### Qui tape sur `structures_audit`

| Acteur | Opération | Lecture | Écriture |
|---|---|---|---|
| Handlers `structures.go` | INSERT dans la même transaction que les opérations CRUD sur `structures` | | x |
| Super-admin | `GET /api/admin/audit` (cross-clubs, filtres clubId/actor/action/dates) | x | |
| Club admin | `GET /api/club/audit` (scope `claims.ClubID` via JOIN sur structures) | x | |

Handler : [`internal/api/structures.go`](../internal/api/structures.go) (writes), [`internal/api/admin_audit.go`](../internal/api/admin_audit.go) + [`internal/api/club_audit_info.go`](../internal/api/club_audit_info.go) (reads).

---

## published_tournaments

> Tournoi publié sur le site public depuis l'app WPF via "Publier en ligne". Snapshot des paramètres du tournoi local + state online. UPSERT lors de la republication (anti-doublon via index unique partiel).

| Colonne | Type | Notes |
|---|---|---|
| `id` | uuid PK | |
| `club_id` | uuid NOT NULL FK → clubs.id ON DELETE CASCADE | |
| `name` | text NOT NULL | |
| `description` | text NULL | |
| `start_at` | timestamptz NOT NULL | |
| `buy_in_amount` | numeric(10,2) NOT NULL DEFAULT 0 | Précision décimale préservée |
| `buy_in_fees` | numeric(10,2) NOT NULL DEFAULT 0 | |
| `starting_stack` | integer NOT NULL DEFAULT 0 | |
| `max_players` | integer NOT NULL DEFAULT 0 | 0 = illimité |
| `format` | text NOT NULL DEFAULT `Freezeout` | |
| `late_reg_enabled` | bool NOT NULL DEFAULT true | |
| `late_reg_until_level` | integer NULL | |
| `local_tournament_id` | integer NULL | ID local côté SQLite WPF, sert à l'upsert |
| `status` | text NOT NULL | `open` / `closed` / `cancelled`, default `open` |
| `blinds_summary` | jsonb NULL | Résumé blinds pour affichage public |
| `show_players` | bool NOT NULL DEFAULT false | Ajouté en 012 |
| `players_display_mode` | text NOT NULL DEFAULT `hidden` | `hidden` / `pseudo` / `first_initial_last` / `full_name`. Ajouté en 012 |
| `local_players` | jsonb NOT NULL DEFAULT `[]` | Snapshot des joueurs locaux poussés par WPF. Ajouté en 012 |
| `prize_structure` | jsonb NULL | Payouts calculés côté WPF. Ajouté en 013 |
| `audience` | text NOT NULL DEFAULT `public` CHECK | `public` ou `members_only`. Ajouté en 021 (Phase 0.F) |
| `published_by_license` | text NOT NULL | License_key du publisher |
| `published_at`, `updated_at` | timestamptz | |

Index :
- `idx_published_tournaments_club_id`
- `idx_published_tournaments_status`
- `idx_published_tournaments_start_at`
- `idx_published_tournaments_local_active` UNIQUE partiel `WHERE local_tournament_id IS NOT NULL AND status <> 'cancelled'` (ajouté en 011, anti-doublon republish)

Migrations : 009 (create), 011 (partial unique index), 012 (show_players/display_mode/local_players), 013 (prize_structure).

### Qui tape sur `published_tournaments`

| Acteur | Endpoint / opération | Lecture | Écriture |
|---|---|---|---|
| Club admin | `GET /api/club/tournaments` (list + pending/confirmed counts) | x | |
| Club admin via WPF | `POST /api/club/tournaments` (UPSERT par `(club_id, local_tournament_id)`) | | x |
| Club admin | `POST /api/club/tournaments/:id/close` / `cancel` | | x |
| Club admin | `POST /api/club/tournaments/:id/results` (passe en `closed` si `closeTournament=true`) | x | x |
| Public | `GET /public/clubs/:slug/tournaments` | x | |
| Public | `GET /public/clubs/:slug/tournaments/:id` (LEFT JOIN point_schemes, calcul confirmedCount + localPlayersCount) | x | |
| Public | `POST /public/clubs/:slug/tournaments/:id/register` (lit capacity + jsonb_array_length(local_players)) | x | |
| Joueur authentifié | `POST /api/players/me/clubs/:slug/tournaments/:id/register` (idem + player_id) | x | |
| Joueur authentifié | `GET /api/players/me/registrations` (JOIN pour récupérer nom/start_at) | x | |

Handler : [`internal/api/club_tournaments.go`](../internal/api/club_tournaments.go), [`internal/api/public_tournaments.go`](../internal/api/public_tournaments.go), [`internal/api/club_tournament_results.go`](../internal/api/club_tournament_results.go), [`internal/api/players_registrations.go`](../internal/api/players_registrations.go).

---

## tournament_registrations

> Inscription d'un joueur à un tournoi publié. Peut être anonyme (Phase 0.D-α, identifié par `cancel_token`) ou liée à un compte joueur (Phase 0.E.2 via `player_id`).

| Colonne | Type | Notes |
|---|---|---|
| `id` | uuid PK | |
| `published_tournament_id` | uuid NOT NULL FK → published_tournaments.id ON DELETE CASCADE | |
| `first_name`, `last_name`, `email` | text NOT NULL | Données saisies à l'inscription. Anonymisées au DELETE player |
| `phone` | text NULL | |
| `status` | text NOT NULL | `pending` / `confirmed` / `rejected` / `cancelled`, default `pending` |
| `moderated_by_license` | text NULL | License_key de l'admin qui a confirmé/refusé |
| `moderated_at` | timestamptz NULL | |
| `moderation_reason` | text NULL | Motif optionnel sur reject |
| `cancel_token` | text NOT NULL UNIQUE | 32 bytes base64url, magic link annulation email |
| `player_id` | uuid NULL FK → players.id ON DELETE SET NULL | Ajouté en 017. Set si inscription via compte |
| `google_event_id` | text NULL | ID de l'event Google Calendar associé (Phase 0.E.5). Set via calendarSync. NULL si joueur n'a pas Calendar connecté ou si l'API a échoué. Ajouté en 023 |
| `registered_at`, `updated_at` | timestamptz | |

Index :
- `idx_tournament_registrations_tournament` `(published_tournament_id)`
- `idx_tournament_registrations_status`
- `idx_tournament_registrations_email`
- `tournament_registrations_player_id_idx` `WHERE player_id IS NOT NULL`
- `tournament_registrations_player_active_uniq` UNIQUE partiel `(published_tournament_id, player_id) WHERE player_id IS NOT NULL AND status IN ('pending','confirmed')` (anti-doublon player)

Migrations : 010 (create), 017 (player_id + indexes).

### Qui tape sur `tournament_registrations`

| Acteur | Endpoint / opération | Lecture | Écriture |
|---|---|---|---|
| Public anonyme | `POST /public/clubs/:slug/tournaments/:id/register` | x | x |
| Public anonyme | `GET /public/registrations/:token/cancel` (UPDATE status = cancelled) | | x |
| Joueur authentifié | `POST /api/players/me/clubs/:slug/tournaments/:id/register` | x | x |
| Joueur authentifié | `DELETE /api/players/me/registrations/:id` (scope `player_id = claims.PlayerID`) | | x |
| Joueur authentifié | `GET /api/players/me/registrations` | x | |
| Club admin | `GET /api/club/tournaments/:tid/registrations` (filtre par status) | x | |
| Club admin | `POST /api/club/registrations/:id/confirm` / `reject` | | x |
| Club admin via WPF | `POST /api/club/tournaments` (auto-sync recompte les actives) | x | |
| Public | `GET /public/clubs/:slug/tournaments` (compte confirmed pour `confirmedCount`) | x | |
| Joueur RGPD | `DELETE /api/players/me` (anonymise les rows du player) | | x |

Handler : [`internal/api/public_tournaments.go`](../internal/api/public_tournaments.go), [`internal/api/club_registrations.go`](../internal/api/club_registrations.go), [`internal/api/players_registrations.go`](../internal/api/players_registrations.go), [`internal/api/players_me.go`](../internal/api/players_me.go) (deleteMe).

---

## tournament_results

> Classement final d'un tournoi. Publié depuis le WPF après le tournoi. Sert pour le leaderboard agrégé du club.

| Colonne | Type | Notes |
|---|---|---|
| `id` | uuid PK | |
| `published_tournament_id` | uuid NOT NULL FK → published_tournaments.id ON DELETE CASCADE | |
| `position` | int NOT NULL CHECK >= 1 | 1 = vainqueur |
| `first_name`, `last_name` | text NOT NULL | Anonymisés au DELETE player |
| `player_id` | uuid NULL FK → players.id ON DELETE SET NULL | Set si l'admin a lié le résultat à un compte |
| `points` | int NOT NULL DEFAULT 0 | Calculé via `point_schemes` ou fourni par WPF |
| `prize_amount` | text NULL | Décimal en string, ex `"100.00"` |
| `created_at` | timestamptz | |

Contraintes : UNIQUE `(published_tournament_id, position)`.

Index : `tournament_results_tournament_idx`, `tournament_results_player_idx` `WHERE player_id IS NOT NULL`.

Migration : 018.

### Qui tape sur `tournament_results`

| Acteur | Endpoint / opération | Lecture | Écriture |
|---|---|---|---|
| Club admin via WPF | `POST /api/club/tournaments/:id/results` (DELETE+INSERT en transaction, calcul points via `point_schemes` si `applyPointScheme=true`) | x | x |
| Public | `GET /public/clubs/:slug/tournaments/:id/results` | x | |
| Public | `GET /public/clubs/:slug/leaderboard` (agrégation GROUP BY `lower(first_name), lower(last_name)`) | x | |
| Joueur RGPD | `DELETE /api/players/me` (anonymise les rows du player) | | x |

Handler : [`internal/api/club_tournament_results.go`](../internal/api/club_tournament_results.go), [`internal/api/public_results.go`](../internal/api/public_results.go), [`internal/api/players_me.go`](../internal/api/players_me.go) (deleteMe).

---

## point_schemes

> Barème de points d'un club. **1-to-1 avec clubs** (PRIMARY KEY = `club_id`, pas d'`id` auto). Configurable depuis WPF via `Mon club > INFO > Configurer le barème`.

| Colonne | Type | Notes |
|---|---|---|
| `club_id` | uuid PK FK → clubs.id ON DELETE CASCADE | |
| `name` | text NOT NULL | Affichage public |
| `type` | text NOT NULL CHECK | `linear` / `fixed` / `proportional` |
| `params` | jsonb NOT NULL DEFAULT `{}` | Schéma dépend du type (cf. `PointsCalculator` côté WPF + helper Go) |
| `updated_at` | timestamptz | |

Migration : 014.

### Qui tape sur `point_schemes`

| Acteur | Endpoint / opération | Lecture | Écriture |
|---|---|---|---|
| Club admin via WPF | `GET /api/club/point-scheme` | x | |
| Club admin via WPF | `PUT /api/club/point-scheme` (UPSERT) | | x |
| Club admin via WPF | `DELETE /api/club/point-scheme` | | x |
| Club admin via WPF | `POST /api/club/tournaments/:id/results` avec `applyPointScheme=true` (lit le barème pour calculer points) | x | |
| Public | `GET /public/clubs/:slug/tournaments/:id` (LEFT JOIN, expose `pointScheme` au site) | x | |

Handler : [`internal/api/club_point_schemes.go`](../internal/api/club_point_schemes.go), [`internal/api/club_tournament_results.go`](../internal/api/club_tournament_results.go), [`internal/api/public_tournaments.go`](../internal/api/public_tournaments.go).

---

## players

> Compte joueur (Phase 0.E.1). Auth passwordless via magic link email + Google OAuth (à venir 0.E.1.e). Aucune FK vers `clubs` : un joueur est cross-clubs par design (il peut s'inscrire dans plusieurs).

| Colonne | Type | Notes |
|---|---|---|
| `id` | uuid PK | |
| `email` | text NOT NULL UNIQUE | En clair. Indexé en `lower(email)` |
| `google_id` | text UNIQUE NULL | `sub` Google OAuth (0.E.1.e, non encore actif) |
| `first_name`, `last_name` | text NOT NULL DEFAULT '' | |
| `email_verified` | bool NOT NULL DEFAULT false | Passe à `true` après premier magic link consommé |
| `status` | text NOT NULL CHECK | `active` / `banned` / `deleted` |
| `created_at`, `updated_at` | timestamptz | |

Index : `players_email_lower_idx` sur `lower(email)`.

Migration : 015.

### Qui tape sur `players`

| Acteur | Endpoint / opération | Lecture | Écriture |
|---|---|---|---|
| Public | `GET /api/players/auth/magic-link/verify` (UPSERT par email, set `email_verified=true`) | x | x |
| Joueur authentifié | `GET /api/players/me` | x | |
| Joueur authentifié | `PATCH /api/players/me` (update first_name/last_name) | | x |
| Joueur authentifié | `DELETE /api/players/me` (hard delete dans transaction) | | x |
| Joueur authentifié | `POST /api/players/me/clubs/:slug/tournaments/:id/register` (lit le profil pour préremplir) | x | |

Handler : [`internal/api/players_auth.go`](../internal/api/players_auth.go), [`internal/api/players_me.go`](../internal/api/players_me.go), [`internal/api/players_registrations.go`](../internal/api/players_registrations.go).

Référence détaillée : [`PLAYERS_DATA_STORAGE.md`](PLAYERS_DATA_STORAGE.md).

---

## player_magic_links

> Tokens de login temporaires. Pas de FK vers `players` (le compte peut ne pas exister au moment où le lien est généré). Keyé par email.

| Colonne | Type | Notes |
|---|---|---|
| `token` | text PK | 32 bytes random base64url |
| `email` | text NOT NULL | Indexé en `lower(email)` |
| `expires_at` | timestamptz NOT NULL | `now() + 15 min` à la création |
| `used_at` | timestamptz NULL | Set au verify, bloque réutilisation |
| `created_at` | timestamptz | |

Index : `player_magic_links_email_idx` sur `(lower(email), created_at DESC)`.

Migration : 016. Cleanup one-shot : 019.

### Qui tape sur `player_magic_links`

| Acteur | Endpoint / opération | Lecture | Écriture |
|---|---|---|---|
| Public | `POST /api/players/auth/magic-link` (INSERT) | | x |
| Public | `GET /api/players/auth/magic-link/verify` (SELECT + UPDATE `used_at`) | x | x |
| Joueur RGPD | `DELETE /api/players/me` (DELETE par email) | | x |
| Background goroutine | `StartMagicLinkCleanup` (DELETE expired/used /6h) | | x |

Handler : [`internal/api/players_auth.go`](../internal/api/players_auth.go), [`internal/api/players_me.go`](../internal/api/players_me.go), [`internal/api/cleanup.go`](../internal/api/cleanup.go).

---

## player_club_memberships

> Adhésion d'un compte joueur à un club (Phase 0.F). Permet de gérer les tournois `members_only` : seuls les joueurs avec une membership `active` sur le club peuvent s'inscrire.

Distinct de `members` (qui représente le staff du club : croupiers, dealers, admins).

| Colonne | Type | Notes |
|---|---|---|
| `id` | uuid PK | |
| `player_id` | uuid NOT NULL FK → players.id ON DELETE CASCADE | |
| `club_id` | uuid NOT NULL FK → clubs.id ON DELETE CASCADE | |
| `status` | text NOT NULL CHECK | `pending` / `active` / `rejected` / `revoked`, default `pending` |
| `requested_at` | timestamptz NOT NULL | |
| `moderated_by_license` | text NULL | License_key de l'admin qui a validé/refusé |
| `moderated_at` | timestamptz NULL | |
| `moderation_reason` | text NULL | Motif optionnel sur reject/revoke |
| `updated_at` | timestamptz NOT NULL | Trigger |

Contraintes : UNIQUE `(player_id, club_id)`.

Index : `player_club_memberships_player_idx`, `player_club_memberships_club_status_idx`.

Migration : 020.

### Qui tape sur `player_club_memberships`

| Acteur | Endpoint / opération | Lecture | Écriture |
|---|---|---|---|
| Joueur authentifié | `POST /api/players/me/clubs/:slug/join` (INSERT pending, ou re-pending si rejected/revoked existant) | x | x |
| Joueur authentifié | `GET /api/players/me/clubs` (list ses memberships) | x | |
| Joueur authentifié | `DELETE /api/players/me/clubs/:slug` (hard delete) | | x |
| Club admin | `GET /api/club/memberships?status=...` (JOIN players pour nom/email) | x | |
| Club admin | `POST /api/club/memberships/:id/approve` (pending → active) | x | x |
| Club admin | `POST /api/club/memberships/:id/reject` (pending → rejected) | x | x |
| Club admin | `POST /api/club/memberships/:id/revoke` (active → revoked) | x | x |
| Joueur authentifié | `POST /api/players/me/clubs/:slug/tournaments/:id/register` (EXISTS check sur status='active') | x | |

Handler : [`internal/api/players_memberships.go`](../internal/api/players_memberships.go), [`internal/api/club_memberships.go`](../internal/api/club_memberships.go), [`internal/api/players_registrations.go`](../internal/api/players_registrations.go).

---

## player_google_tokens

> Tokens OAuth Google Calendar par joueur (Phase 0.E.5). 1-to-1 via PK player_id. Permet d'auto-créer les events tournoi dans l'agenda Google du joueur.

Stockés en clair (réseau privé Swarm, attaquant DB a déjà tout). pgcrypto possible plus tard si défense en profondeur.

| Colonne | Type | Notes |
|---|---|---|
| `player_id` | uuid PK FK → players.id ON DELETE CASCADE | |
| `access_token` | text NOT NULL | Court (~1h Google), refresh auto via service |
| `refresh_token` | text NOT NULL | Long terme, utilisé pour rafraîchir l'access_token |
| `token_type` | text NOT NULL DEFAULT `Bearer` | |
| `expires_at` | timestamptz NOT NULL | Auto-refresh quand < 1 min de marge |
| `scope` | text NOT NULL | Scopes accordés (en pratique `calendar.events`) |
| `connected_at` | timestamptz NOT NULL | Date du premier consent |
| `updated_at` | timestamptz NOT NULL | Trigger |

Migration : 022.

### Qui tape sur `player_google_tokens`

| Acteur | Endpoint / opération | Lecture | Écriture |
|---|---|---|---|
| Joueur authentifié | `GET /api/players/me/google-calendar/start` → callback (UPSERT) | | x |
| Joueur authentifié | `GET /api/players/me/google-calendar` (status SELECT) | x | |
| Joueur authentifié | `DELETE /api/players/me/google-calendar` (revoke + DELETE) | | x |
| Background calendarSync | À chaque sync : SELECT pour récupérer access_token. UPDATE après refresh si nouveau access_token. | x | x |

Handler : [`internal/api/players_calendar.go`](../internal/api/players_calendar.go), [`internal/api/calendar_sync.go`](../internal/api/calendar_sync.go).

---

## Patterns transverses

### Triggers `updated_at`

Une fonction `set_updated_at()` est définie en 001 et réutilisée par toutes les tables qui ont un `updated_at`. Le trigger met à jour la colonne automatiquement à chaque UPDATE. Pas besoin de la setter à la main dans le code Go.

Tables avec trigger : `structures`, `clubs`, `licenses`, `members`, `tournament_registrations`. Les autres (`published_tournaments`, `point_schemes`, `players`) la setent explicitement dans les queries `... SET updated_at = now() ...`.

### Cascades FK

| Relation | Comportement |
|---|---|
| `clubs → *` (toutes les FK club_id) | `ON DELETE CASCADE` : supprimer un club supprime tout son contenu |
| `licenses.member_id → members.id` | `ON DELETE SET NULL` : supprimer un membre détache ses licenses (pas de révocation auto) |
| `players → tournament_registrations.player_id` | `ON DELETE SET NULL` : supprimer un joueur détache ses inscriptions historiques |
| `players → tournament_results.player_id` | `ON DELETE SET NULL` idem |
| `published_tournaments → tournament_registrations.published_tournament_id` | `ON DELETE CASCADE` : supprimer un tournoi supprime ses inscriptions |
| `published_tournaments → tournament_results.published_tournament_id` | `ON DELETE CASCADE` idem pour les résultats |
| `structures → structures_audit.structure_id` | `ON DELETE CASCADE` |

### Index uniques partiels

Deux index uniques partiels gèrent les contraintes métier complexes :

1. **`idx_published_tournaments_local_active`** (migration 011) :
   ```sql
   UNIQUE (club_id, local_tournament_id)
   WHERE local_tournament_id IS NOT NULL AND status <> 'cancelled'
   ```
   Permet le pattern "publier → annuler → republier" sans doublon, tout en autorisant plusieurs rows `cancelled` pour la trace historique.

2. **`tournament_registrations_player_active_uniq`** (migration 017) :
   ```sql
   UNIQUE (published_tournament_id, player_id)
   WHERE player_id IS NOT NULL AND status IN ('pending', 'confirmed')
   ```
   Empêche un joueur connecté de s'inscrire deux fois au même tournoi. Les inscriptions anonymes (`player_id IS NULL`) ne sont pas concernées (anti-doublon via email/cancel_token).

---

## Voir aussi

- [`README.md`](../README.md) section "Endpoints" pour la liste des routes
- [`PLAYERS_DATA_STORAGE.md`](PLAYERS_DATA_STORAGE.md) pour le focus joueurs + RGPD
- Migrations : [`internal/db/migrations/`](../internal/db/migrations/)
- Handlers : [`internal/api/`](../internal/api/)
