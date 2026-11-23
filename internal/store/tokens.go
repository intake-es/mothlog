package store

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/intake-es/intake-errors/internal/db"
)

const TokenPrefix = "ie_"

// ErrNameTaken: ya hay un token activo con ese nombre.
var ErrNameTaken = errors.New("ya hay un token activo con ese nombre")

type Token struct {
	ID         int64      `json:"id"`
	Name       string     `json:"name"`
	CreatedAt  time.Time  `json:"created_at"`
	LastUsedAt *time.Time `json:"last_used_at"`
	RevokedAt  *time.Time `json:"revoked_at"`
}

func hashToken(secret string) []byte {
	h := sha256.Sum256([]byte(secret))
	return h[:]
}

// CreateToken guarda solo el sha256; el token en claro se devuelve una vez.
func (s *Store) CreateToken(ctx context.Context, name string) (Token, string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return Token{}, "", fmt.Errorf("%w: el token necesita un nombre", ErrInvalid)
	}
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return Token{}, "", err
	}
	secret := TokenPrefix + base64.RawURLEncoding.EncodeToString(b)
	t := Token{Name: name}
	err := s.pool.QueryRow(ctx,
		`INSERT INTO api_tokens (name, token_sha256) VALUES ($1, $2) RETURNING id, created_at`,
		name, hashToken(secret)).Scan(&t.ID, &t.CreatedAt)
	if db.IsUniqueViolation(err) {
		return Token{}, "", ErrNameTaken
	}
	if err != nil {
		return Token{}, "", fmt.Errorf("crear token: %w", err)
	}
	return t, secret, nil
}

func (s *Store) ListTokens(ctx context.Context) ([]Token, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, name, created_at, last_used_at, revoked_at
		FROM api_tokens ORDER BY revoked_at IS NOT NULL, name, id`)
	if err != nil {
		return nil, fmt.Errorf("listar tokens: %w", err)
	}
	return db.CollectRows(rows, func(r db.CollectableRow) (Token, error) {
		var t Token
		err := r.Scan(&t.ID, &t.Name, &t.CreatedAt, &t.LastUsedAt, &t.RevokedAt)
		return t, err
	})
}

// RevokeToken revoca el token activo con ese nombre.
func (s *Store) RevokeToken(ctx context.Context, name string) error {
	tag, err := s.pool.Exec(ctx,
		`UPDATE api_tokens SET revoked_at = now() WHERE name = $1 AND revoked_at IS NULL`, name)
	if err != nil {
		return fmt.Errorf("revocar token: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// Authenticate devuelve el token activo que corresponde a secret, o ErrNotFound.
func (s *Store) Authenticate(ctx context.Context, secret string) (Token, error) {
	if !strings.HasPrefix(secret, TokenPrefix) {
		return Token{}, ErrNotFound
	}
	var t Token
	err := s.pool.QueryRow(ctx, `
		SELECT id, name, created_at, last_used_at, revoked_at
		FROM api_tokens WHERE token_sha256 = $1 AND revoked_at IS NULL`,
		hashToken(secret)).Scan(&t.ID, &t.Name, &t.CreatedAt, &t.LastUsedAt, &t.RevokedAt)
	if errors.Is(err, db.ErrNoRows) {
		return Token{}, ErrNotFound
	}
	if err != nil {
		return Token{}, fmt.Errorf("buscar token: %w", err)
	}
	// Como mucho una escritura por minuto y token.
	if t.LastUsedAt == nil || time.Since(*t.LastUsedAt) > time.Minute {
		if _, err := s.pool.Exec(ctx, `UPDATE api_tokens SET last_used_at = now() WHERE id = $1`, t.ID); err != nil {
			return Token{}, fmt.Errorf("marcar uso del token: %w", err)
		}
	}
	return t, nil
}
