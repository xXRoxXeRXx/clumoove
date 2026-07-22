package db

import (
	"database/sql"
	"fmt"
	"strings"
	"time"
)

type ConnectionProfile struct {
	ID                    string       `json:"id"`
	UserID                string       `json:"user_id"`
	Name                  string       `json:"name"`
	Provider              string       `json:"provider"`
	URL                   string       `json:"url,omitempty"`
	Username              string       `json:"username,omitempty"`
	PasswordEncrypted    string       `json:"-"`
	RefreshTokenEncrypted string       `json:"-"`
	TokenExpiresAt       sql.NullTime `json:"token_expires_at,omitempty"`
	OAuthUser            string       `json:"oauth_user,omitempty"`
	CreatedAt            time.Time    `json:"created_at"`
	UpdatedAt            time.Time    `json:"updated_at"`
}

type ConnectionProfilePublic struct {
	ID             string       `json:"id"`
	Name           string       `json:"name"`
	Provider       string       `json:"provider"`
	URL            string       `json:"url,omitempty"`
	Username       string       `json:"username,omitempty"`
	HasPassword    bool         `json:"has_password"`
	TokenExpiresAt sql.NullTime `json:"token_expires_at,omitempty"`
	OAuthUser     string       `json:"oauth_user,omitempty"`
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
		OAuthUser:     p.OAuthUser,
		CreatedAt:      p.CreatedAt,
		UpdatedAt:      p.UpdatedAt,
	}
}

func CreateConnectionProfile(database *sql.DB, p *ConnectionProfile) (string, error) {
	query := `
		INSERT INTO connection_profiles (
			user_id, name, provider, url, username,
			password_encrypted, refresh_token_encrypted, token_expires_at, oauth_user
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
		RETURNING id, created_at, updated_at
	`
	err := database.QueryRow(
		query,
		p.UserID, p.Name, p.Provider, p.URL, p.Username,
		p.PasswordEncrypted, p.RefreshTokenEncrypted, p.TokenExpiresAt, p.OAuthUser,
	).Scan(&p.ID, &p.CreatedAt, &p.UpdatedAt)
	if err != nil {
		return "", err
	}
	return p.ID, nil
}

func GetConnectionProfile(database *sql.DB, id string) (*ConnectionProfile, error) {
	query := `
		SELECT id, user_id, name, provider, url, username,
		       password_encrypted, refresh_token_encrypted, token_expires_at, oauth_user,
		       created_at, updated_at
		FROM connection_profiles WHERE id = $1
	`
	var p ConnectionProfile
	err := database.QueryRow(query, id).Scan(
		&p.ID, &p.UserID, &p.Name, &p.Provider, &p.URL, &p.Username,
		&p.PasswordEncrypted, &p.RefreshTokenEncrypted, &p.TokenExpiresAt, &p.OAuthUser,
		&p.CreatedAt, &p.UpdatedAt,
	)
	if err != nil {
		return nil, err
	}
	return &p, nil
}

func GetConnectionProfiles(database *sql.DB, userID, _ string) ([]ConnectionProfile, error) {
	args := []interface{}{userID}
	query := `
		SELECT id, user_id, name, provider, url, username,
		       password_encrypted, refresh_token_encrypted, token_expires_at, oauth_user,
		       created_at, updated_at
		FROM connection_profiles
		WHERE user_id = $1
		ORDER BY name ASC
	`
	rows, err := database.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var profiles []ConnectionProfile
	for rows.Next() {
		var p ConnectionProfile
		if err := rows.Scan(
			&p.ID, &p.UserID, &p.Name, &p.Provider, &p.URL, &p.Username,
			&p.PasswordEncrypted, &p.RefreshTokenEncrypted, &p.TokenExpiresAt, &p.OAuthUser,
			&p.CreatedAt, &p.UpdatedAt,
		); err != nil {
			return nil, err
		}
		profiles = append(profiles, p)
	}
	return profiles, rows.Err()
}

type UpdateConnectionProfileInput struct {
	Name                  *string
	Provider              *string
	URL                   *string
	Username              *string
	PasswordEncrypted     *string
	RefreshTokenEncrypted *string
	TokenExpiresAt        *time.Time
	OAuthUser             *string
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

	if len(setClauses) == 0 {
		return nil
	}

	query := `UPDATE connection_profiles SET ` + strings.Join(setClauses, ", ") + ` WHERE id = $1`
	args = append([]interface{}{id}, args...)
	_, err := database.Exec(query, args...)
	return err
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
