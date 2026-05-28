# Stockage des données joueurs

> Phase 0.E + RGPD. Référence technique sur où vivent les données des comptes joueurs PokClock, et comment elles sont protégées, sauvegardées et supprimables.

À jour au **2026-05-27**.

---

## 1. Vue d'ensemble

| Catégorie | Localisation | Hébergeur | Région |
|---|---|---|---|
| Tables Postgres (profils, magic links, inscriptions, résultats) | Service Swarm `pokclock-api_postgres` | VPS OVH `vps-eb94174b` (54.38.243.50) | France |
| JWT session joueur | Cookie HttpOnly côté navigateur | Aucun (stateless) | N/A |
| Backups DB chiffrés | Bucket `pokclock-backups` | Cloudflare R2 | WEUR (Western Europe) |
| Emails transactionnels | Resend HTTP API | Resend | Ireland eu-west-1 |
| Logs applicatifs | stdout container (Docker logs rotation) | VPS OVH | France |

Toutes les données restent dans l'UE. Aucun transfert hors-UE.

---

## 2. Tables Postgres concernées

### 2.1 `players`

Le compte joueur principal.

| Colonne | Type | Notes |
|---|---|---|
| `id` | uuid PK | Identifiant interne, utilisé dans tous les FK |
| `email` | text UNIQUE | En clair (nécessaire pour les magic links). Indexé en `lower(email)`. |
| `google_id` | text UNIQUE NULL | `sub` Google OAuth (Phase 0.E.1.e, non encore actif) |
| `first_name`, `last_name` | text | Vide par défaut, alimenté quand le joueur édite son profil |
| `email_verified` | bool | Passe à `true` après consommation du premier magic link |
| `status` | text | `active` / `banned` / `deleted` (soft-delete réservé aux bans admin) |
| `created_at`, `updated_at` | timestamptz | Audit basique |

Migration : [`015_players.sql`](../internal/db/migrations/015_players.sql).

### 2.2 `player_magic_links`

Tokens de connexion passwordless.

| Colonne | Type | Notes |
|---|---|---|
| `token` | text PK | 32 bytes random base64url (256 bits d'entropie) |
| `email` | text | Cible du lien, pas de FK vers `players` (le compte peut ne pas exister encore) |
| `expires_at` | timestamptz | `now() + 15 min` à la création |
| `used_at` | timestamptz NULL | Marqué au verify, bloque la réutilisation |
| `created_at` | timestamptz | |

Migration : [`016_player_magic_links.sql`](../internal/db/migrations/016_player_magic_links.sql).

**Cleanup** : la table est purgée toutes les 6h par la goroutine `StartMagicLinkCleanup` (cf. [`cleanup.go`](../internal/api/cleanup.go)) avec `DELETE WHERE used_at IS NOT NULL OR expires_at < now()`. Migration [`019_player_magic_links_cleanup.sql`](../internal/db/migrations/019_player_magic_links_cleanup.sql) fait un nettoyage one-shot au déploiement.

### 2.3 `tournament_registrations.player_id`

FK ajouté en Phase 0.E.2 pour lier une inscription à un compte joueur authentifié. Nullable (compat avec les inscriptions anonymes Phase 0.D-α). `ON DELETE SET NULL` : si le joueur est supprimé, le `player_id` est nullifié mais la registration historique reste.

Migration : [`017_tournament_registrations_player_id.sql`](../internal/db/migrations/017_tournament_registrations_player_id.sql).

### 2.4 `tournament_results.player_id`

FK ajouté en Phase 0.E.4 pour lier un classement final à un compte. Nullable, `ON DELETE SET NULL`. Permet d'agréger le leaderboard cross-tournois par `player_id` quand disponible, fallback nom sinon.

Migration : [`018_tournament_results.sql`](../internal/db/migrations/018_tournament_results.sql).

### 2.5 `player_club_memberships` (Phase 0.F)

Adhésion d'un joueur à un club, prérequis pour s'inscrire à un tournoi `members_only`.

| Colonne | Type | Notes |
|---|---|---|
| `id` | uuid PK | |
| `player_id` | uuid FK CASCADE | |
| `club_id` | uuid FK CASCADE | |
| `status` | text | `pending` / `active` / `rejected` / `revoked` |
| `requested_at` | timestamptz | |
| `moderated_by_license`, `moderated_at`, `moderation_reason` | nullable | |

Cascade : si le joueur supprime son compte, ses memberships disparaissent (CASCADE) — pas d'anonymisation nécessaire, c'est juste une relation.

Migration : [`020_player_club_memberships.sql`](../internal/db/migrations/020_player_club_memberships.sql).

### 2.6 `player_google_tokens` (Phase 0.E.5)

Tokens OAuth pour synchroniser les inscriptions tournoi dans l'agenda Google du joueur.

| Colonne | Type | Notes |
|---|---|---|
| `player_id` | uuid PK FK CASCADE | 1-to-1 avec player |
| `access_token` | text | Court terme (~1h), stocké en clair |
| `refresh_token` | text | Long terme, stocké en clair |
| `token_type` | text DEFAULT 'Bearer' | |
| `expires_at` | timestamptz | Auto-refresh quand expiré |
| `scope` | text | `calendar.events` |
| `connected_at`, `updated_at` | timestamptz | |

**Stockage en clair** : la DB Postgres est sur réseau privé Swarm, l'attaquant qui y accède a déjà accès à tout le reste. Si on veut une couche supplémentaire plus tard (défense en profondeur), pgcrypto + secret KMS.

**Suppression compte** : CASCADE supprime la row, **mais** le token reste valide côté Google jusqu'à expiration (refresh) ou révocation explicite. Pour révoquer côté Google aussi : voir endpoint `DELETE /api/players/me/google-calendar` qui révoque avant de supprimer la row.

Migration : [`022_player_google_tokens.sql`](../internal/db/migrations/022_player_google_tokens.sql).

### 2.7 `tournament_registrations.google_event_id` (Phase 0.E.5)

Colonne nullable ajoutée pour lier une inscription à l'event Google Calendar correspondant. Permet update (au confirm) et delete (au cancel/reject).

Migration : [`023_tournament_registrations_google_event_id.sql`](../internal/db/migrations/023_tournament_registrations_google_event_id.sql).

---

## 3. Session côté navigateur (cookie JWT)

Pas de table de sessions côté serveur. La session est un JWT signé RS256 transporté dans un cookie.

| Attribut | Valeur |
|---|---|
| Nom | `pokclock_player` |
| Contenu | JWT RS256 (claims `pid`, `email`, `aud=pokclock-player`, `exp`, `sub`, `iat`) |
| `HttpOnly` | `true` (JS du navigateur ne peut pas le lire) |
| `Secure` | `true` (HTTPS uniquement) |
| `SameSite` | `None` (autorise les requêtes cross-subdomain `pokclock.com` ↔ `api.pokclock.com`) |
| `Domain` | `.pokclock.com` en prod (env `PLAYER_COOKIE_DOMAIN`), vide en dev |
| `Path` | `/` |
| TTL | 30 jours |

Conséquences :
- **Pas de stockage serveur de la session**, rien à supprimer côté DB quand un user se déconnecte
- **Le logout supprime juste le cookie** (efface `Set-Cookie` avec `Max-Age=-1`)
- **Une session ne peut pas être révoquée à la volée** sans changer la clé de signature globale. Trade-off accepté pour la simplicité ; le TTL de 30 jours limite l'exposition.
- Si un joueur supprime son compte, le JWT reste cryptographiquement valide jusqu'à expiration, mais tous les endpoints `/api/players/me/*` retournent `410 Gone` ou `404` parce que `players.id` n'existe plus.

Signature : la même paire RSA que les JWT desktop (cf. `auth.Signer`), avec un `aud` différent (`pokclock-player` vs `pokclock-desktop`). Le middleware `playerAuthMiddleware` rejette tout token qui n'a pas l'audience `pokclock-player`.

---

## 4. Backups

```
┌──────────────────────────────┐         daily 03:00 UTC          ┌─────────────────────────┐
│ pokclock-api_postgres        │  ─── pg_dump | restic backup ──> │ Cloudflare R2           │
│ Service Swarm sur VPS OVH    │                                  │ pokclock-backups (WEUR) │
│ Volume Docker persistant     │                                  │ Chiffrement AES-256     │
└──────────────────────────────┘                                  └─────────────────────────┘
```

- **Outil** : `restic` dans un container dédié `pokclock-api_backup` qui tourne dans le même stack Swarm
- **Fréquence** : quotidienne (03:00 UTC)
- **Rétention** : 7 jours / 4 semaines / 6 mois
- **Chiffrement** : AES-256 via restic, clé stockée comme secret Swarm (`restic_password`), JAMAIS dans le repo Git
- **Vérification** : `restic check` exécuté chaque semaine

Les backups contiennent les tables `players`, `player_magic_links`, `tournament_registrations`, `tournament_results`, etc. **Implication RGPD** : si un joueur supprime son compte, les snapshots existants conservent ses données pendant la fenêtre de rétention (max 6 mois). Au-delà, les anciens snapshots expirent automatiquement.

---

## 5. Emails (Resend)

Les magic links et confirmations sont envoyés via [Resend](https://resend.com) (provider transactionnel, Ireland eu-west-1).

| Email | Déclencheur | Contenu joueur transmis à Resend |
|---|---|---|
| Magic link login | `POST /api/players/auth/magic-link` | Email + lien avec token |
| Confirmation compte supprimé | `DELETE /api/players/me` | Email + first_name (optionnel) |
| Inscription tournoi confirmée | Admin valide depuis WPF | Email + nom + détail tournoi + cancel_token |
| Inscription tournoi refusée | Admin reject depuis WPF | Email + nom + motif optionnel |

Resend conserve les emails envoyés ~30 jours dans son log pour debug. Domaine d'envoi : `noreply@pokclock.com` (DKIM + SPF + DMARC verified).

---

## 6. Ce qui n'est PAS stocké

- **Pas de mot de passe** (auth passwordless via magic link)
- **Pas d'adresse IP côté `players`** (les logs Echo loguent l'IP mais sans liaison user, rotation Docker logs)
- **Pas de User-Agent stocké**
- **Pas de credit card / paiement** (pas encore implémenté ; quand Stripe sera branché, les tokens Stripe seront stockés, pas les numéros)
- **Pas de tracking ni cookies analytics côté site public** sur les pages `/joueurs/*`
- **Pas de géolocalisation**

---

## 7. Droit à l'effacement (RGPD Art. 17)

Endpoint : `DELETE /api/players/me` (authentifié par cookie).

### Transaction côté backend

```sql
BEGIN;

-- 1. Anonymise les inscriptions historiques (préserve les capacités tournois)
UPDATE tournament_registrations
   SET first_name = 'Anonyme',
       last_name  = '',
       email      = '',
       phone      = NULL,
       player_id  = NULL
 WHERE player_id = :player_id;

-- 2. Anonymise les résultats historiques (préserve les leaderboards)
UPDATE tournament_results
   SET first_name = 'Anonyme',
       last_name  = '',
       player_id  = NULL
 WHERE player_id = :player_id;

-- 3. Supprime tous les magic links pendants liés à l'email
DELETE FROM player_magic_links WHERE lower(email) = lower(:email);

-- 4. Supprime le compte
DELETE FROM players WHERE id = :player_id;

COMMIT;
```

### Effets

- **Compte** : supprimé définitivement
- **Inscriptions passées et futures** : anonymisées. Les positions dans les classements historiques restent (sans nom identifiable), les compteurs de joueurs sur les tournois passés restent corrects.
- **Résultats** : anonymisés idem. Le leaderboard club préserve la structure (rangs, points) sans le nom.
- **Memberships clubs** : CASCADE delete via FK (table `player_club_memberships`)
- **Tokens Google Calendar** : CASCADE delete via FK (table `player_google_tokens`). **Limitation** : les tokens restent valides côté Google jusqu'à leur expiration naturelle ou révocation explicite (le DELETE me ne les révoque pas via l'API Google pour rester rapide en transaction). Pour révoquer côté Google, l'utilisateur peut le faire via [security.google.com](https://myaccount.google.com/permissions).
- **Events Google Calendar existants** : pas supprimés (le compte est déjà détruit, on ne peut plus appeler l'API Calendar avec son token). Les events tournoi resteront dans son agenda. L'utilisateur peut les supprimer manuellement.
- **Cookie de session** : cleared via `Set-Cookie` avec `Max-Age=-1` dans la réponse
- **JWT existant** : cryptographiquement valide mais inutilisable (player_id introuvable en DB)
- **Email de confirmation** : envoyé best-effort via Resend pour traçabilité

### Limitations connues

- **Backups** : un snapshot R2 de la veille contient encore les données du joueur. Expirent automatiquement selon la politique de rétention (max 6 mois). Pas de purge ciblée des backups (techniquement possible avec restic mais pas implémenté).
- **Logs Resend** : Resend conserve ~30 jours les emails envoyés. Le joueur peut faire une demande directement à Resend si besoin.
- **Logs Docker** : les logs applicatifs peuvent contenir des emails dans les messages de log (ex: `slog.Info("send register confirmation failed", "email", in.Email)`). Rotation Docker par défaut limite la rétention à quelques jours.

---

## 8. Schéma global

```
┌────────────────────────────────────────────────────────────────┐
│ Navigateur joueur (pokclock.com)                               │
│   - Cookie pokclock_player (JWT, HttpOnly, 30j)                │
└──────────────────────┬─────────────────────────────────────────┘
                       │ HTTPS avec credentials
                       ▼
┌────────────────────────────────────────────────────────────────┐
│ VPS OVH vps-eb94174b (France)                                  │
│                                                                │
│  ┌─────────────────────────────┐                               │
│  │ pokclock-api_api (Echo Go)  │                               │
│  │   - playerAuthMiddleware    │                               │
│  │   - handlers /api/players/* │                               │
│  │   - magicLinkCleanup /6h    │                               │
│  └──────────────┬──────────────┘                               │
│                 │                                              │
│  ┌──────────────▼──────────────┐    ┌──────────────────────┐   │
│  │ pokclock-api_postgres       │    │ pokclock-api_backup  │   │
│  │   - players                 │    │   - restic daily     │   │
│  │   - player_magic_links      │ ─> │   - chiffré AES-256  │   │
│  │   - tournament_registrations│    └──────────┬───────────┘   │
│  │   - tournament_results      │               │               │
│  │   - clubs, licenses, ...    │               │               │
│  └─────────────────────────────┘               │               │
└────────────────────────────────────────────────┼───────────────┘
                                                 │
                                                 ▼
                                  ┌──────────────────────────────┐
                                  │ Cloudflare R2 (WEUR)         │
                                  │   bucket pokclock-backups    │
                                  │   rétention 7d / 4w / 6m     │
                                  └──────────────────────────────┘
```

---

## 9. Voir aussi

- [`README.md`](../README.md) section "Endpoints" pour la liste complète des routes joueurs
- [`PHASE-0B-OPS-ROADMAP.md`](../../PHASE-0B-OPS-ROADMAP.md) section "Bilan 2026-05-27" pour le contexte des phases
- Migrations Phase 0.E : 015 à 019 dans [`internal/db/migrations/`](../internal/db/migrations/)
- Handlers : [`players_auth.go`](../internal/api/players_auth.go), [`players_me.go`](../internal/api/players_me.go), [`players_registrations.go`](../internal/api/players_registrations.go), [`cleanup.go`](../internal/api/cleanup.go)
- [Politique de confidentialité publique](https://pokclock.com/fr/legal) (à rédiger côté `pokclock-react` à partir de ce document)
