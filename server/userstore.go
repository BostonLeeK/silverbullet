package server

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	_ "modernc.org/sqlite"

	"golang.org/x/crypto/bcrypt"
)

const (
	RoleAdmin = "admin"
	RoleUser  = "user"
)

const (
	PermReader = "reader"
	PermWriter = "writer"
	PermOwner  = "owner"
)

var (
	ErrUserNotFound      = errors.New("user not found")
	ErrUserExists        = errors.New("user already exists")
	ErrInvalidRole       = errors.New("invalid role")
	ErrInvalidPermission = errors.New("invalid permission")
	ErrInvalidCreds      = errors.New("invalid credentials")
)

type User struct {
	ID        int64  `json:"id"`
	Email     string `json:"email"`
	Role      string `json:"role"`
	CreatedAt int64  `json:"createdAt"`
}

type FolderACL struct {
	ID         int64  `json:"id"`
	FolderPath string `json:"folderPath"`
	UserID     int64  `json:"userId"`
	UserEmail  string `json:"userEmail,omitempty"`
	Permission string `json:"permission"`
}

type UserStore struct {
	db *sql.DB
}

func OpenUserStore(path string) (*UserStore, error) {
	db, err := sql.Open("sqlite", path+"?_pragma=foreign_keys(1)&_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)")
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	db.SetMaxOpenConns(1) // sqlite is single-writer; avoid "database is locked"
	s := &UserStore{db: db}
	if err := s.migrate(); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

func (s *UserStore) Close() error { return s.db.Close() }

func (s *UserStore) migrate() error {
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS users (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			email TEXT NOT NULL UNIQUE COLLATE NOCASE,
			password_hash TEXT NOT NULL,
			role TEXT NOT NULL DEFAULT 'user',
			created_at INTEGER NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS folder_acls (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			folder_path TEXT NOT NULL,
			user_id INTEGER NOT NULL,
			permission TEXT NOT NULL,
			UNIQUE(folder_path, user_id),
			FOREIGN KEY(user_id) REFERENCES users(id) ON DELETE CASCADE
		)`,
		`CREATE INDEX IF NOT EXISTS idx_acl_user ON folder_acls(user_id)`,
		`CREATE INDEX IF NOT EXISTS idx_acl_folder ON folder_acls(folder_path)`,
	}
	for _, q := range stmts {
		if _, err := s.db.Exec(q); err != nil {
			return fmt.Errorf("migrate: %w", err)
		}
	}
	return nil
}

func normalizeEmail(email string) string {
	return strings.ToLower(strings.TrimSpace(email))
}

func validRole(role string) bool {
	return role == RoleAdmin || role == RoleUser
}

func validPermission(p string) bool {
	return p == PermReader || p == PermWriter || p == PermOwner
}

func (s *UserStore) HasAnyUser() (bool, error) {
	var count int
	if err := s.db.QueryRow("SELECT COUNT(*) FROM users").Scan(&count); err != nil {
		return false, err
	}
	return count > 0, nil
}

func (s *UserStore) CreateUser(email, password, role string) (*User, error) {
	email = normalizeEmail(email)
	if email == "" || password == "" {
		return nil, errors.New("email and password are required")
	}
	if !validRole(role) {
		return nil, ErrInvalidRole
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return nil, err
	}
	now := time.Now().Unix()
	res, err := s.db.Exec(
		`INSERT INTO users(email, password_hash, role, created_at) VALUES (?,?,?,?)`,
		email, string(hash), role, now)
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			return nil, ErrUserExists
		}
		return nil, err
	}
	id, _ := res.LastInsertId()
	return &User{ID: id, Email: email, Role: role, CreatedAt: now}, nil
}

func (s *UserStore) VerifyPassword(email, password string) (*User, error) {
	email = normalizeEmail(email)
	var u User
	var hash string
	err := s.db.QueryRow(
		`SELECT id, email, password_hash, role, created_at FROM users WHERE email = ?`, email,
	).Scan(&u.ID, &u.Email, &hash, &u.Role, &u.CreatedAt)
	if err == sql.ErrNoRows {
		return nil, ErrInvalidCreds
	}
	if err != nil {
		return nil, err
	}
	if err := bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)); err != nil {
		return nil, ErrInvalidCreds
	}
	return &u, nil
}

func (s *UserStore) GetUser(id int64) (*User, error) {
	var u User
	err := s.db.QueryRow(
		`SELECT id, email, role, created_at FROM users WHERE id = ?`, id,
	).Scan(&u.ID, &u.Email, &u.Role, &u.CreatedAt)
	if err == sql.ErrNoRows {
		return nil, ErrUserNotFound
	}
	if err != nil {
		return nil, err
	}
	return &u, nil
}

func (s *UserStore) ListUsers() ([]User, error) {
	rows, err := s.db.Query(`SELECT id, email, role, created_at FROM users ORDER BY id ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []User
	for rows.Next() {
		var u User
		if err := rows.Scan(&u.ID, &u.Email, &u.Role, &u.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

func (s *UserStore) UpdatePassword(id int64, password string) error {
	if password == "" {
		return errors.New("password required")
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return err
	}
	res, err := s.db.Exec(`UPDATE users SET password_hash = ? WHERE id = ?`, string(hash), id)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrUserNotFound
	}
	return nil
}

func (s *UserStore) UpdateRole(id int64, role string) error {
	if !validRole(role) {
		return ErrInvalidRole
	}
	res, err := s.db.Exec(`UPDATE users SET role = ? WHERE id = ?`, role, id)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrUserNotFound
	}
	return nil
}

func (s *UserStore) DeleteUser(id int64) error {
	res, err := s.db.Exec(`DELETE FROM users WHERE id = ?`, id)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrUserNotFound
	}
	return nil
}

func (s *UserStore) CountAdmins() (int, error) {
	var c int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM users WHERE role = ?`, RoleAdmin).Scan(&c)
	return c, err
}

// --- Folder ACL ---

// normalizeFolder returns a canonical folder path: no leading slash, no trailing slash.
// Empty string means the space root.
func normalizeFolder(folder string) string {
	folder = strings.TrimSpace(folder)
	folder = strings.Trim(folder, "/")
	return folder
}

func (s *UserStore) SetFolderACL(folder string, userID int64, permission string) error {
	if !validPermission(permission) {
		return ErrInvalidPermission
	}
	folder = normalizeFolder(folder)
	_, err := s.db.Exec(`
		INSERT INTO folder_acls(folder_path, user_id, permission) VALUES (?,?,?)
		ON CONFLICT(folder_path, user_id) DO UPDATE SET permission = excluded.permission`,
		folder, userID, permission)
	return err
}

func (s *UserStore) RemoveFolderACL(folder string, userID int64) error {
	folder = normalizeFolder(folder)
	_, err := s.db.Exec(`DELETE FROM folder_acls WHERE folder_path = ? AND user_id = ?`, folder, userID)
	return err
}

func (s *UserStore) ListFolderACLs(folder string) ([]FolderACL, error) {
	folder = normalizeFolder(folder)
	rows, err := s.db.Query(`
		SELECT a.id, a.folder_path, a.user_id, u.email, a.permission
		FROM folder_acls a JOIN users u ON u.id = a.user_id
		WHERE a.folder_path = ? ORDER BY u.email`, folder)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []FolderACL
	for rows.Next() {
		var a FolderACL
		if err := rows.Scan(&a.ID, &a.FolderPath, &a.UserID, &a.UserEmail, &a.Permission); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

func (s *UserStore) ListAllACLs() ([]FolderACL, error) {
	rows, err := s.db.Query(`
		SELECT a.id, a.folder_path, a.user_id, u.email, a.permission
		FROM folder_acls a JOIN users u ON u.id = a.user_id
		ORDER BY a.folder_path, u.email`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []FolderACL
	for rows.Next() {
		var a FolderACL
		if err := rows.Scan(&a.ID, &a.FolderPath, &a.UserID, &a.UserEmail, &a.Permission); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// userFolderACLs returns permission-by-folder for a user (keys normalized, "" = root).
func (s *UserStore) userFolderACLs(userID int64) (map[string]string, error) {
	rows, err := s.db.Query(`SELECT folder_path, permission FROM folder_acls WHERE user_id = ?`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var folder, perm string
		if err := rows.Scan(&folder, &perm); err != nil {
			return nil, err
		}
		out[folder] = perm
	}
	return out, rows.Err()
}

// permRank maps a permission string to comparable strength.
// owner > writer > reader > none (0).
func permRank(p string) int {
	switch p {
	case PermOwner:
		return 3
	case PermWriter:
		return 2
	case PermReader:
		return 1
	default:
		return 0
	}
}

// EffectivePermission computes the user's effective permission on a given path,
// walking up the folder hierarchy. Admins always get owner-level access.
// An empty/absent ACL on the root ("") implies no access for non-admins.
func (s *UserStore) EffectivePermission(user *User, path string) (string, error) {
	if user == nil {
		return "", nil
	}
	if user.Role == RoleAdmin {
		return PermOwner, nil
	}
	acls, err := s.userFolderACLs(user.ID)
	if err != nil {
		return "", err
	}
	// Take max rank across all ancestor folders including root ("")
	best := 0
	path = normalizeFolder(path)
	// file's parent folder is path without trailing filename; we check every ancestor of the
	// *file location*. For a file "a/b/c.md" the candidates are "a/b", "a", "".
	// For a folder "a/b" candidates are "a/b", "a", "".
	candidates := []string{path}
	for {
		idx := strings.LastIndex(path, "/")
		if idx < 0 {
			break
		}
		path = path[:idx]
		candidates = append(candidates, path)
	}
	candidates = append(candidates, "") // root fallback
	for _, c := range candidates {
		if p, ok := acls[c]; ok {
			if r := permRank(p); r > best {
				best = r
			}
		}
	}
	switch best {
	case 3:
		return PermOwner, nil
	case 2:
		return PermWriter, nil
	case 1:
		return PermReader, nil
	}
	return "", nil
}

// CanRead returns true if the user has at least reader permission for path.
func (s *UserStore) CanRead(user *User, path string) (bool, error) {
	p, err := s.EffectivePermission(user, path)
	if err != nil {
		return false, err
	}
	return permRank(p) >= permRank(PermReader), nil
}

// CanWrite returns true if the user has writer or owner permission for path.
func (s *UserStore) CanWrite(user *User, path string) (bool, error) {
	p, err := s.EffectivePermission(user, path)
	if err != nil {
		return false, err
	}
	return permRank(p) >= permRank(PermWriter), nil
}
