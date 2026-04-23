package server

import (
	"fmt"
	"io/fs"
	"log"
	pathLib "path"
	"strings"
	"time"
)

// Implements a simple read-only fallthrough implementation of SpacePrimitives, used to serve static files embedde in the Go binary
type ReadOnlyFallthroughSpacePrimitives struct {
	fsys                       fs.FS
	fallthroughSpacePrimitives SpacePrimitives
	rootPath                   string    // Root path within the fs.FS
	timeStamp                  time.Time // Fake timestamp to use for all files
}

var _ SpacePrimitives = &ReadOnlyFallthroughSpacePrimitives{}

func NewReadOnlyFallthroughSpacePrimitives(fsys fs.FS, rootPath string, timeStamp time.Time, wrapped SpacePrimitives) *ReadOnlyFallthroughSpacePrimitives {
	return &ReadOnlyFallthroughSpacePrimitives{
		fsys:                       fsys,
		fallthroughSpacePrimitives: wrapped,
		rootPath:                   rootPath,
		timeStamp:                  timeStamp,
	}
}

func (e *ReadOnlyFallthroughSpacePrimitives) pathToEmbedPath(path string) string {
	return pathLib.Join(e.rootPath, path)
}

func normalizeEmbedSpacePath(spacePath string) string {
	p := strings.ReplaceAll(spacePath, "\\", "/")
	return strings.TrimPrefix(p, "/")
}

func (e *ReadOnlyFallthroughSpacePrimitives) candidateEmbedFSPaths(spacePath string) []string {
	spacePath = normalizeEmbedSpacePath(spacePath)
	primary := pathLib.Join(e.rootPath, spacePath)
	out := []string{primary}
	if spacePath != "" && !strings.HasPrefix(spacePath, "libraries/") {
		out = append(out, pathLib.Join(e.rootPath, "libraries", spacePath))
	}
	return out
}

func (e *ReadOnlyFallthroughSpacePrimitives) statEmbedFile(spacePath string) (fs.FileInfo, error) {
	for _, ep := range e.candidateEmbedFSPaths(spacePath) {
		info, err := fs.Stat(e.fsys, ep)
		if err == nil && !info.IsDir() {
			return info, nil
		}
	}
	return nil, fs.ErrNotExist
}

func (e *ReadOnlyFallthroughSpacePrimitives) readEmbedFile(spacePath string) ([]byte, fs.FileInfo, error) {
	for _, ep := range e.candidateEmbedFSPaths(spacePath) {
		data, err := fs.ReadFile(e.fsys, ep)
		if err == nil {
			info, statErr := fs.Stat(e.fsys, ep)
			if statErr != nil {
				return nil, nil, statErr
			}
			if info.IsDir() {
				continue
			}
			return data, info, nil
		}
	}
	return nil, nil, fs.ErrNotExist
}

// Inverse of pathToEmbedPath
func (e *ReadOnlyFallthroughSpacePrimitives) embedPathToPath(path string) string {
	return strings.TrimPrefix(path, e.rootPath+"/")
}

// fileInfoToFileMeta converts fs.FileInfo to FileMeta for fs.FS files
func (e *ReadOnlyFallthroughSpacePrimitives) fileInfoToFileMeta(path string, info fs.FileInfo) FileMeta {
	return FileMeta{
		Name:         path,
		Size:         info.Size(),
		ContentType:  LookupContentTypeFromPath(path),
		Created:      e.timeStamp.UnixMilli(),
		LastModified: e.timeStamp.UnixMilli(),
		Perm:         "ro",
	}
}

// FetchFileList implements SpacePrimitives.FetchFileList
// Lists all files from the filesystem first, then combines files from the fallback.
func (e *ReadOnlyFallthroughSpacePrimitives) FetchFileList() ([]FileMeta, error) {
	allFiles := make([]FileMeta, 0, 1000)

	// First, collect files from the filesystem
	err := fs.WalkDir(e.fsys, e.rootPath, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			// Skip files that can't be accessed
			return nil
		}

		// Skip directories
		if entry.IsDir() {
			return nil
		}

		// Get file info
		info, err := entry.Info()
		if err != nil {
			// Skip files we can't stat
			return nil
		}

		// Convert to our path format (forward slashes)
		relativePath := e.embedPathToPath(path)
		fileMeta := e.fileInfoToFileMeta(relativePath, info)
		allFiles = append(allFiles, fileMeta)

		return nil
	})

	if err != nil {
		log.Printf("Something went wrong listing files in the FS: %v", err)
	}

	wrappedFiles, err := e.fallthroughSpacePrimitives.FetchFileList()
	if err != nil {
		return allFiles, err
	}

	allFiles = append(allFiles, wrappedFiles...)

	return allFiles, nil
}

// GetFileMeta implements SpacePrimitives.GetFileMeta
func (e *ReadOnlyFallthroughSpacePrimitives) GetFileMeta(path string) (FileMeta, error) {
	info, err := e.statEmbedFile(path)
	if err == nil {
		return e.fileInfoToFileMeta(path, info), nil
	}

	if e.fallthroughSpacePrimitives == nil {
		return FileMeta{}, ErrNotFound
	}

	// If not found in fs.FS, fall back
	return e.fallthroughSpacePrimitives.GetFileMeta(path)
}

// ReadFile implements SpacePrimitives.ReadFile
func (e *ReadOnlyFallthroughSpacePrimitives) ReadFile(path string) ([]byte, FileMeta, error) {
	data, info, err := e.readEmbedFile(path)
	if err == nil {
		return data, e.fileInfoToFileMeta(path, info), nil
	}

	if e.fallthroughSpacePrimitives == nil {
		return nil, FileMeta{}, ErrNotFound
	}

	// If not found in fs.FS, fall back
	return e.fallthroughSpacePrimitives.ReadFile(path)
}

// WriteFile implements SpacePrimitives.WriteFile
// Fails if file exists in filesystem, otherwise delegates to fallback
func (e *ReadOnlyFallthroughSpacePrimitives) WriteFile(path string, data []byte, meta *FileMeta) (FileMeta, error) {
	_, err := e.statEmbedFile(path)
	if err == nil {
		return FileMeta{}, fmt.Errorf("cannot write %q: %w", path, ErrReadOnlySpacePath)
	}
	if _, _, rerr := e.readEmbedFile(path); rerr == nil {
		return FileMeta{}, fmt.Errorf("cannot write %q: %w", path, ErrReadOnlySpacePath)
	}

	if e.fallthroughSpacePrimitives == nil {
		return FileMeta{}, ErrNotFound
	}

	return e.fallthroughSpacePrimitives.WriteFile(path, data, meta)
}

// DeleteFile implements SpacePrimitives.DeleteFile
// Fails if file exists in filesystem, otherwise delegates to fallback
func (e *ReadOnlyFallthroughSpacePrimitives) DeleteFile(path string) error {
	_, err := e.statEmbedFile(path)
	if err == nil {
		return fmt.Errorf("cannot delete %q: %w", path, ErrReadOnlySpacePath)
	}
	if _, _, rerr := e.readEmbedFile(path); rerr == nil {
		return fmt.Errorf("cannot delete %q: %w", path, ErrReadOnlySpacePath)
	}

	if e.fallthroughSpacePrimitives == nil {
		return ErrNotFound
	}

	return e.fallthroughSpacePrimitives.DeleteFile(path)
}
