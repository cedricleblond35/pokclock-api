package api

import (
	"context"
	"fmt"
	"html"
	"log/slog"
	"strings"

	"github.com/cedricleblond35/pokclock-api/internal/clients/resend"
)

// Templates emails FR pour les notifications d'inscriptions online.
// HTML inline minimal (compatible Gmail/Outlook). Pas de framework MJML
// pour rester dépendance-free et debugger facile.
//
// Best-effort : tous les Send* loggent une warning sur échec mais ne
// retournent pas d'erreur au caller — l'inscription doit aboutir même
// si Resend est down.

type emailContext struct {
	client        *resend.Client
	publicSiteURL string
	apiBaseURL    string // ex "https://api.pokclock.com", utilisé pour les liens magiques joueur (0.E.1)
	logger        *slog.Logger
}

// sendRegisterConfirmation : envoyé au candidat juste après son inscription.
// Inclut le lien magique d'annulation. Si Resend non configuré, no-op.
func (e *emailContext) sendRegisterConfirmation(ctx context.Context, in registerConfirmationData) {
	if e.client == nil || !e.client.IsConfigured() {
		return
	}
	html := buildRegisterConfirmationHTML(in, e.publicSiteURL)
	text := buildRegisterConfirmationText(in, e.publicSiteURL)
	msg := resend.Message{
		ToEmail: in.Email,
		ToName:  in.FirstName + " " + in.LastName,
		Subject: fmt.Sprintf("Inscription reçue : %s", in.TournamentName),
		HTML:    html,
		Text:    text,
	}
	if err := e.client.Send(ctx, msg); err != nil {
		e.logger.Warn("send register confirmation failed", "err", err, "email", in.Email)
	}
}

// sendModerationDecision : envoyé après confirm/reject par l'admin.
// confirmed=true ⇒ "Inscription validée", false ⇒ "Inscription refusée".
func (e *emailContext) sendModerationDecision(ctx context.Context, in moderationDecisionData) {
	if e.client == nil || !e.client.IsConfigured() {
		return
	}
	subject := fmt.Sprintf("Inscription refusée : %s", in.TournamentName)
	html := buildRejectHTML(in, e.publicSiteURL)
	text := buildRejectText(in, e.publicSiteURL)
	if in.Confirmed {
		subject = fmt.Sprintf("Inscription validée : %s", in.TournamentName)
		html = buildConfirmHTML(in, e.publicSiteURL)
		text = buildConfirmText(in, e.publicSiteURL)
	}
	msg := resend.Message{
		ToEmail: in.Email,
		ToName:  in.FirstName + " " + in.LastName,
		Subject: subject,
		HTML:    html,
		Text:    text,
	}
	if err := e.client.Send(ctx, msg); err != nil {
		e.logger.Warn("send moderation email failed", "err", err, "email", in.Email, "confirmed", in.Confirmed)
	}
}

// sendPlayerMagicLink : envoyé au joueur qui a demandé un magic link login.
// Lien à 15 min de validité. Si Resend non configuré, no-op (le caller log).
func (e *emailContext) sendPlayerMagicLink(ctx context.Context, in playerMagicLinkData) {
	if e.client == nil || !e.client.IsConfigured() {
		return
	}
	msg := resend.Message{
		ToEmail: in.Email,
		ToName:  in.Email,
		Subject: "Ton lien de connexion PokClock",
		HTML:    buildPlayerMagicLinkHTML(in, e.apiBaseURL),
		Text:    buildPlayerMagicLinkText(in, e.apiBaseURL),
	}
	if err := e.client.Send(ctx, msg); err != nil {
		e.logger.Warn("send player magic link failed", "err", err, "email", in.Email)
	}
}

type playerMagicLinkData struct {
	Email string
	Token string
}

func buildPlayerMagicLinkHTML(d playerMagicLinkData, apiBaseURL string) string {
	// Le lien pointe directement sur l'API : elle pose le cookie de session
	// puis redirige vers le dashboard côté frontend. Évite un aller-retour
	// RSC qui ne pourrait pas écrire le cookie d'auth sur .pokclock.com.
	url := fmt.Sprintf("%s/api/players/auth/magic-link/verify?token=%s", apiBaseURL, d.Token)
	return wrapEmailHTML(fmt.Sprintf(`
<h2 style="margin:0 0 16px;color:#FFD700;font-size:20px;">Connexion à ton compte</h2>
<p>Bonjour,</p>
<p>Clique sur le bouton ci-dessous pour te connecter à ton compte joueur PokClock. Ce lien expire dans 15 minutes et ne peut être utilisé qu'une fois.</p>
<p style="margin:24px 0;">
  <a href="%s" style="display:inline-block;padding:12px 20px;background:#FFD700;color:#000;text-decoration:none;border-radius:4px;font-weight:bold;">Me connecter</a>
</p>
<p style="font-size:13px;color:#aaa;">Si le bouton ne marche pas, copie ce lien :<br><span style="word-break:break-all;color:#888;">%s</span></p>
<p style="margin-top:24px;font-size:11px;color:#666;">Si tu n'as pas demandé cette connexion, tu peux ignorer ce message.</p>
`, url, url))
}

func buildPlayerMagicLinkText(d playerMagicLinkData, apiBaseURL string) string {
	url := fmt.Sprintf("%s/api/players/auth/magic-link/verify?token=%s", apiBaseURL, d.Token)
	return fmt.Sprintf(`Bonjour,

Clique sur ce lien pour te connecter à ton compte joueur PokClock :
%s

Ce lien expire dans 15 minutes et ne peut être utilisé qu'une fois.

Si tu n'as pas demandé cette connexion, ignore ce message.

— PokClock
`, url)
}

type registerConfirmationData struct {
	FirstName       string
	LastName        string
	Email           string
	TournamentName  string
	TournamentDate  string // déjà formatée FR
	ClubName        string
	BuyIn           string // déjà formaté ex "50,00 €"
	CancelToken     string
}

type moderationDecisionData struct {
	FirstName      string
	LastName       string
	Email          string
	TournamentName string
	TournamentDate string
	ClubName       string
	Confirmed      bool   // true=confirm, false=reject
	Reason         string // motif optionnel sur reject
}

// ---- HTML / Text builders ----

func buildRegisterConfirmationHTML(d registerConfirmationData, siteURL string) string {
	cancelURL := fmt.Sprintf("%s/fr/inscriptions/%s/annuler", siteURL, d.CancelToken)
	return wrapEmailHTML(fmt.Sprintf(`
<h2 style="margin:0 0 16px;color:#FFD700;font-size:20px;">Inscription reçue</h2>
<p>Bonjour %s,</p>
<p>Ton inscription pour <strong>%s</strong> (%s) a bien été reçue.</p>
<p style="background:#1a1a1a;border-left:3px solid #FFD700;padding:12px;margin:16px 0;">
  <strong>État :</strong> en attente de validation par l'organisateur du club <strong>%s</strong>.
</p>
<p>Tu recevras un nouvel email dès que ton inscription sera validée (ou refusée).</p>
<p style="margin-top:24px;font-size:13px;color:#aaa;">
  Buy-in : %s · règle ta place sur place le jour J (pas de paiement en ligne).
</p>
<p style="margin-top:24px;">
  Tu peux annuler ton inscription à tout moment :<br>
  <a href="%s" style="display:inline-block;margin-top:8px;padding:10px 16px;background:#7a1f1f;color:#fff;text-decoration:none;border-radius:4px;">Annuler mon inscription</a>
</p>
`, html.EscapeString(d.FirstName), html.EscapeString(d.TournamentName), html.EscapeString(d.TournamentDate),
		html.EscapeString(d.ClubName), html.EscapeString(d.BuyIn), cancelURL))
}

func buildRegisterConfirmationText(d registerConfirmationData, siteURL string) string {
	cancelURL := fmt.Sprintf("%s/fr/inscriptions/%s/annuler", siteURL, d.CancelToken)
	return fmt.Sprintf(`Bonjour %s,

Ton inscription pour "%s" (%s) a bien été reçue.

État : en attente de validation par l'organisateur du club %s.

Buy-in : %s — règle sur place le jour J.

Tu peux annuler à tout moment via ce lien :
%s

— PokClock
`, d.FirstName, d.TournamentName, d.TournamentDate, d.ClubName, d.BuyIn, cancelURL)
}

func buildConfirmHTML(d moderationDecisionData, siteURL string) string {
	return wrapEmailHTML(fmt.Sprintf(`
<h2 style="margin:0 0 16px;color:#3da53d;font-size:20px;">Ta place est confirmée ✓</h2>
<p>Bonjour %s,</p>
<p>L'organisateur a validé ton inscription pour <strong>%s</strong> (%s) au club <strong>%s</strong>.</p>
<p style="background:#1a3a1a;border-left:3px solid #3da53d;padding:12px;margin:16px 0;">
  À très vite à la table !
</p>
<p style="margin-top:24px;font-size:13px;color:#aaa;">
  Tu peux toujours te désinscrire depuis le lien reçu dans l'email précédent.
</p>
`, html.EscapeString(d.FirstName), html.EscapeString(d.TournamentName),
		html.EscapeString(d.TournamentDate), html.EscapeString(d.ClubName)))
}

func buildConfirmText(d moderationDecisionData, _ string) string {
	return fmt.Sprintf(`Bonjour %s,

L'organisateur a validé ton inscription pour "%s" (%s) au club %s.

À très vite à la table !

— PokClock
`, d.FirstName, d.TournamentName, d.TournamentDate, d.ClubName)
}

func buildRejectHTML(d moderationDecisionData, _ string) string {
	reasonBlock := ""
	if strings.TrimSpace(d.Reason) != "" {
		reasonBlock = fmt.Sprintf(`<p style="background:#3a1f1f;border-left:3px solid #a53d3d;padding:12px;margin:16px 0;"><strong>Motif :</strong> %s</p>`, html.EscapeString(d.Reason))
	}
	return wrapEmailHTML(fmt.Sprintf(`
<h2 style="margin:0 0 16px;color:#f5a9a9;font-size:20px;">Inscription refusée</h2>
<p>Bonjour %s,</p>
<p>L'organisateur du club <strong>%s</strong> n'a pas validé ton inscription pour <strong>%s</strong> (%s).</p>
%s
<p style="margin-top:24px;font-size:13px;color:#aaa;">
  Pour plus d'infos, contacte directement le club.
</p>
`, html.EscapeString(d.FirstName), html.EscapeString(d.ClubName),
		html.EscapeString(d.TournamentName), html.EscapeString(d.TournamentDate), reasonBlock))
}

func buildRejectText(d moderationDecisionData, _ string) string {
	reasonLine := ""
	if strings.TrimSpace(d.Reason) != "" {
		reasonLine = "\nMotif : " + d.Reason + "\n"
	}
	return fmt.Sprintf(`Bonjour %s,

L'organisateur du club %s n'a pas validé ton inscription pour "%s" (%s).
%s
Pour plus d'infos, contacte directement le club.

— PokClock
`, d.FirstName, d.ClubName, d.TournamentName, d.TournamentDate, reasonLine)
}

// wrapEmailHTML enveloppe le contenu dans un layout minimal (background sombre,
// largeur fixe 600px). Compatible Gmail/Outlook (pas de CSS externe).
func wrapEmailHTML(content string) string {
	return fmt.Sprintf(`<!DOCTYPE html>
<html lang="fr"><head><meta charset="UTF-8"></head>
<body style="margin:0;padding:24px;background:#0a0a0a;font-family:-apple-system,BlinkMacSystemFont,'Segoe UI',sans-serif;color:#f5f5f5;">
<table role="presentation" width="100%%" cellpadding="0" cellspacing="0" border="0">
  <tr><td align="center">
    <table role="presentation" width="600" cellpadding="0" cellspacing="0" border="0" style="max-width:600px;background:#141414;border:1px solid #2d2d2d;border-radius:8px;padding:32px;">
      <tr><td>
        <div style="text-align:center;margin-bottom:24px;">
          <span style="font-size:18px;font-weight:bold;color:#FFD700;">♠ POKCLOCK</span>
        </div>
        %s
      </td></tr>
    </table>
    <p style="margin-top:16px;font-size:11px;color:#666;text-align:center;">
      Tu reçois cet email parce qu'une inscription a été créée à ton nom sur pokclock.com.<br>
      Si ce n'est pas toi, ignore ce message ou contacte support@pokclock.com.
    </p>
  </td></tr>
</table>
</body></html>`, content)
}
