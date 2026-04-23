package server

import (
	"errors"
	"testing"
	"testing/fstest"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestFSSpacePrimitivesWithMapFS(t *testing.T) {
	// Test with a MapFS that has embedded files
	mapFS := fstest.MapFS{
		"readme.txt":  &fstest.MapFile{Data: []byte("Hello World")},
		"config.json": &fstest.MapFile{Data: []byte(`{"test": true}`)},
	}

	// Note: t.TempDir() automatically deletes the folder after the tests runs
	fallback, err := NewDiskSpacePrimitives(t.TempDir(), "")
	assert.NoError(t, err, "Failed to create fallback")

	primitives := NewReadOnlyFallthroughSpacePrimitives(mapFS, "", time.Now(), fallback)

	// Test reading embedded files
	data, meta, err := primitives.ReadFile("readme.txt")
	assert.NoError(t, err, "Should read embedded file")
	assert.Equal(t, []byte("Hello World"), data, "Content should match")
	assert.Equal(t, "readme.txt", meta.Name, "Name should match")
	assert.Equal(t, "ro", meta.Perm, "Embedded files should be read-only")

	// Test that writing to embedded file names fails
	_, err = primitives.WriteFile("readme.txt", []byte("new content"), nil)
	assert.Error(t, err, "Should not overwrite embedded files")
	assert.True(t, errors.Is(err, ErrReadOnlySpacePath))

	// Test that deleting embedded files fails
	err = primitives.DeleteFile("config.json")
	assert.Error(t, err, "Should not delete embedded files")
	assert.True(t, errors.Is(err, ErrReadOnlySpacePath))

	// Test writing to non-embedded file names (should work via fallback)
	_, err = primitives.WriteFile("new_file.txt", []byte("fallback content"), nil)
	assert.NoError(t, err, "Should write to fallback for non-embedded files")

	// Test that fallback file can be read back
	data, meta, err = primitives.ReadFile("new_file.txt")
	assert.NoError(t, err, "Should read fallback file")
	assert.Equal(t, []byte("fallback content"), data, "Fallback content should match")
	assert.Equal(t, "new_file.txt", meta.Name, "Fallback file name should match")
}

func TestEmbedLegacyLibrariesPrefixLayout(t *testing.T) {
	mapFS := fstest.MapFS{
		"base_fs/libraries/Library/Std/APIs/Action Button.md": &fstest.MapFile{Data: []byte("embedded")},
	}
	fallback, err := NewDiskSpacePrimitives(t.TempDir(), "")
	assert.NoError(t, err)
	primitives := NewReadOnlyFallthroughSpacePrimitives(mapFS, "base_fs", time.Now(), fallback)
	const logical = "Library/Std/APIs/Action Button.md"
	data, meta, err := primitives.ReadFile(logical)
	assert.NoError(t, err)
	assert.Equal(t, []byte("embedded"), data)
	assert.Equal(t, logical, meta.Name)
	_, err = primitives.WriteFile(logical, []byte("x"), nil)
	assert.Error(t, err)
	assert.True(t, errors.Is(err, ErrReadOnlySpacePath))
	err = primitives.DeleteFile(logical)
	assert.Error(t, err)
	assert.True(t, errors.Is(err, ErrReadOnlySpacePath))
	meta2, err := primitives.GetFileMeta(logical)
	assert.NoError(t, err)
	assert.Equal(t, logical, meta2.Name)
	assert.Equal(t, int64(len("embedded")), meta2.Size)
}

func TestEmbedPrimaryLayoutStillWorks(t *testing.T) {
	mapFS := fstest.MapFS{
		"base_fs/Library/Std/Foo.md": &fstest.MapFile{Data: []byte("a")},
	}
	fallback, err := NewDiskSpacePrimitives(t.TempDir(), "")
	assert.NoError(t, err)
	primitives := NewReadOnlyFallthroughSpacePrimitives(mapFS, "base_fs", time.Now(), fallback)
	data, _, err := primitives.ReadFile("Library/Std/Foo.md")
	assert.NoError(t, err)
	assert.Equal(t, []byte("a"), data)
}

func TestEmbedSpacePathAlreadyUnderLibraries(t *testing.T) {
	mapFS := fstest.MapFS{
		"base_fs/libraries/Library/X.md": &fstest.MapFile{Data: []byte("z")},
	}
	fallback, err := NewDiskSpacePrimitives(t.TempDir(), "")
	assert.NoError(t, err)
	primitives := NewReadOnlyFallthroughSpacePrimitives(mapFS, "base_fs", time.Now(), fallback)
	data, _, err := primitives.ReadFile("libraries/Library/X.md")
	assert.NoError(t, err)
	assert.Equal(t, []byte("z"), data)
}
