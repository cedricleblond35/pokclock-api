# Référence complète des routes HTTP — pokclock-api

Backend Go (Echo). Voir [router.go](../internal/api/router.go) pour le code source. Toutes les routes JSON sauf indication.

**Conventions** :
- **Auth** : `anonyme` / `JWT player` (cookie `pokclock_player`) / `JWT staff` (header `Authorization: Bearer`, club-scoped) / `JWT admin/superadmin` (rôle requis) / `JWT superadmin` (cross-clubs).
- **Codes** : 2xx succès, 4xx erreur métier (`{"error": "code"}`), 5xx erreur serveur (`{"error": "db_read|db_write|gen_failed"}`).
- Toutes les timestamps en RFC3339 UTC.
- Tous les UUIDs en string format `xxxxxxxx-xxxx-xxxx-xxxx-xxxxxxxxxxxx`.

---

## Table des matières

1. [Système](#systeme)
2. [Authentification desktop](#auth-desktop) — `/api/auth/*`
3. [Structures de blinds](#structures) — `/api/structures/*`
4. [Espace club admin](#club-admin) — `/api/club/*`
5. [Public anonyme](#public) — `/public/*`
6. [Authentification joueur](#players-auth) — `/api/players/auth/*`
7. [Espace joueur connecté](#players-me) — `/api/players/me/*`
8. [Superadmin cross-clubs](#admin) — `/api/admin/*`

---

## <a name="systeme"></a>1. Système

### GET /api/health
- **Auth** : anonyme
- **Réponse** : `200 { "status": "ok", "build": "SHA" }`
- **Purpose** : Health check, retourne le build SHA.

### GET /.well-known/jwks.json
- **Auth** : anonyme
- **Réponse** : `200 { "keys": [...] }` (JWKS RS256)
- **Purpose** : Clé publique pour vérifier les JWT émis par l'API.

---

## <a name="auth-desktop"></a>2. `/api/auth/*` — Authentification desktop (license → JWT staff)

Rate-limit : 10 req/min/IP.

### POST /api/auth/issue
- **Auth** : anonyme (vérif côté Worker Cloudflare)
- **Body** : `{ "licenseKey": string, "hardwareId": string, "workerToken": string }`
- **Réponse** : `200 { "token": JWT, "tier": "Solo|Club|Pro", "expiresAt": RFC3339 }`
- **Erreurs** : `400 invalid_body|missing_fields`, `401 license_invalid`, `502 license_verification_unavailable`
- **Purpose** : Échange une license (clé entrée dans WPF) contre un JWT RS256 24h pour appeler les routes club-scoped.

### POST /api/auth/refresh
- **Auth** : anonyme
- **Réponse** : `501 { "error": "refresh_not_yet_implemented", "phase": "0.B" }`
- **Purpose** : Stub (Phase 0.B). Rotation non implémentée.

---

## <a name="structures"></a>3. `/api/structures/*` — Structures de blinds (club-scoped)

Auth JWT staff + filtrage automatique sur `claims.ClubID`.

### GET /api/structures
- **Query** : `?limit=50&offset=0` (max 200)
- **Réponse** : `200 { "structures": [...], "limit", "offset" }`
- **Purpose** : Liste les structures de blinds du club du caller.

### POST /api/structures
- **Body** : `{ "slug": string, "name": string, "kind": "club|casino|home|streamer|other", "settings": object }`
- **Réponse** : `201 { structure }`
- **Erreurs** : `400 missing_fields|invalid_kind`, `409 slug_taken`
- **Purpose** : Crée une structure de blinds.

### GET /api/structures/:slug
- **Réponse** : `200 { structure }`
- **Erreurs** : `404 not_found`

### PATCH /api/structures/:slug
- **Body** : `{ "name"?, "kind"?, "settings"? }`
- **Réponse** : `200 { structure }`
- **Erreurs** : `400 invalid_kind`, `404 not_found`

### DELETE /api/structures/:slug
- **Auth supplémentaire** : `admin` ou `superadmin` du club
- **Réponse** : `204`
- **Purpose** : Suppression hard + audit log.

---

## <a name="club-admin"></a>4. `/api/club/*` — Espace club admin

Auth JWT staff + `requireAnyRole(admin, superadmin)` + scope club. Jamais cross-club.

### Infos & flags

#### GET /api/club/info
- **Réponse** : `200 { id, name, slug, plan, email, status, onlineRegistrationsEnabled, createdAt, updatedAt }`
- **Erreurs** : `410 club_gone`

#### PATCH /api/club/online-registrations
- **Body** : `{ "enabled": bool }`
- **Réponse** : `200 { enabled }`
- **Purpose** : Active/désactive les inscriptions en ligne pour ce club.

### Licenses du club

#### GET /api/club/licenses
- **Query** : `?limit=50&offset=0&status=&role=`
- **Réponse** : `200 { licenses, limit, offset }`

#### POST /api/club/licenses
- **Body** : `{ "role": "member|admin", "expiresAt"?: RFC3339, "memberId"?: uuid }`
- **Réponse** : `201 { license, licenseKey, workerToken, syncStatus }`
- **Erreurs** : `400 member_not_found`, `403 cannot_create_superadmin`, `409 license_key_collision`
- **Purpose** : Génère une license + push vers Worker D1.

#### POST /api/club/licenses/:id/revoke
- **Réponse** : `200 { license, syncStatus }`
- **Erreurs** : `404 not_found`

#### POST /api/club/licenses/:id/assign-member
- **Body** : `{ "memberId"?: uuid }` (null = désassigne)
- **Réponse** : `204`
- **Purpose** : Lie une license à un membre du club.

### Membres du club (non-joueurs)

#### GET /api/club/members
- **Query** : `?limit=100&offset=0`
- **Réponse** : `200 { members, limit, offset }`

#### POST /api/club/members
- **Body** : `{ "firstName": string, "lastName": string, "email"?: string, "notes"?: string }`
- **Réponse** : `201 { member }`

#### PATCH /api/club/members/:id
- **Body** : `{ "firstName"?, "lastName"?, "email"?, "notes"?, "status"?: "active|inactive" }`
- **Réponse** : `200 { member }`

#### DELETE /api/club/members/:id
- **Réponse** : `204` (licenses orphelines, non révoquées)

### Audit du club

#### GET /api/club/audit
- **Query** : `?limit=100&offset=0&actor=&action=created|updated|deleted&structureId=&from=&to=`
- **Réponse** : `200 { entries, limit, offset }`
- **Purpose** : Audit log des structures du club (club-scoped).

### Modération adhésions joueur (Phase 0.F)

#### GET /api/club/memberships
- **Query** : `?limit=100&offset=0&status=pending|active|rejected|revoked`
- **Réponse** : `200 { memberships, limit, offset }`

#### POST /api/club/memberships/:id/approve
- **Réponse** : `200 { status: "active" }`

#### POST /api/club/memberships/:id/reject
- **Body** : `{ "reason"?: string }`
- **Réponse** : `200 { status: "rejected" }`

#### POST /api/club/memberships/:id/revoke
- **Body** : `{ "reason"?: string }`
- **Réponse** : `200 { status: "revoked" }`
- **Purpose** : Exclut un membre actif (active → revoked).

### Roster persistant des membres (Phase 0.J)

Push du roster des Player WPF avec `MembershipType=Member` vers le cloud, indépendamment de toute publication de tournoi. Permet l'auto-link Phase 0.H avant qu'un seul tournoi soit publié (cas du début de saison).

#### PUT /api/club/roster
- **Body** : `{ "members": [{ "firstName": string, "lastName": string, "nickname"?: string, "emailHash"?: string }] }`
- **Réponse** : `200 { count: int }`
- **Erreurs** : `400 invalid_body`, `401 not_authenticated`
- **Purpose** : Idempotent. Remplace intégralement la colonne `clubs.member_roster` (JSONB). Les entrées sans `emailHash` sont conservées (membres sans email, comptés mais non auto-linkables). L'auto-link `autoLinkPlayerToClubs` scanne ce roster en UNION avec `published_tournaments.local_players`.

### Barème de points (Phase 0.D-γ.1)

#### GET /api/club/point-scheme
- **Réponse** : `200 { clubId, name, type, params, updatedAt }`
- **Erreurs** : `404 not_configured`

#### PUT /api/club/point-scheme
- **Body** : `{ "name": string, "type": "linear|fixed|proportional|custom", "params": object }`
- **Réponse** : `200 { scheme }`
- **Erreurs** : `400 invalid_type|invalid_params`

#### DELETE /api/club/point-scheme
- **Réponse** : `204`

### Saisons / championnats (Phase 0.G)

#### GET /api/club/seasons
- **Réponse** : `200 { seasons }` (avec `tournamentsCount`)

#### POST /api/club/seasons
- **Body** : `{ "slug": string, "name": string, "description"?: string, "startsAt": RFC3339, "endsAt": RFC3339 }`
- **Réponse** : `201 { season }`
- **Erreurs** : `400 invalid_starts_at|ends_before_starts`, `409 slug_already_used`

#### PATCH /api/club/seasons/:id
- **Body** : `{ "name"?, "description"?, "startsAt"?, "endsAt"?, "status"?: "upcoming|active|finished|archived" }`
- **Réponse** : `200 { season }`

#### DELETE /api/club/seasons/:id
- **Réponse** : `204` (tournois associés gardent `season_id=NULL`)

### Tournois publiés (Phase 0.D-α)

#### GET /api/club/tournaments
- **Query** : `?limit=100&offset=0&status=`
- **Réponse** : `200 { tournaments, limit, offset }`

#### POST /api/club/tournaments
- **Body** : `{ "name": string, "description"?, "startAt": RFC3339, "buyInAmount": string, "buyInFees": string, "startingStack": int, "maxPlayers": int, "format": string, "lateRegEnabled": bool, "lateRegUntilLevel"?: int, "localTournamentId"?: int, "blindsSummary"?: object, "showPlayers"?: bool, "playersDisplayMode"?: "hidden|full_name|first_initial_last|pseudo", "localPlayers"?: [{firstName,lastName,nickname?,emailHash?}], "prizeStructure"?: object, "audience"?: "public|members_only", "seasonId"?: uuid }`
- **Réponse** : `201 { tournament }` (insert) ou `200 { tournament }` (UPSERT)
- **Erreurs** : `400 invalid_display_mode|season_not_in_club`, `403 online_registrations_disabled`
- **Purpose** : UPSERT par `(club_id, local_tournament_id)`. Endpoint utilisé par WPF Publish.

#### POST /api/club/tournaments/:id/close
- **Réponse** : `204`
- **Erreurs** : `404 not_found_or_not_open`

#### POST /api/club/tournaments/:id/cancel
- **Réponse** : `204`

### Résultats finaux (Phase 0.E.4)

#### POST /api/club/tournaments/:id/results
- **Body** : `{ "results": [{ "position": int, "firstName": string, "lastName": string, "playerId"?: uuid, "points"?: int, "prizeAmount"?: string }], "applyPointScheme": bool, "closeTournament": bool }`
- **Réponse** : `200 { "status": "published", "count": int, "applied": bool }`
- **Erreurs** : `400 empty_results|invalid_position|custom_scheme_requires_explicit_points`, `404 tournament_not_found`
- **Purpose** : DELETE + INSERT par position. Calcule les points via point_scheme si `applyPointScheme=true`.

### Modération inscriptions en ligne

#### GET /api/club/tournaments/:tid/registrations
- **Query** : `?limit=200&offset=0&status=`
- **Réponse** : `200 { registrations, limit, offset }`

#### POST /api/club/registrations/:id/confirm
- **Réponse** : `200 { status }`
- **Purpose** : pending → confirmed + email + sync Calendar joueur.

#### POST /api/club/registrations/:id/reject
- **Body** : `{ "reason"?: string }`
- **Réponse** : `200 { status }`

---

## <a name="public"></a>5. `/public/*` — Endpoints anonymes

Aucune auth. Rate-limit conseillé en amont (Cloudflare/Traefik). Filtre `clubs.online_registrations_enabled = true`.

### GET /public/clubs/:slug/tournaments
- **Réponse** : `200 { tournaments: [...], club: {id, name, slug} }`
- **Erreurs** : `404 club_not_found` (club inexistant OU opt-out)
- **Purpose** : Liste des tournois `open|closed` du club.

### GET /public/clubs/:slug/tournaments/:id
- **Réponse** : `200 { ...tournament, players: [], prizeStructure?, pointScheme? }`
- **Erreurs** : `404 club_not_found|tournament_not_found`
- **Purpose** : Détail tournoi. La liste `players` est filtrée selon `show_players` + `players_display_mode`.

### POST /public/clubs/:slug/tournaments/:id/register
- **Body** : `{ "firstName": string, "lastName": string, "email": string, "phone"?: string }`
- **Réponse** : `201 { id, status: "pending", cancelToken, registeredAt, tournamentId, message }`
- **Erreurs** : `400 missing_fields|invalid_email`, `404 club_not_found|tournament_not_found`, `409 registrations_closed|tournament_started|tournament_full|already_registered`, `403 members_only`
- **Purpose** : Inscription anonyme. Email Resend de confirmation avec `cancelToken`.

### GET /public/registrations/:token/cancel
- **Réponse** : `200 { status: "cancelled" }`
- **Erreurs** : `404 not_found_or_already_cancelled` (indistinguable, anti-bruteforce)
- **Purpose** : Magic link d'annulation (envoyé dans l'email de confirmation).

### GET /public/clubs/:slug/tournaments/:id/results
- **Réponse** : `200 { results: [{ position, firstName, lastName, points, prizeAmount? }] }`

### GET /public/clubs/:slug/leaderboard
- **Query** : `?limit=50` (max 200)
- **Réponse** : `200 { club, entries: [{ rank, firstName, lastName, totalPoints, tournamentsPlayed, wins }] }`
- **Purpose** : Leaderboard global agrégé sur tous les tournois `published`. Groupe par `(lower(firstName), lower(lastName))`.

### GET /public/clubs/:slug/seasons
- **Réponse** : `200 { club, seasons: [...] }`

### GET /public/clubs/:slug/seasons/:season_slug
- **Query** : `?limit=100` (max 500)
- **Réponse** : `200 { season, entries: [...] }`
- **Purpose** : Leaderboard scopé à une saison (même agrégation, filtre `season_id`).

---

## <a name="players-auth"></a>6. `/api/players/auth/*` — Authentification joueur

Rate-limit : 10 req/min/IP.

### POST /api/players/auth/magic-link
- **Body** : `{ "email": string }`
- **Réponse** : `202 { status: "sent" }` (TOUJOURS 202, anti-énumération)
- **Erreurs** : `400 missing_email|invalid_email`
- **Purpose** : Envoie un email Resend avec un lien magique `<API>/api/players/auth/magic-link/verify?token=<32 bytes>`. Token valide 15 min, single-use.

### GET /api/players/auth/magic-link/verify
- **Query** : `?token=<base64url>`
- **Réponse** : `302 Redirect` vers `<publicSiteURL>/joueurs/dashboard` + `Set-Cookie pokclock_player=<JWT>` (HttpOnly, Secure, SameSite=Lax, Domain=PLAYER_COOKIE_DOMAIN, 30 jours)
- **Erreurs** : redirect avec `?error=invalid_token|expired_token|used_token`
- **Purpose** : Consomme le token, upsert le `player`, déclenche Phase 0.H auto-link, émet JWT.

### POST /api/players/auth/logout
- **Auth** : JWT player (cookie)
- **Réponse** : `200 { status: "logged_out" }` + clear cookie
- **Purpose** : Logout. Pas de blacklist côté serveur, JWT expire naturellement.

### GET /api/players/auth/google/start
- **Query** : `?redirect_uri=<URL>`
- **Réponse** : `302 Redirect` vers consent Google OAuth
- **Erreurs** : `503 google_oauth_disabled` (credentials non configurées)
- **Purpose** : Lance le flow OAuth (scope `openid email profile`).

### GET /api/players/auth/google/callback
- **Query** : `?code=&state=`
- **Réponse** : `302 Redirect` vers dashboard + `Set-Cookie pokclock_player`
- **Purpose** : Échange code → id_token, upsert player, déclenche Phase 0.H auto-link, set cookie.

---

## <a name="players-me"></a>7. `/api/players/me/*` — Espace joueur connecté

Auth : `playerAuthMiddleware` (cookie `pokclock_player`). Toutes les routes requièrent un JWT player valide.

### Profil

#### GET /api/players/me
- **Réponse** : `200 { id, email, firstName, lastName, emailVerified, status, createdAt, updatedAt }`
- **Erreurs** : `401 not_authenticated`, `410 player_gone`

#### PATCH /api/players/me
- **Body** : `{ "firstName"?: string, "lastName"?: string }`
- **Réponse** : `200 { profile }`
- **Erreurs** : `400 no_fields`
- **Purpose** : L'email n'est pas modifiable (clé d'identité, lié à l'auto-link 0.H).

#### DELETE /api/players/me
- **Réponse** : `200 { status: "deleted" }` + clear cookie
- **Purpose** : Droit à l'effacement RGPD. Anonymise `tournament_registrations` + `tournament_results` (set first_name="—" last_name="—" player_id=NULL), supprime player + magic_links.

### Inscriptions

#### POST /api/players/me/clubs/:slug/tournaments/:id/register
- **Body** : `{ "firstName"?: string, "lastName"?: string, "phone"?: string }` (optionnels, défaut = profil)
- **Réponse** : `201 { id, status: "pending", registeredAt, ... }`
- **Erreurs** : `400 missing_name`, `404 club_not_found|tournament_not_found`, `409 registrations_closed|tournament_started|tournament_full|already_registered`, `403 members_only`, `410 player_gone`
- **Purpose** : Inscription avec compte. Renseigne `player_id` sur la registration (vs flow anonyme).

#### DELETE /api/players/me/registrations/:id
- **Réponse** : `200 { status: "cancelled" }`
- **Erreurs** : `404 not_found|not_owner`

#### GET /api/players/me/registrations
- **Réponse** : `200 { registrations: [{ id, tournamentId, tournamentName, startAt, buyInAmount, clubName, clubSlug, status, registeredAt }] }`

### Adhésions clubs (Phase 0.F)

#### GET /api/players/me/clubs
- **Réponse** : `200 { memberships: [{ id, clubId, clubName, clubSlug, status, requestedAt, moderatedAt? }] }`

#### POST /api/players/me/clubs/:slug/join
- **Réponse** : `201 { status: "pending" }` (nouvelle demande) ou `200 { status: "pending" }` (re-demande après rejected/revoked)
- **Erreurs** : `404 club_not_found`, `409 already_pending|already_member`

#### DELETE /api/players/me/clubs/:slug
- **Réponse** : `200 { status: "left" }`
- **Erreurs** : `404 club_not_found|not_a_member`
- **Purpose** : Hard delete de la membership. Pour exclusion utiliser `POST /api/club/memberships/:id/revoke` côté admin.

### Google Calendar (Phase 0.E.5)

#### GET /api/players/me/google-calendar
- **Réponse** : `200 { available: bool, connected: bool, connectedAt?: RFC3339 }`
- **Purpose** : `available=false` si credentials OAuth non configurées côté serveur.

#### GET /api/players/me/google-calendar/start
- **Réponse** : `302 Redirect` vers Google consent (scope `calendar.events`)

#### GET /api/players/me/google-calendar/callback
- **Query** : `?code=&state=`
- **Réponse** : `302 Redirect` vers dashboard avec `?calendar_connected=1` ou `?calendar_error=<code>`
- **Purpose** : Sauvegarde refresh_token chiffré dans `player_google_tokens`.

#### DELETE /api/players/me/google-calendar
- **Réponse** : `200 { status: "disconnected" }`
- **Purpose** : Révoque le token côté Google + supprime la row locale.

### Dashboard agrégé (Phase 0.I)

#### GET /api/players/me/dashboard
- **Réponse** : `200 { widgets: [ClubWidget...] }`

`ClubWidget` :
```json
{
  "club": { "id": uuid, "slug": string, "name": string },
  "myRank": {
    "position": int,
    "totalEntries": int,
    "totalPoints": int,
    "tournamentsPlayed": int
  } | null,
  "activeSeason": {
    "id": uuid, "slug": string, "name": string,
    "startsAt": RFC3339, "endsAt": RFC3339,
    "myRank": <même structure que ci-dessus> | null
  } | null,
  "upcomingTournaments": [{
    "id": uuid, "name": string, "startAt": RFC3339,
    "audience": "public|members_only",
    "spotsLeft": int | null,
    "myRegistrationStatus": "pending|confirmed|rejected|cancelled" | null
  }]
}
```

- **Tri** : par dernière activité (max `start_at` des tournois du club, fallback `requested_at` de la membership).
- **Scope** : uniquement les memberships `status='active'`.
- **Calcul du rang** : agrégation `SUM(points)` groupée par `(lower(firstName), lower(lastName))` comme le leaderboard public. Le joueur est matché par `player_id` OU par nom (insensible casse) pour englober les résultats publiés avant qu'il ait son compte cloud.
- **Saison active** : `seasons.status='active'` la plus récente. `LIMIT 1`.
- **Upcoming** : `published_tournaments.status='open' AND start_at > now()` triés `start_at ASC`, `LIMIT 3`.

**Erreurs** : `401 not_authenticated`, `410 player_gone`.

---

## <a name="admin"></a>8. `/api/admin/*` — Superadmin cross-clubs

Auth JWT + `requireRole(superadmin)`. Réservé à Cédric.

### Clubs

#### GET /api/admin/clubs
- **Query** : `?limit=50&offset=0&status=&q=<search>`
- **Réponse** : `200 { clubs, limit, offset }`

#### POST /api/admin/clubs
- **Body** : `{ "name": string, "plan": "solo|club|pro", "email"?, "notes"? }`
- **Réponse** : `201 { club }`

#### GET /api/admin/clubs/:id
- **Réponse** : `200 { club }`

#### PATCH /api/admin/clubs/:id
- **Body** : `{ "name"?, "plan"?, "email"?, "notes"? }`
- **Réponse** : `200 { club }`

#### POST /api/admin/clubs/:id/suspend
- **Réponse** : `200 { status: "suspended" }`

#### POST /api/admin/clubs/:id/unsuspend
- **Réponse** : `200 { status: "active" }`

#### DELETE /api/admin/clubs/:id
- **Réponse** : `204`
- **Purpose** : Soft delete (`status='deleted'`).

#### GET /api/admin/clubs/:id/members
- **Réponse** : `200 { members }`
- **Purpose** : Visibilité super-admin sur les membres d'un club tiers.

### Licenses

#### GET /api/admin/licenses
- **Query** : `?limit=50&offset=0&clubId=&status=&role=`
- **Réponse** : `200 { licenses, limit, offset }`

#### POST /api/admin/licenses
- **Body** : `{ "clubId": uuid, "role": "member|admin|superadmin", "expiresAt"? }`
- **Réponse** : `201 { license, licenseKey, workerToken, syncStatus }`
- **Purpose** : Superadmin PEUT créer un role=superadmin (vs `/api/club/licenses` qui refuse).

#### POST /api/admin/licenses/:id/revoke
- **Réponse** : `200 { license, syncStatus }`

### Audit cross-clubs

#### GET /api/admin/audit
- **Query** : `?limit=100&offset=0&clubId=&actor=&action=&structureId=&from=&to=`
- **Réponse** : `200 { entries, limit, offset }`

---

## Récap rapide par stack consommateur

### WPF desktop (StackPilot)
- `POST /api/auth/issue` (au démarrage)
- `GET|POST|PATCH|DELETE /api/structures/*`
- `POST /api/club/tournaments` (publish)
- `POST /api/club/tournaments/:id/results` (publish results)
- `POST /api/club/tournaments/:id/close`
- `GET /api/club/audit`, `GET /api/club/members`, etc. selon UI

### Site Next.js (pokclock-react)
- **SSR anonyme** : `GET /public/clubs/:slug/*`
- **Auth joueur** : `POST /api/players/auth/magic-link`, `GET /api/players/auth/magic-link/verify`, `GET /api/players/auth/google/start|callback`
- **Espace joueur** : `GET|PATCH|DELETE /api/players/me`, `GET /api/players/me/registrations|clubs|dashboard|google-calendar`, `POST /api/players/me/clubs/:slug/join`, etc.

### Worker Cloudflare (license verification)
- N'appelle pas l'API. C'est l'inverse : `POST /api/auth/issue` appelle le Worker pour vérifier `licenseKey + hardwareId + workerToken`.
