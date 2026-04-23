package server

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func openTestStore(t *testing.T) *UserStore {
	t.Helper()
	path := filepath.Join(t.TempDir(), "users.db")
	s, err := OpenUserStore(path)
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestUserStore_CreateAndVerify(t *testing.T) {
	s := openTestStore(t)

	ok, err := s.HasAnyUser()
	require.NoError(t, err)
	assert.False(t, ok)

	u, err := s.CreateUser("Alice@example.com", "password123", RoleAdmin)
	require.NoError(t, err)
	assert.Equal(t, "alice@example.com", u.Email)
	assert.Equal(t, RoleAdmin, u.Role)

	// Duplicate email (case-insensitive) fails
	_, err = s.CreateUser("ALICE@example.com", "other", RoleUser)
	assert.ErrorIs(t, err, ErrUserExists)

	// Correct password verifies
	got, err := s.VerifyPassword("alice@example.com", "password123")
	require.NoError(t, err)
	assert.Equal(t, u.ID, got.ID)

	// Wrong password rejected
	_, err = s.VerifyPassword("alice@example.com", "wrong")
	assert.ErrorIs(t, err, ErrInvalidCreds)
}

func TestUserStore_CountAdmins(t *testing.T) {
	s := openTestStore(t)
	_, err := s.CreateUser("a@x", "password1", RoleAdmin)
	require.NoError(t, err)
	_, err = s.CreateUser("b@x", "password2", RoleUser)
	require.NoError(t, err)
	c, err := s.CountAdmins()
	require.NoError(t, err)
	assert.Equal(t, 1, c)
}

func TestACL_EffectivePermission(t *testing.T) {
	s := openTestStore(t)
	admin, err := s.CreateUser("admin@x", "password1", RoleAdmin)
	require.NoError(t, err)
	user, err := s.CreateUser("user@x", "password1", RoleUser)
	require.NoError(t, err)

	// Admin always gets owner
	p, err := s.EffectivePermission(admin, "any/path.md")
	require.NoError(t, err)
	assert.Equal(t, PermOwner, p)

	// User with no ACL has no access
	p, err = s.EffectivePermission(user, "any/path.md")
	require.NoError(t, err)
	assert.Equal(t, "", p)

	// Grant reader on root
	require.NoError(t, s.SetFolderACL("", user.ID, PermReader))
	p, err = s.EffectivePermission(user, "a/b/c.md")
	require.NoError(t, err)
	assert.Equal(t, PermReader, p)

	// Grant writer on a specific subfolder; it should win over root reader
	require.NoError(t, s.SetFolderACL("a/b", user.ID, PermWriter))
	p, err = s.EffectivePermission(user, "a/b/c.md")
	require.NoError(t, err)
	assert.Equal(t, PermWriter, p)

	// Sibling folder still only has root reader
	p, err = s.EffectivePermission(user, "a/x/y.md")
	require.NoError(t, err)
	assert.Equal(t, PermReader, p)

	// Remove writer ACL -> back to reader
	require.NoError(t, s.RemoveFolderACL("a/b", user.ID))
	p, err = s.EffectivePermission(user, "a/b/c.md")
	require.NoError(t, err)
	assert.Equal(t, PermReader, p)
}

func TestACL_CanReadWrite(t *testing.T) {
	s := openTestStore(t)
	u, err := s.CreateUser("u@x", "password1", RoleUser)
	require.NoError(t, err)

	canR, err := s.CanRead(u, "docs/x.md")
	require.NoError(t, err)
	assert.False(t, canR)

	require.NoError(t, s.SetFolderACL("docs", u.ID, PermReader))
	canR, _ = s.CanRead(u, "docs/x.md")
	assert.True(t, canR)
	canW, _ := s.CanWrite(u, "docs/x.md")
	assert.False(t, canW)

	require.NoError(t, s.SetFolderACL("docs", u.ID, PermWriter))
	canW, _ = s.CanWrite(u, "docs/x.md")
	assert.True(t, canW)
}
