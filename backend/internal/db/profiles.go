package db

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"
)

type ConnectionProfile struct {
	ID                     string       `json:"id"`
	UserID                 string       `json:"user_id"`
	Name                   string       `json:"name"`
	Provider               string       `json:"provider"`
	URL                    string       `json:"url,omitempty"`
	Username               string       `json:"username,omitempty"`
	PasswordEncrypted      string       `json:"-"`
	RefreshTokenEncrypted  string       `json:"-"`
	MegaSessionIDEncrypted string       `json:"-"`
	MegaMasterKeyEncrypted string       `json:"-"`
	TokenExpiresAt         sql.NullTime `json:"token_expires_at,omitempty"`
	OAuthUser              string       `json:"oauth_user,omitempty"`
	CreatedAt              time.Time    `json:"created_at"`
	UpdatedAt              time.Time    `json:"updated_at"`
}

type ConnectionProfilePublic struct {
	ID             string       `json:"id"`
	Name           string       `json:"name"`
	Provider       string       `json:"provider"`
	URL            string       `json:"url,omitempty"`
	Username       string       `json:"username,omitempty"`
	HasPassword    bool         `json:"has_password"`
	TokenExpiresAt sql.NullTime `json:"token_expires_at,omitempty"`
	OAuthUser      string       `json:"oauth_user,omitempty"`
	CreatedAt      time.Time    `json:"created_at"`
	UpdatedAt      time.Time    `json:"updated_at"`
}

func (p *ConnectionProfile) ToPublic() ConnectionProfilePublic {
	return ConnectionProfilePublic{
		ID:             p.ID,
		Name:           p.Name,
		Provider:       p.Provider,
		URL:            p.URL,
		Username:       p.Username,
		HasPassword:    p.PasswordEncrypted != "",
		TokenExpiresAt: p.TokenExpiresAt,
		OAuthUser:      p.OAuthUser,
		CreatedAt:      p.CreatedAt,
		UpdatedAt:      p.UpdatedAt,
	}
}

func CreateConnectionProfile(database *sql.DB, p *ConnectionProfile) (string, error) {
	query := `
		INSERT INTO connection_profiles (
			user_id, name, provider, url, username,
			password_encrypted, refresh_token_encrypted, token_expires_at, oauth_user, mega_session_id_encrypted, mega_master_key_encrypted
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
		RETURNING id, created_at, updated_at
	`
	err := database.QueryRow(
		query,
		p.UserID, p.Name, p.Provider, p.URL, p.Username,
		p.PasswordEncrypted, p.RefreshTokenEncrypted, p.TokenExpiresAt, p.OAuthUser, p.MegaSessionIDEncrypted, p.MegaMasterKeyEncrypted,
	).Scan(&p.ID, &p.CreatedAt, &p.UpdatedAt)
	if err != nil {
		return "", err
	}
	return p.ID, nil
}

func GetConnectionProfile(ctx context.Context, database *sql.DB, id string) (*ConnectionProfile, error) {
	query := `
		SELECT id, user_id, name, provider, url, username,
		       password_encrypted, refresh_token_encrypted, token_expires_at, oauth_user, mega_session_id_encrypted, mega_master_key_encrypted,
		       created_at, updated_at
		FROM connection_profiles WHERE id = $1
	`
	var p ConnectionProfile
	err := database.QueryRowContext(ctx, query, id).Scan(
		&p.ID, &p.UserID, &p.Name, &p.Provider, &p.URL, &p.Username,
		&p.PasswordEncrypted, &p.RefreshTokenEncrypted, &p.TokenExpiresAt, &p.OAuthUser, &p.MegaSessionIDEncrypted, &p.MegaMasterKeyEncrypted,
		&p.CreatedAt, &p.UpdatedAt,
	)
	if err != nil {
		return nil, err
	}
	return &p, nil
}

// GetConnectionProfiles lists a user's profiles. The retained ignored argument
// preserves the established call signature while callers migrate from the
// removed provider-filter API.
func GetConnectionProfiles(ctx context.Context, database *sql.DB, userID, _ string) ([]ConnectionProfile, error) {
	args := []interface{}{userID}
	query := `
		SELECT id, user_id, name, provider, url, username,
		       password_encrypted, refresh_token_encrypted, token_expires_at, oauth_user, mega_session_id_encrypted, mega_master_key_encrypted,
		       created_at, updated_at
		FROM connection_profiles
		WHERE user_id = $1
		ORDER BY name ASC
	`
	rows, err := database.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var profiles []ConnectionProfile
	for rows.Next() {
		var p ConnectionProfile
		if err := rows.Scan(
			&p.ID, &p.UserID, &p.Name, &p.Provider, &p.URL, &p.Username,
			&p.PasswordEncrypted, &p.RefreshTokenEncrypted, &p.TokenExpiresAt, &p.OAuthUser, &p.MegaSessionIDEncrypted, &p.MegaMasterKeyEncrypted,
			&p.CreatedAt, &p.UpdatedAt,
		); err != nil {
			return nil, err
		}
		profiles = append(profiles, p)
	}
	return profiles, rows.Err()
}

type UpdateConnectionProfileInput struct {
	Name                   *string
	Provider               *string
	URL                    *string
	Username               *string
	PasswordEncrypted      *string
	RefreshTokenEncrypted  *string
	TokenExpiresAt         *time.Time
	OAuthUser              *string
	MegaSessionIDEncrypted *string
	MegaMasterKeyEncrypted *string
}

func UpdateConnectionProfile(database *sql.DB, id string, in UpdateConnectionProfileInput) error {
	setClauses := []string{}
	args := []interface{}{}
	idx := 1

	if in.Name != nil {
		idx++
		setClauses = append(setClauses, fmt.Sprintf("name = $%d", idx))
		args = append(args, *in.Name)
	}
	if in.Provider != nil {
		idx++
		setClauses = append(setClauses, fmt.Sprintf("provider = $%d", idx))
		args = append(args, *in.Provider)
	}
	if in.URL != nil {
		idx++
		setClauses = append(setClauses, fmt.Sprintf("url = $%d", idx))
		args = append(args, *in.URL)
	}
	if in.Username != nil {
		idx++
		setClauses = append(setClauses, fmt.Sprintf("username = $%d", idx))
		args = append(args, *in.Username)
	}
	if in.PasswordEncrypted != nil {
		idx++
		setClauses = append(setClauses, fmt.Sprintf("password_encrypted = $%d", idx))
		args = append(args, *in.PasswordEncrypted)
	}
	if in.RefreshTokenEncrypted != nil {
		idx++
		setClauses = append(setClauses, fmt.Sprintf("refresh_token_encrypted = $%d", idx))
		args = append(args, *in.RefreshTokenEncrypted)
	}
	if in.TokenExpiresAt != nil {
		idx++
		setClauses = append(setClauses, fmt.Sprintf("token_expires_at = $%d", idx))
		args = append(args, *in.TokenExpiresAt)
	}
	if in.OAuthUser != nil {
		idx++
		setClauses = append(setClauses, fmt.Sprintf("oauth_user = $%d", idx))
		args = append(args, *in.OAuthUser)
	}
	if in.MegaSessionIDEncrypted != nil {
		idx++
		setClauses = append(setClauses, fmt.Sprintf("mega_session_id_encrypted = $%d", idx))
		args = append(args, *in.MegaSessionIDEncrypted)
	}
	if in.MegaMasterKeyEncrypted != nil {
		idx++
		setClauses = append(setClauses, fmt.Sprintf("mega_master_key_encrypted = $%d", idx))
		args = append(args, *in.MegaMasterKeyEncrypted)
	}

	if len(setClauses) == 0 {
		return nil
	}

	// $1 remains the profile ID; dynamic SET values start at $2 above.
	query := `UPDATE connection_profiles SET ` + strings.Join(setClauses, ", ") + ` WHERE id = $1`
	args = append([]interface{}{id}, args...)
	_, err := database.Exec(query, args...)
	return err
}

func UpdateConnectionProfileMegaSession(ctx context.Context, database *sql.DB, id, sessionIDEncrypted, masterKeyEncrypted, expectedSessionIDEncrypted string) error {
	result, err := database.ExecContext(ctx, `UPDATE connection_profiles SET mega_session_id_encrypted = $1, mega_master_key_encrypted = $2, updated_at = CURRENT_TIMESTAMP WHERE id = $3 AND COALESCE(mega_session_id_encrypted, '') = $4`, sessionIDEncrypted, masterKeyEncrypted, id, expectedSessionIDEncrypted)
	if err != nil {
		return err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		return ErrOAuthTokenConflict
	}
	return nil
}

// UpdateConnectionProfileOAuthTokens atomically persists a refreshed OAuth
// credential set if no concurrent rotation has already replaced its refresh token.
func UpdateConnectionProfileOAuthTokens(database *sql.DB, id, accessTokenEncrypted, refreshTokenEncrypted string, expiresAt time.Time, expectedRefreshTokenEncrypted string) error {
	if expectedRefreshTokenEncrypted == "" {
		return ErrOAuthTokenConflict
	}
	res, err := database.Exec(`
		UPDATE connection_profiles
		SET password_encrypted = $1,
		    refresh_token_encrypted = $2,
		    token_expires_at = $3,
		    updated_at = CURRENT_TIMESTAMP
		WHERE id = $4
		  AND refresh_token_encrypted = $5
	`, accessTokenEncrypted, refreshTokenEncrypted, expiresAt, id, expectedRefreshTokenEncrypted)
	if err != nil {
		return err
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		return ErrOAuthTokenConflict
	}
	return nil
}

func DeleteConnectionProfile(database *sql.DB, id string) error {
	_, err := database.Exec(`DELETE FROM connection_profiles WHERE id = $1`, id)
	return err
}

func VerifyProfileOwnership(database *sql.DB, profileID, userID string) (bool, error) {
	query := `SELECT EXISTS(SELECT 1 FROM connection_profiles WHERE id = $1 AND user_id = $2)`
	var exists bool
	err := database.QueryRow(query, profileID, userID).Scan(&exists)
	return exists, err
}
