package server

import "fmt"

type ReadOnlySpacePrimitives struct {
	wrapped SpacePrimitives
}

var _ SpacePrimitives = &ReadOnlySpacePrimitives{}

func NewReadOnlySpacePrimitives(wrapped SpacePrimitives) *ReadOnlySpacePrimitives {
	return &ReadOnlySpacePrimitives{wrapped: wrapped}
}

// FetchFileList retrieves a list of all files in the space
func (ro *ReadOnlySpacePrimitives) FetchFileList() ([]FileMeta, error) {
	return ro.wrapped.FetchFileList()
}

// GetFileMeta retrieves metadata for a specific file
func (ro *ReadOnlySpacePrimitives) GetFileMeta(path string) (FileMeta, error) {
	return ro.wrapped.GetFileMeta(path)
}

// ReadFile reads a file and returns its data and metadata
func (ro *ReadOnlySpacePrimitives) ReadFile(path string) ([]byte, FileMeta, error) {
	return ro.wrapped.ReadFile(path)
}

// WriteFile returns an error since this is a read-only implementation
func (ro *ReadOnlySpacePrimitives) WriteFile(path string, data []byte, meta *FileMeta) (FileMeta, error) {
	return FileMeta{}, fmt.Errorf("%w: %q", ErrReadOnlySpacePath, path)
}

// DeleteFile returns an error since this is a read-only implementation
func (ro *ReadOnlySpacePrimitives) DeleteFile(path string) error {
	return fmt.Errorf("%w: %q", ErrReadOnlySpacePath, path)
}
