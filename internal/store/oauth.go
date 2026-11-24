package store

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/intake-es/mothlog/internal/db"
)

// Prefijos de los tokens de OAuth del MCP, distintos del de la API.
const (
	OAuthAccessPrefix  = "mlo_"
	OAuthRefreshPrefix = "mlr_"
)

// Los de los tokens de OAuth emitidos antes del nombre Mothlog, que siguen
// valiendo hasta que caducan.
const (
	LegacyOAuthAccessPrefix  = "ieo_"
	LegacyOAuthRefreshPrefix = "ier_"
)

// IsOAuthAccess dice si secret tiene la forma de un token de acceso de OAuth, nuevo o de antes.
func IsOAuthAccess(secret string) bool {
	return strings.HasPrefix(secret, OAuthAccessPrefix) || strings.HasPrefix(secret, LegacyOAuthAccessPrefix)
}

var (
	// ErrOAuthReused: un código o un refresco ya gastado; la concesión que
	// salió de él queda revocada.
	ErrOAuthReused = errors.New("already used")
	// ErrOAuthExpired: el código ha caducado.
	ErrOAuthExpired = errors.New("ha caducado")
	// ErrOAuthTooMany: demasiados clientes registrados sin usar.
	ErrOAuthTooMany = errors.New("too many unused registered clients")
)

// Cuánto viven los clientes registrados sin usar y cuántos puede haber.
const (
	OAuthUnusedClientTTL = 24 * time.Hour
	maxUnusedClients     = 50000
)

type OAuthClient struct {
	ID           string
	Name         string
	RedirectURIs []string
}

// OAuthRequest es una petición de autorización, pendiente o ya consentida.
type OAuthRequest struct {
	ID            string
	ClientID      string
	ClientName    string
	RedirectURI   string
	Scopes        []string
	State         string
	CodeChallenge string
	Resource      string
	ExpiresAt     time.Time
	// Login y Granted, una vez consentida.
	Login   string
	Granted []string
}

// OAuthGrant es una concesión, como la ve quien la dio.
type OAuthGrant struct {
	ID          int64      `json:"id"`
	ClientID    string     `json:"client_id"`
	ClientName  string     `json:"client_name"`
	RedirectURI string     `json:"redirect_uri"`
	Scopes      []string   `json:"scopes"`
	CreatedAt   time.Time  `json:"created_at"`
	LastUsedAt  *time.Time `json:"last_used_at"`
	LastUsedIP  *string    `json:"last_used_ip"`
}

// OAuthTokens son los que se entregan al canjear un código o un refresco.
type OAuthTokens struct {
	Access    string
	Refresh   string
	Scopes    []string
	ExpiresIn time.Duration
}

// OAuthAccess es quien llama con un token de acceso.
type OAuthAccess struct {
	GrantID    int64
	Login      string
	ClientID   string
	ClientName string
	Scopes     []string
	Resource   string
}

// OAuthTTL son las vidas de los tokens.
type OAuthTTL struct {
	Access  time.Duration
	Refresh time.Duration
}

func joinScopes(s []string) string { return strings.Join(s, " ") }

func splitScopes(s string) []string { return strings.Fields(s) }

func newSecret(prefix string) (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return prefix + base64.RawURLEncoding.EncodeToString(b), nil
}

// CreateOAuthClient guarda un cliente del registro dinámico y, de paso,
// borra los que llevan un día sin usarse.
func (s *Store) CreateOAuthClient(ctx context.Context, c OAuthClient, now time.Time) error {
	uris, err := json.Marshal(c.RedirectURIs)
	if err != nil {
		return err
	}
	return db.BeginFunc(ctx, s.pool, func(tx db.Tx) error {
		if _, err := tx.Exec(ctx, `DELETE FROM oauth_clients WHERE used_at IS NULL AND created_at < $1`,
			now.Add(-OAuthUnusedClientTTL)); err != nil {
			return fmt.Errorf("delete unused clients: %w", err)
		}
		var unused int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM oauth_clients WHERE used_at IS NULL`).Scan(&unused); err != nil {
			return fmt.Errorf("count clients: %w", err)
		}
		if unused >= maxUnusedClients {
			return ErrOAuthTooMany
		}
		if _, err := tx.Exec(ctx, `INSERT INTO oauth_clients (client_id, name, redirect_uris, created_at) VALUES ($1, $2, $3, $4)`,
			c.ID, c.Name, string(uris), now); err != nil {
			return fmt.Errorf("register client: %w", err)
		}
		return nil
	})
}

// OAuthClientByID es ErrNotFound si no existe o caducó sin usarse.
func (s *Store) OAuthClientByID(ctx context.Context, id string, now time.Time) (OAuthClient, error) {
	c := OAuthClient{ID: id}
	var uris string
	err := s.pool.QueryRow(ctx, `
		SELECT name, redirect_uris FROM oauth_clients
		WHERE client_id = $1 AND (used_at IS NOT NULL OR created_at >= $2)`,
		id, now.Add(-OAuthUnusedClientTTL)).Scan(&c.Name, &uris)
	if errors.Is(err, db.ErrNoRows) {
		return OAuthClient{}, ErrNotFound
	}
	if err != nil {
		return OAuthClient{}, fmt.Errorf("read client: %w", err)
	}
	if err := json.Unmarshal([]byte(uris), &c.RedirectURIs); err != nil {
		return OAuthClient{}, fmt.Errorf("read client: %w", err)
	}
	return c, nil
}

// CreateOAuthRequest guarda una petición de autorización pendiente.
func (s *Store) CreateOAuthRequest(ctx context.Context, r OAuthRequest, now time.Time) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO oauth_requests (id, client_id, client_name, redirect_uri, scopes, state, code_challenge, resource, created_at, expires_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)`,
		r.ID, r.ClientID, r.ClientName, r.RedirectURI, joinScopes(r.Scopes), r.State, r.CodeChallenge, r.Resource, now, r.ExpiresAt)
	if err != nil {
		return fmt.Errorf("save authorization request: %w", err)
	}
	return nil
}

const oauthRequestCols = `id, client_id, client_name, redirect_uri, scopes, state, code_challenge, resource, expires_at`

func scanOAuthRequest(row db.Row, extra ...any) (OAuthRequest, error) {
	var r OAuthRequest
	var scopes string
	err := row.Scan(append([]any{&r.ID, &r.ClientID, &r.ClientName, &r.RedirectURI, &scopes, &r.State,
		&r.CodeChallenge, &r.Resource, &r.ExpiresAt}, extra...)...)
	r.Scopes = splitScopes(scopes)
	return r, err
}

// PendingOAuthRequest es la petición si aún espera el consentimiento.
func (s *Store) PendingOAuthRequest(ctx context.Context, id string, now time.Time) (OAuthRequest, error) {
	r, err := scanOAuthRequest(s.pool.QueryRow(ctx, `SELECT `+oauthRequestCols+` FROM oauth_requests
		WHERE id = $1 AND code_sha256 IS NULL AND expires_at > $2`, id, now))
	if errors.Is(err, db.ErrNoRows) {
		return OAuthRequest{}, ErrNotFound
	}
	if err != nil {
		return OAuthRequest{}, fmt.Errorf("read authorization request: %w", err)
	}
	return r, nil
}

// ApproveOAuthRequest apunta que login concede scopes y devuelve el código,
// que vale hasta codeExpires y se guarda solo como hash.
func (s *Store) ApproveOAuthRequest(ctx context.Context, id, login string, scopes []string, codeExpires, now time.Time) (OAuthRequest, string, error) {
	code, err := newSecret("")
	if err != nil {
		return OAuthRequest{}, "", err
	}
	r, err := scanOAuthRequest(s.pool.QueryRow(ctx, `
		UPDATE oauth_requests SET login = $2, granted_scopes = $3, code_sha256 = $4, code_expires_at = $5
		WHERE id = $1 AND code_sha256 IS NULL AND expires_at > $6
		RETURNING `+oauthRequestCols, id, login, joinScopes(scopes), hashToken(code), codeExpires, now))
	if errors.Is(err, db.ErrNoRows) {
		return OAuthRequest{}, "", ErrNotFound
	}
	if err != nil {
		return OAuthRequest{}, "", fmt.Errorf("consent: %w", err)
	}
	r.Login, r.Granted = login, scopes
	return r, code, nil
}

// DenyOAuthRequest borra la petición pendiente y la devuelve.
func (s *Store) DenyOAuthRequest(ctx context.Context, id string, now time.Time) (OAuthRequest, error) {
	r, err := scanOAuthRequest(s.pool.QueryRow(ctx, `
		DELETE FROM oauth_requests WHERE id = $1 AND code_sha256 IS NULL AND expires_at > $2
		RETURNING `+oauthRequestCols, id, now))
	if errors.Is(err, db.ErrNoRows) {
		return OAuthRequest{}, ErrNotFound
	}
	if err != nil {
		return OAuthRequest{}, fmt.Errorf("deny: %w", err)
	}
	return r, nil
}

// ExchangeOAuthCode canjea un código: lo gasta, pasa la petición por check
// (cliente, URI de vuelta, PKCE, recurso) y, si vale, crea la concesión y sus
// primeros tokens. El código queda gastado aunque check falle. Un código ya
// gastado revoca la concesión que salió de él y es ErrOAuthReused.
func (s *Store) ExchangeOAuthCode(ctx context.Context, code string, check func(OAuthRequest) error, ttl OAuthTTL, now time.Time) (OAuthTokens, error) {
	var out OAuthTokens
	var failed error
	err := db.BeginFunc(ctx, s.pool, func(tx db.Tx) error {
		var login, granted *string
		var codeExpires *time.Time
		r, err := scanOAuthRequest(tx.QueryRow(ctx, `
			UPDATE oauth_requests SET used_at = $2 WHERE code_sha256 = $1 AND used_at IS NULL
			RETURNING `+oauthRequestCols+`, login, granted_scopes, code_expires_at`, hashToken(code), now),
			&login, &granted, &codeExpires)
		if errors.Is(err, db.ErrNoRows) {
			var grantID *int64
			err := tx.QueryRow(ctx, `SELECT grant_id FROM oauth_requests WHERE code_sha256 = $1`, hashToken(code)).Scan(&grantID)
			if errors.Is(err, db.ErrNoRows) {
				failed = ErrNotFound
				return nil
			}
			if err != nil {
				return err
			}
			if grantID != nil {
				if _, err := tx.Exec(ctx, `UPDATE oauth_grants SET revoked_at = $2 WHERE id = $1 AND revoked_at IS NULL`, *grantID, now); err != nil {
					return err
				}
			}
			failed = ErrOAuthReused
			return nil
		}
		if err != nil {
			return err
		}
		if login == nil || granted == nil || codeExpires == nil {
			failed = ErrNotFound
			return nil
		}
		if !now.Before(*codeExpires) {
			failed = ErrOAuthExpired
			return nil
		}
		r.Login, r.Granted = *login, splitScopes(*granted)
		if failed = check(r); failed != nil {
			return nil
		}
		var grantID int64
		if err := tx.QueryRow(ctx, `
			INSERT INTO oauth_grants (login, client_id, client_name, redirect_uri, scopes, resource, created_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7) RETURNING id`,
			r.Login, r.ClientID, r.ClientName, r.RedirectURI, joinScopes(r.Granted), r.Resource, now).Scan(&grantID); err != nil {
			return fmt.Errorf("create grant: %w", err)
		}
		if _, err := tx.Exec(ctx, `UPDATE oauth_requests SET grant_id = $2 WHERE id = $1`, r.ID, grantID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE oauth_clients SET used_at = $2 WHERE client_id = $1`, r.ClientID, now); err != nil {
			return err
		}
		out, err = issueOAuthTokens(ctx, tx, grantID, r.Granted, ttl, now)
		return err
	})
	if err != nil {
		return OAuthTokens{}, fmt.Errorf("redeem code: %w", err)
	}
	return out, failed
}

func issueOAuthTokens(ctx context.Context, q Querier, grantID int64, scopes []string, ttl OAuthTTL, now time.Time) (OAuthTokens, error) {
	access, err := newSecret(OAuthAccessPrefix)
	if err != nil {
		return OAuthTokens{}, err
	}
	refresh, err := newSecret(OAuthRefreshPrefix)
	if err != nil {
		return OAuthTokens{}, err
	}
	sc := joinScopes(scopes)
	if _, err := q.Exec(ctx, `
		INSERT INTO oauth_tokens (token_sha256, grant_id, kind, scopes, created_at, expires_at)
		VALUES ($1, $2, 'access', $3, $4, $5), ($6, $2, 'refresh', $3, $4, $7)`,
		hashToken(access), grantID, sc, now, now.Add(ttl.Access), hashToken(refresh), now.Add(ttl.Refresh)); err != nil {
		return OAuthTokens{}, fmt.Errorf("save tokens: %w", err)
	}
	return OAuthTokens{Access: access, Refresh: refresh, Scopes: scopes, ExpiresIn: ttl.Access}, nil
}

// RefreshOAuth canjea un token de refresco por otro y uno de acceso nuevos.
// Con scopes, el acceso se queda en esos, que tienen que estar entre los del
// refresco. Un refresco ya canjeado revoca la concesión entera y es
// ErrOAuthReused.
func (s *Store) RefreshOAuth(ctx context.Context, refresh, clientID string, scopes []string, ttl OAuthTTL, now time.Time) (OAuthTokens, error) {
	var out OAuthTokens
	var failed error
	err := db.BeginFunc(ctx, s.pool, func(tx db.Tx) error {
		var grantID int64
		var have string
		err := tx.QueryRow(ctx, `
			UPDATE oauth_tokens SET used_at = $2
			WHERE token_sha256 = $1 AND kind = 'refresh' AND used_at IS NULL AND expires_at > $2
			RETURNING grant_id, scopes`, hashToken(refresh), now).Scan(&grantID, &have)
		if errors.Is(err, db.ErrNoRows) {
			var used *time.Time
			err := tx.QueryRow(ctx, `SELECT grant_id, used_at FROM oauth_tokens WHERE token_sha256 = $1 AND kind = 'refresh'`,
				hashToken(refresh)).Scan(&grantID, &used)
			if errors.Is(err, db.ErrNoRows) || (err == nil && used == nil) {
				failed = ErrNotFound
				return nil
			}
			if err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `UPDATE oauth_grants SET revoked_at = $2 WHERE id = $1 AND revoked_at IS NULL`, grantID, now); err != nil {
				return err
			}
			failed = ErrOAuthReused
			return nil
		}
		if err != nil {
			return err
		}
		var client string
		var revoked *time.Time
		if err := tx.QueryRow(ctx, `SELECT client_id, revoked_at FROM oauth_grants WHERE id = $1`, grantID).Scan(&client, &revoked); err != nil {
			return err
		}
		if revoked != nil || client != clientID {
			failed = ErrNotFound
			return nil
		}
		granted := splitScopes(have)
		if len(scopes) > 0 {
			for _, sc := range scopes {
				if !slices.Contains(granted, sc) {
					failed = fmt.Errorf("%w: scope %q was not granted", ErrInvalid, sc)
					return nil
				}
			}
			granted = scopes
		}
		if _, err := tx.Exec(ctx, `DELETE FROM oauth_tokens WHERE grant_id = $1 AND expires_at <= $2`, grantID, now); err != nil {
			return err
		}
		out, err = issueOAuthTokens(ctx, tx, grantID, granted, ttl, now)
		if err != nil {
			return err
		}
		// El refresco nuevo guarda los permisos de la concesión, no los recortados.
		_, err = tx.Exec(ctx, `UPDATE oauth_tokens SET scopes = $2 WHERE token_sha256 = $1`, hashToken(out.Refresh), have)
		return err
	})
	if err != nil {
		return OAuthTokens{}, fmt.Errorf("refresh token: %w", err)
	}
	return out, failed
}

// AuthenticateOAuth devuelve quién llama con el token de acceso, si vale, y
// apunta cuándo y desde qué dirección se usó (como mucho una vez por minuto,
// o al cambiar de dirección). ErrNotFound si no vale.
func (s *Store) AuthenticateOAuth(ctx context.Context, access, ip string, now time.Time) (OAuthAccess, error) {
	if !IsOAuthAccess(access) {
		return OAuthAccess{}, ErrNotFound
	}
	var a OAuthAccess
	var scopes string
	var lastUsed *time.Time
	var lastIP *string
	err := s.pool.QueryRow(ctx, `
		SELECT g.id, g.login, g.client_id, g.client_name, t.scopes, g.resource, g.last_used_at, g.last_used_ip
		FROM oauth_tokens t JOIN oauth_grants g ON g.id = t.grant_id
		WHERE t.token_sha256 = $1 AND t.kind = 'access' AND t.expires_at > $2 AND g.revoked_at IS NULL`,
		hashToken(access), now).Scan(&a.GrantID, &a.Login, &a.ClientID, &a.ClientName, &scopes, &a.Resource, &lastUsed, &lastIP)
	if errors.Is(err, db.ErrNoRows) {
		return OAuthAccess{}, ErrNotFound
	}
	if err != nil {
		return OAuthAccess{}, fmt.Errorf("find OAuth token: %w", err)
	}
	a.Scopes = splitScopes(scopes)
	if lastUsed == nil || now.Sub(*lastUsed) > time.Minute || lastIP == nil || *lastIP != ip {
		if _, err := s.pool.Exec(ctx, `UPDATE oauth_grants SET last_used_at = $2, last_used_ip = $3 WHERE id = $1`,
			a.GrantID, now, ip); err != nil {
			return OAuthAccess{}, fmt.Errorf("mark grant use: %w", err)
		}
	}
	return a, nil
}

// RevokeOAuthToken revoca un token del cliente clientID (RFC 7009): un
// refresco revoca la concesión entera y uno de acceso, solo ese token. Un
// token que no existe, o de otro cliente, no hace nada.
func (s *Store) RevokeOAuthToken(ctx context.Context, token, clientID string, now time.Time) error {
	var grantID int64
	var kind, client string
	err := s.pool.QueryRow(ctx, `
		SELECT t.grant_id, t.kind, g.client_id FROM oauth_tokens t JOIN oauth_grants g ON g.id = t.grant_id
		WHERE t.token_sha256 = $1`, hashToken(token)).Scan(&grantID, &kind, &client)
	if errors.Is(err, db.ErrNoRows) || (err == nil && client != clientID) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("revoke token: %w", err)
	}
	if kind == "access" {
		_, err = s.pool.Exec(ctx, `DELETE FROM oauth_tokens WHERE token_sha256 = $1`, hashToken(token))
	} else {
		_, err = s.pool.Exec(ctx, `UPDATE oauth_grants SET revoked_at = $2 WHERE id = $1 AND revoked_at IS NULL`, grantID, now)
	}
	if err != nil {
		return fmt.Errorf("revoke token: %w", err)
	}
	return nil
}

// OAuthGrants son las concesiones vivas de login: sin revocar y con algún
// token que aún vale, de la más nueva a la más vieja.
func (s *Store) OAuthGrants(ctx context.Context, login string, now time.Time) ([]OAuthGrant, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT g.id, g.client_id, g.client_name, g.redirect_uri, g.scopes, g.created_at, g.last_used_at, g.last_used_ip
		FROM oauth_grants g
		WHERE g.login = $1 AND g.revoked_at IS NULL AND EXISTS (
			SELECT 1 FROM oauth_tokens t WHERE t.grant_id = g.id AND t.expires_at > $2 AND t.used_at IS NULL)
		ORDER BY g.created_at DESC, g.id DESC`, login, now)
	if err != nil {
		return nil, fmt.Errorf("list grants: %w", err)
	}
	return db.CollectRows(rows, func(r db.CollectableRow) (OAuthGrant, error) {
		var g OAuthGrant
		var scopes string
		err := r.Scan(&g.ID, &g.ClientID, &g.ClientName, &g.RedirectURI, &scopes, &g.CreatedAt, &g.LastUsedAt, &g.LastUsedIP)
		g.Scopes = splitScopes(scopes)
		return g, err
	})
}

// RevokeOAuthGrant revoca una concesión de login; ErrNotFound si no es suya
// o ya estaba revocada.
func (s *Store) RevokeOAuthGrant(ctx context.Context, login string, id int64, now time.Time) error {
	tag, err := s.pool.Exec(ctx, `UPDATE oauth_grants SET revoked_at = $3 WHERE id = $1 AND login = $2 AND revoked_at IS NULL`,
		id, login, now)
	if err != nil {
		return fmt.Errorf("revoke grant: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// RevokeOAuthGrantsOf revoca todas las concesiones de la persona (login sin
// mirar mayúsculas) y dice cuántas eran.
func (s *Store) RevokeOAuthGrantsOf(ctx context.Context, login string, now time.Time) (int64, error) {
	tag, err := s.pool.Exec(ctx, `
		UPDATE oauth_grants SET revoked_at = $2
		WHERE login IN (SELECT login FROM people WHERE lower(login) = lower($1)) AND revoked_at IS NULL`, login, now)
	if err != nil {
		return 0, fmt.Errorf("revoke grants: %w", err)
	}
	return tag.RowsAffected(), nil
}

// PruneOAuth borra lo que ya no sirve: peticiones y tokens caducados (con un
// día de margen, para seguir reconociendo un código reusado), clientes sin
// usar y concesiones revocadas hace más de 30 días.
func (s *Store) PruneOAuth(ctx context.Context, now time.Time) error {
	for _, q := range []struct {
		sql string
		at  time.Time
	}{
		{`DELETE FROM oauth_requests WHERE expires_at < $1`, now.Add(-24 * time.Hour)},
		{`DELETE FROM oauth_tokens WHERE expires_at < $1`, now},
		{`DELETE FROM oauth_clients WHERE used_at IS NULL AND created_at < $1`, now.Add(-OAuthUnusedClientTTL)},
		{`DELETE FROM oauth_grants WHERE revoked_at < $1`, now.Add(-30 * 24 * time.Hour)},
	} {
		if _, err := s.pool.Exec(ctx, q.sql, q.at); err != nil {
			return fmt.Errorf("clean up OAuth: %w", err)
		}
	}
	return nil
}
