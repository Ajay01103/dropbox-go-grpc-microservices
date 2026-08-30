package repository

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/gocql/gocql"
	"github.com/google/uuid"
)

// User represents a user record
type User struct {
	ID        string    `db:"id"`
	Email     string    `db:"email"`
	Name      string    `db:"name"`
	Password  string    `db:"password"`
	CreatedAt time.Time `db:"created_at"`
	UpdatedAt time.Time `db:"updated_at"`
}

// ErrUserNotFound is returned when a user lookup yields no result.
var ErrUserNotFound = errors.New("user not found")

// ErrEmailTaken is returned when registering with an already-used email.
var ErrEmailTaken = errors.New("email already in use")

// UserRepo provides data access for users using ScyllaDB
type UserRepo struct {
	session *gocql.Session
}

// NewUserRepo creates a UserRepo backed by a ScyllaDB session
func NewUserRepo(session *gocql.Session) *UserRepo {
	return &UserRepo{session: session}
}

// CreateUser inserts a new user record. Detects unique-constraint violations.
func (r *UserRepo) CreateUser(ctx context.Context, email, name, hashedPassword string) (User, error) {
	userID := uuid.New().String()
	now := time.Now().UTC()

	var reservedEmail string
	var existingUserID string
	applied, err := r.session.Query(
		`INSERT INTO users_by_email (email, user_id) VALUES (?, ?) IF NOT EXISTS`,
		email, userID,
	).WithContext(ctx).ScanCAS(&reservedEmail, &existingUserID)
	if err != nil {
		return User{}, fmt.Errorf("reserve email: %w", err)
	}
	if !applied {
		return User{}, ErrEmailTaken
	}

	// Insert the new user
	if err := r.session.Query(
		`INSERT INTO users (id, email, name, password, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?)`,
		userID, email, name, hashedPassword, now, now,
	).WithContext(ctx).Exec(); err != nil {
		_, _ = r.session.Query(
			`DELETE FROM users_by_email WHERE email = ? IF user_id = ?`,
			email, userID,
		).WithContext(ctx).ScanCAS()
		return User{}, fmt.Errorf("insert user: %w", err)
	}

	return User{
		ID:        userID,
		Email:     email,
		Name:      name,
		Password:  hashedPassword,
		CreatedAt: now,
		UpdatedAt: now,
	}, nil
}

// GetByEmail fetches a user by email using the materialized view for optimal performance
func (r *UserRepo) GetByEmail(ctx context.Context, email string) (User, error) {
	var user User
	// Use materialized view users_by_email_mv for single-query lookup
	// Instead of: 2 queries (users_by_email -> users)
	err := r.session.Query(
		`SELECT id, email, name, password, created_at, updated_at
		FROM users_by_email_mv WHERE email = ? LIMIT 1`,
		email,
	).WithContext(ctx).Scan(
		&user.ID, &user.Email, &user.Name, &user.Password,
		&user.CreatedAt, &user.UpdatedAt,
	)
	if err == gocql.ErrNotFound {
		return User{}, ErrUserNotFound
	}
	if err != nil {
		return User{}, fmt.Errorf("get user by email: %w", err)
	}
	return user, nil
}

// GetByID fetches a user by UUID
func (r *UserRepo) GetByID(ctx context.Context, id uuid.UUID) (User, error) {
	var user User
	err := r.session.Query(
		`SELECT id, email, name, password, created_at, updated_at
		FROM users WHERE id = ? LIMIT 1`,
		id.String(),
	).WithContext(ctx).Scan(
		&user.ID, &user.Email, &user.Name, &user.Password,
		&user.CreatedAt, &user.UpdatedAt,
	)
	if err == gocql.ErrNotFound {
		return User{}, ErrUserNotFound
	}
	if err != nil {
		return User{}, fmt.Errorf("get user by id: %w", err)
	}
	return user, nil
}
