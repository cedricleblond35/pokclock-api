# Phase 0.B → ops.pokclock.com — Dashboard admin unifié

> **Objectif** : un seul URL (`ops.pokclock.com`), un seul login (Cloudflare Access),
> tout intégré : observabilité tech + gestion des clubs + licenses + audit.

**Plan validé** : 2026-05-24
**Estimation totale** : ~5 jours de dev, livrables incrémentaux.

Légende : `[ ]` todo · `[x]` fait · `[⏳]` en cours · `[👤]` action humaine

---

## Étape 1 — Fondations (≈ 1 jour)

Objectif : observabilité tech complète, accessible en un login.

- [ ] Wildcard cert `*.pokclock.com` via OVH DNS-01
  - Modifier labels Traefik de `pokclock-api` : `tls.domains[0].main=*.pokclock.com` + `sans=pokclock.com`
  - Redéployer le stack, vérifier émission du wildcard via Traefik logs
- [👤] Cloudflare Access setup
  - Cloudflare Zero Trust → Access → Applications → "Add an application" type Self-hosted
  - Hostnames : `ops.pokclock.com`, `grafana.pokclock.com`
  - Policy : Include → Emails → `drick35@gmail.com`
  - Identity provider : Google OAuth (built-in)
- [ ] Stack monitoring dans `pokclock-infra/monitoring/`
  - `docker-compose.swarm.prod.yml` : services `grafana`, `loki`, `promtail`, `prometheus`
  - Réseau overlay interne `nw-ops` (Grafana exposé via Traefik sur `grafana.pokclock.com`, le reste interne)
  - Volumes persistants pour Grafana et Prometheus
- [ ] Provisioning Grafana
  - Datasources : Postgres (sur `pokclock-api_postgres`), Loki, Prometheus
  - Config `[auth.proxy]` trust header `Cf-Access-Authenticated-User-Email`
  - Désactiver le login Grafana built-in
- [ ] Promtail : tail Docker logs de tous les containers Swarm vers Loki
- [ ] Prometheus : scrape Traefik metrics + cAdvisor (optionnel) + futures `/metrics` API
- [ ] DNS : record A `grafana.pokclock.com → 54.38.243.50` (ou pas si wildcard CNAME)
- [ ] Test E2E : ouvrir `grafana.pokclock.com` → login Google → Grafana sans password

**Valeur livrée** : observabilité tech opérationnelle.

---

## Étape 2 — Skeleton de l'admin app (≈ 1 jour)

Objectif : `ops.pokclock.com` qui s'ouvre, prête à recevoir le métier.

- [ ] Créer le repo `pokclock-ops` (Next.js 15, App Router, TypeScript)
  - Sidebar : Dashboard / Clubs / Licenses / Audit / Observabilité / Infrastructure / Backups / Facturation
  - Topbar : nom user (depuis header CF Access) + bouton logout
  - Theming cohérent avec pokclock-react (mêmes couleurs/fonts)
- [ ] Page `/` Dashboard (v0)
  - KPIs hardcodés au début (MRR placeholder, Clubs count placeholder)
  - 2-3 iframes Grafana pour les panels les plus parlants (latency p99, errors rate)
  - Block "Recent activity" placeholder (fed par les vrais events plus tard)
- [ ] Dockerfile multi-stage + GHA Build & Deploy similaire à pokclock-api
- [ ] Compose Swarm `docker-compose.swarm.prod.yml` avec labels Traefik pour `ops.pokclock.com`
- [👤] Configurer Cloudflare Access sur le hostname
- [ ] DNS : record A `ops.pokclock.com → 54.38.243.50`
- [ ] Premier deploy via GHA → vérifier en HTTPS derrière Access

**Valeur livrée** : la coquille est là, prête à se remplir.

---

## Étape 3 — Backend admin + page Clubs (≈ 1.5 jour)

Objectif : gérer les clubs depuis l'UI.

### Backend (terminé 2026-05-25, commits `96884be` + `1083c92`)

- [x] Migration goose Postgres
  - [x] `003_clubs.sql` : table `clubs` (id uuid PK, name UK, plan CHECK solo/club/pro, status active/suspended/deleted, email, notes, timestamps + suspended_at)
  - [x] `004_licenses.sql` : table `licenses` (FK clubs CASCADE, license_key UK, worker_token_hash SHA-256, hardware_id nullable, role member/admin/superadmin, status active/revoked/expired, expires_at, last_seen_at, revoked_at)
  - [x] `005_structures_club_id.sql` : `ALTER structures ADD club_id` (nullable rétro-compat Phase 0.A) + index partial
- [x] Enrichir les claims JWT
  - [x] `auth.Role` type avec constantes `RoleMember`, `RoleAdmin`, `RoleSuperadmin`
  - [x] `Claims.Role` + `Claims.ClubID` (rétro-compat : tokens existants restent valides)
  - [x] `Signer.Issue(IssueParams{})` refactoré (struct au lieu de 4 params positionnels)
  - [x] `VerifyResponse` (Worker) expose `role` + `clubId` (optionnels)
  - [x] Fallback : Worker n'expose pas encore → JWT émis en RoleMember
- [x] Middleware RBAC dans `internal/api/middleware.go`
  - [x] `requireRole(role)` — exige exactement ce rôle
  - [x] `requireAnyRole(...)` — OU logique
- [x] Routes API admin (`internal/api/admin_clubs.go`, `admin_licenses.go`, `admin_audit.go`)
  - [x] `GET /api/admin/clubs` — liste paginée + filtres status/q + pagination
  - [x] `POST /api/admin/clubs` — création
  - [x] `GET /api/admin/clubs/:id` — détail
  - [x] `PATCH /api/admin/clubs/:id` — update name/plan/email/notes
  - [x] `POST /api/admin/clubs/:id/suspend` + `unsuspend`
  - [x] `DELETE /api/admin/clubs/:id` — soft delete
  - [x] `GET /api/admin/licenses` — liste + filtres clubId/status/role
  - [x] `POST /api/admin/licenses` — génère license_key POK-XXXX-XXXX-XXXX-XXXX + worker_token 32 bytes hex, stocke uniquement SHA-256 du token
  - [x] `POST /api/admin/licenses/:id/revoke`
  - [x] `GET /api/admin/audit` — recherche cross-clubs avec jointure structures, filtres clubId/actor/action/dates/structureId
- [x] Tests `signer_test.go` adaptés à la nouvelle signature + nouveau test fallback role=member

### Reste à faire pour rendre 100% fonctionnel

- [ ] Worker Cloudflare `/verify` : exposer `role` + `clubId` dans la réponse JSON
- [ ] Worker Cloudflare `/verify` : hasher en SHA-256 hex le `workerToken` reçu et matcher contre `worker_token_hash` en base (pour les licenses générées via `/api/admin/licenses`)
- [ ] Bootstrap : créer le premier JWT `role=superadmin` pour Cédric (en attendant que le Worker l'expose, on peut hardcoder un override côté `/api/auth/issue` ou utiliser un endpoint de bootstrap protégé par un secret env)

### Frontend (en attente Étape 2)

- [ ] Page `pokclock-ops/clubs`
  - Liste paginée avec colonnes : Name / Plan / Members / Last activity / Status
  - Search bar (filtre par nom)
  - Bouton "Créer un club" → modale
  - Click sur ligne → drill-down `/clubs/:id`
- [ ] Page `pokclock-ops/clubs/[id]`
  - Header : nom, plan, status, créé le
  - Onglets : Overview / Members / Licenses / Structures / Audit
  - Actions : Suspendre, Changer de plan, Supprimer

**Valeur livrée backend** : ✅ toutes les routes admin sont prêtes et testées.
**Valeur livrée frontend** : ⏳ dépend de l'Étape 2 (skeleton Next.js).

---

## Étape 4 — Licenses + Audit (≈ 1 jour)

Objectif : support client autonome.

- [ ] Page `pokclock-ops/licenses`
  - Liste paginée : License key (masquée) / Club / Status / Created / Last seen
  - Filtres : status (active/expired/revoked), club, période
  - Actions : Revoke (avec confirm), Extend expiration, Reveal key (one-time view + audit)
  - Bouton "Generate new license" → modale → POST /api/admin/licenses
- [ ] Endpoint `POST /api/admin/licenses/:id/revoke` — set `revoked_at`, propager au Worker pour invalider côté D1
- [ ] Endpoint `POST /api/admin/licenses` — génère key + HMAC token, retourne au superadmin une seule fois
- [ ] Page `pokclock-ops/audit`
  - Recherche full-text dans `structures_audit` (jsonb diff + action + actor)
  - Filtres : club, acteur, période, action (CREATE/UPDATE/DELETE)
  - Détail ligne : modal avec le diff jsonb formaté
- [ ] Endpoint `GET /api/admin/audit?club_id=&actor=&from=&to=&q=`

**Valeur livrée** : tu peux répondre à "qui a changé la structure X et quand"

---

## Étape 5 — Polish (≈ 0.5 jour)

- [ ] Lien sortant dans sidebar : "Traefik dashboard" → `https://traefik.pokclock.com` (à exposer derrière CF Access aussi)
- [ ] Page `pokclock-ops/infrastructure`
  - Status des services Swarm via Docker API (read-only)
  - Cert expiration countdown
  - Snapshots restic count + dernière date
- [ ] KPIs Stripe sur le Dashboard
  - MRR via Stripe API + cache 1h
  - Active subscriptions count
  - Link out vers Stripe dashboard
- [ ] Activity feed sur le Dashboard
  - Stream des derniers events (license activated, structure created, etc.)
  - Source : `structures_audit` + `licenses` créées récemment
- [ ] Dark / light mode (toggle dans topbar)
- [ ] Pages d'erreur custom 404/500

**Valeur livrée** : ops complet et confortable au quotidien.

---

## Récap des sous-domaines finaux

| Sous-domaine | Audience | Protection |
|---|---|---|
| `pokclock.com` | Public | aucune (marketing) |
| `api.pokclock.com` | Apps clients + ops.pokclock.com | JWT RS256 (role-based) |
| `ops.pokclock.com` | Cédric (super-admin) | Cloudflare Access + JWT role=superadmin |
| `grafana.pokclock.com` | Cédric | Cloudflare Access |
| `traefik.pokclock.com` | Cédric (Phase 5) | Cloudflare Access |

---

## Risques identifiés

| Risque | Probabilité | Mitigation |
|---|---|---|
| Cloudflare Access OAuth Google failed setup | basse | doc Cloudflare très claire, fallback identité par PIN possible |
| Wildcard cert OVH DNS-01 trop lent à se propager | basse | resolver déjà testé en Phase 0.A, fonctionne |
| Migration Postgres `club_id` casse les structures existantes | moyenne | écrire la migration en deux étapes (ADD COLUMN nullable, backfill, NOT NULL) |
| Worker /verify pas prêt à retourner `club_id` + `role` | haute | étape 3.1 : modifier le Worker D1 d'abord pour exposer ces champs |
| Stripe API quota / facturation | basse | cache 1h côté ops, lecture seule |

---

## Bilan 2026-05-25 — fin de session

### ✅ Étapes 1 à 3 + 4 (presque) complètes

| Étape | État | Détails |
|---|---|---|
| **1 — Fondations** | ✅ | DNS pokclock.com migré OVH → Cloudflare. Wildcard `*.pokclock.com` émis via Cloudflare DNS-01 (cert valide jusqu'à 2026-08-23). Resolver Traefik `ovh-dns` retiré, seul `cloudflare-dns` actif. Cloudflare Access + Google OAuth + One-time PIN configurés sur `ops.pokclock.com`. |
| **2 — Skeleton Next.js** | ✅ | `pokclock-ops` repo créé, déployé sur Swarm, 7 routes (Dashboard, Clubs, Licenses, Audit, Observability, Infrastructure, Backups). Image GHCR. Cert wildcard couvre le sous-domaine. |
| **3 — Backend RBAC** | ✅ | Migrations Postgres `003_clubs.sql` + `004_licenses.sql` + `005_structures_club_id.sql`. JWT claims étendus avec `Role` + `ClubID`. Middlewares `requireRole` + `requireAnyRole`. 11 endpoints `/api/admin/*` (clubs, licenses, audit). Tests verts. Bootstrap CLI `cmd/issue-jwt/`. |
| **4 — Worker /verify** | 🟡 | Code écrit, type-check OK, **partiellement déployé**. Migration D1 0002 (ajout `role` + `club_id` à `licenses`) en cours d'application (colonne `role` créée, vérification de `club_id` en attente). Code Worker prêt à `wrangler deploy`. |

### ⏳ Reste à reprendre demain

#### 4 — Finaliser Worker `/verify` (~30 min)

1. **Vérifier l'état de la DB D1** sur Cloudflare dashboard → Storage & databases → D1 → `pokclock-licenses` → Console :
   ```sql
   SELECT name FROM pragma_table_info('licenses') WHERE name IN ('role', 'club_id');
   ```
   - Si 2 rows → indexes restants : `CREATE INDEX IF NOT EXISTS idx_licenses_role ON licenses(role); CREATE INDEX IF NOT EXISTS idx_licenses_club_id ON licenses(club_id);`
   - Si 1 row (`role` seul) → `ALTER TABLE licenses ADD COLUMN club_id TEXT;` puis les 2 indexes
2. **Deploy Worker** : `cd c:\Users\cédric\source\repos\StackPilot\backend && npx wrangler deploy`
3. **Promouvoir license user en superadmin** :
   ```bash
   curl -X POST https://pokclock-license.dynamidoxa.workers.dev/admin/promote \
     -H "Authorization: Bearer $ADMIN_TOKEN" \
     -H "Content-Type: application/json" \
     -d '{"licenseKey": "POK-XXXX-XXXX-XXXX-XXXX", "role": "superadmin"}'
   ```
4. **Test E2E** : `curl /api/auth/issue` avec la license → JWT décodé doit montrer `role=superadmin` + `club_id=null` venant directement du Worker (et non plus de l'override env `SUPERADMIN_LICENSE_KEYS`).
5. (Optionnel) **Retirer le bootstrap** `SUPERADMIN_LICENSE_KEYS` du compose pokclock-api une fois validé.

#### 5b — Pages CRUD étoffées (~2-3 jours)

- Page **Clubs** : modale Create / Edit / Delete confirm / boutons Suspend-Unsuspend / drill-down `/clubs/[id]` avec onglets (Overview, Members, Licenses, Structures, Audit)
- Page **Licenses** : modale Generate (display key + token UNE fois) / bouton Revoke / filtre par club
- Page **Audit** : filtres (clubId, actor, action, date range) / modale View diff JSON formaté

#### 5a — Stack monitoring (~1 jour)

- Déployer Grafana + Loki + Promtail + Prometheus sur le VPS
- Exposer `grafana.pokclock.com` derrière Cloudflare Access
- Provisioning datasources (Postgres `pokclock`, Loki, Prometheus)
- 4 dashboards : Overview SaaS, Latency & Errors, Containers, Host

#### Cosmétique

- Cleanup secrets OVH inutilisés : `docker secret rm ovh_endpoint ovh_application_key ovh_application_secret ovh_consumer_key` sur le VPS
- Dockerfile.backup dédié (combine restic + postgresql16-client pré-installés) pour économiser ~3s d'`apk add` au boot

### Commits de la journée

#### `cedricleblond35/pokclock-api`
- `0a2f332` fix(traefik): switch cert resolver to cloudflare-dns after DNS migration
- (commits précédents de la matinée : RBAC, admin endpoints, issue-jwt CLI, lint fix, API_TOKEN_FILE read)

#### `cedricleblond35/pokclock-ops`
- `15bf9a7` fix(traefik): switch ops router to cloudflare-dns resolver
- (commits précédents : skeleton Next.js, eslint config, public/.gitkeep, HOSTNAME=0.0.0.0)

#### `cedricleblond35/pokclock-infra`
- `5cabf4d` fix(traefik): remove ovh-dns resolver to force wildcard cert via cloudflare
- `dc12d13` ci(infra): remove deploy.yml — Traefik deploy reste manuel
- `fc5eb07` feat(traefik): add Cloudflare DNS-01 resolver for wildcard `*.pokclock.com`

#### `cedricleblond35/PokClock` (sous-dossier `backend/`)
- **Modifié non commité** (à déployer via `wrangler deploy`) :
  - `migrations/0002_role_and_club.sql` (NEW)
  - `src/db/queries.ts` (LicenseRow + updateLicenseRoleAndClub)
  - `src/routes/verify.ts` (réponse JSON inclut role + clubId)
  - `src/routes/admin.ts` (accepte role + clubId à la création, nouveau endpoint POST /admin/promote)

### URLs actives en prod

- https://api.pokclock.com → 200 ✓ wildcard cert
- https://ops.pokclock.com → CF Access Google OAuth + Dashboard Next.js
- https://pokclock-license.dynamidoxa.workers.dev → Worker (à redéployer demain)
- Cloudflare DNS gère `pokclock.com` (NS `jim.ns.cloudflare.com` + `liberty.ns.cloudflare.com`)

---

## Bilan 2026-05-27 — Phases 0.C, 0.D et 0.E livrées

### Phase 0.C — Club ops baseline (livrée)

Couverture : un admin de club peut tout faire sur son propre club depuis l'app desktop, sans passer par le super-admin.

| Sous-phase | Livré | Repos / commits |
|---|---|---|
| 0.C-α structures partagées | x | pokclock-api migrations 005-007, contrats `ClubStructure*` côté WPF |
| 0.C-β GET /api/club/info + audit club-scoped | x | `club_audit_info.go`, route `/api/club/audit` |
| 0.C-γ partie 1+2+3 (CRUD licences + members + assignation) | x | `club_licenses.go`, `club_members.go`, migration 007, dialog `MemberDialog` côté WPF |
| 0.C-γ partie 3e (super-admin visibilité membres) | x | `GET /api/admin/clubs/:id/members`, page ops |

### Phase 0.D — Online registrations + published tournaments (livrée sauf γ.2 simple, γ.3 reporté)

| Sous-phase | Livré | Notes |
|---|---|---|
| 0.D-α tournois publiés + inscriptions anonymes + modération | x | migrations 008-011, table `published_tournaments` + `tournament_registrations`, emails Resend confirmation/decision, cancel_token magic link |
| 0.D-α.4 upsert republish + unpublish + auto-sync | x | partial unique index (status <> cancelled), auto-update à chaque save WPF |
| 0.D-β.1 liste joueurs publique + display modes | x | migration 012, 4 modes (pseudo / first_initial_last / full_name / hidden) configurés par tournoi |
| 0.D-β.2 prize structure | x | migration 013, `PrizeStructureBuilder` WPF (distribution standard 1-10 places), affichage React |
| 0.D-γ.1 barème de points | x | migration 014, dialog `PointSchemeDialog`, types linear / fixed / proportional, affichage React |
| 0.D-γ.2 publication classement final | x (intégré à 0.E.4) | migration 018, dialog `PublishResultsDialog` WPF, endpoint POST results |
| 0.D-γ.3 custom formula (cellule de calcul) | reporté | scope hors MVP, prévu plus tard |

### Phase 0.E — Comptes joueurs (livrée à 80%, Google OAuth + Calendar restants)

| Sous-phase | Livré | Notes |
|---|---|---|
| 0.E.1.a-d magic link auth + dashboard joueur | x | migrations 015-016, cookie `pokclock_player` Domain=.pokclock.com, JWT audience `pokclock-player`, pages `/joueurs/login` et `/joueurs/dashboard` |
| 0.E.1.e Google OAuth login | en attente | nouveau client OAuth GCP à créer, redirect `https://api.pokclock.com/api/players/auth/google/callback` |
| 0.E.2 inscription via compte (one-click) | x | migration 017 `player_id` sur registrations, endpoints `/api/players/me/clubs/:slug/tournaments/:id/register` + `/registrations/:id` cancel/list, composant `OneClickRegister` |
| 0.E.3 badge inscrit sur liste publique | x | enrichissement de `/clubs/[slug]/tournois` côté React |
| 0.E.4 résultats finaux + leaderboard club | x | migration 018, endpoints POST results admin + GET public results + leaderboard, page `/clubs/[slug]/classement`, dialog `PublishResultsDialog` côté WPF (wiring TournamentListView à finaliser côté user) |
| 0.E.5 sync Google Calendar | en attente | OAuth Calendar scope séparé, hook event création/suppression à l'inscription |

### Config VPS à appliquer avant prochain deploy

- Env `PLAYER_COOKIE_DOMAIN=.pokclock.com` à set sur le service Swarm `pokclock-api`
- Env `API_BASE_URL=https://api.pokclock.com` (a un défaut sain mais explicit ne coûte rien)
- Migrations 014-018 à appliquer au prochain boot de l'API (goose au démarrage)

### Reste WPF (toi sur Windows)

- Push depuis Windows : 4 commits non poussés `2991223` (filtre cancelled) + `0d04fca` (barème points) + `dfe42a2` (dialog publish results) + `f944050` (fix CI workflow PokClock rename)
- Wire `PublishResultsDialog` dans `TournamentListView` (bouton "Publier classement" sur tournoi)
- Rebuild + release Velopack

### Reste fonctionnel pour clore Phase 0.E

- 0.E.1.e Google OAuth login player (~0.5 jour quand le client OAuth GCP est prêt)
- 0.E.5 Google Calendar sync (~1-1.5 jour, OAuth Calendar scope + hook event auto à register/cancel)

---

## Update 2026-05-27 (suite) — Phase 0.E.1.e + 0.E.5 + 0.F + RGPD livrées

### Phase 0.E.1.e Google OAuth login

| Item | Livré | Notes |
|---|---|---|
| Backend handlers /api/players/auth/google/start + /callback | x | state CSRF via cookie HttpOnly+Lax 10min, scope `openid email profile` |
| UPSERT player par google_id puis email | x | Auto-link compte magic-link existant si même email |
| Frontend bouton "Continuer avec Google" sur /joueurs/login | x | Logo SVG officiel multi-couleur |
| Env vars GOOGLE_OAUTH_CLIENT_ID + GOOGLE_OAUTH_CLIENT_SECRET_FILE | x | Secret Swarm prioritaire |

**À set côté VPS** : créer un secret Swarm `google_oauth_client_secret` + ajouter `GOOGLE_OAUTH_CLIENT_ID` en env. Si manquant, le bouton renvoie sur `/joueurs/login?error=google_oauth_disabled`.

### Phase 0.E.5 Sync Google Calendar

| Item | Livré | Notes |
|---|---|---|
| Migrations 022 player_google_tokens + 023 google_event_id | x | Tokens en clair (réseau privé Swarm), refresh auto |
| OAuth Calendar séparé du login (scope calendar.events) | x | access_type=offline + prompt=consent pour refresh_token |
| Client googlecal (Insert/Update/Delete + RevokeToken) | x | Wrapper léger Calendar API v3 primary calendar |
| Service calendarSync avec auto-refresh access_token | x | Refresh quand < 1min de marge avant expiration |
| Hooks lifecycle : create at register, update at confirm, delete at reject/cancel | x | Fire-and-forget goroutines, échec n'impacte pas l'op DB |
| Frontend GoogleCalendarPanel sur dashboard | x | Connect/disconnect, status, notices ?calendar_connected/error |

**Client OAuth** : peut être le même que pour login Google. Ajouter le redirect URI `https://api.pokclock.com/api/players/me/google-calendar/callback` au client existant + ajouter le scope `https://www.googleapis.com/auth/calendar.events` dans le OAuth consent screen.

### Phase 0.F Adhésions joueurs + tournois réservés

| Item | Livré | Notes |
|---|---|---|
| Migration 020 player_club_memberships + 021 audience | x | Status pending/active/rejected/revoked, audience public/members_only |
| Backend endpoints joueur : list / join / leave | x | Re-trigger pending si row existante rejected/revoked |
| Backend admin : list + approve / reject / revoke | x | Modération via WPF Mon club > MEMBRES JOUEURS |
| Register endpoints refusent les members_only sans membership | x | Anonymes 403 + comptes sans membership active 403 |
| Frontend public : 4 cas dans aside selon (audience, auth, membership) | x | CTA Me connecter / Adhésion requise / OneClick / Form anonyme |
| Frontend dashboard : section Mes clubs avec status + bouton quitter | x | |
| WPF onglet MEMBRES JOUEURS distinct de MEMBRES staff | x | DataGrid + boutons Approuver / Refuser / Exclure |
| WPF checkbox "Réservé aux membres" dans PublishSettingsDialog | x | Audience auto-passé via BuildPublishRequestAsync |

### RGPD : suppression compte joueur

| Item | Livré | Notes |
|---|---|---|
| DELETE /api/players/me avec transaction d'anonymisation | x | tournament_registrations + tournament_results anonymisés, magic_links delete, players delete |
| Email best-effort sendAccountDeleted | x | Trace utilisateur post-suppression |
| Migration 019 + goroutine cleanup magic_links toutes les 6h | x | Évite que la table gonfle indéfiniment |
| Frontend bouton "Supprimer mon compte" en zone dangereuse | x | Confirmation inline (premier clic explique les conséquences) |

### Tables Postgres au 2026-05-27 (post 0.E + 0.F + 0.E.5)

`structures`, `structures_audit`, `clubs`, `licenses`, `members`,
`published_tournaments`, `tournament_registrations`, `point_schemes`,
`players`, `player_magic_links`, `tournament_results`,
**`player_club_memberships`**, **`player_google_tokens`**.

Détails complets : voir `~/workspace/pokclock-api/docs/DATABASE_SCHEMA.md`.
