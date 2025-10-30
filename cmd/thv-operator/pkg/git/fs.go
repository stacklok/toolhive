package git

import (
	"fmt"
	"io"
	"io/fs"
	"os"

	billy "github.com/go-git/go-billy/v5"
)

// From https://github.com/mindersec/minder/blob/main/internal/providers/git/memboxfs/fs.go
// Apache License 2.0
// Copyright (c) 2023 MinderSec

// LimitedFs provides a size-limited billy.Filesystem.  This is a struct, there's
// no constructor here. Note that LimitedFs is not thread-safe.
type LimitedFs struct {
	Fs            billy.Filesystem
	MaxFiles      int64
	TotalFileSize int64

	currentFiles int64
	currentSize  int64
}

// ErrNotImplemented is returned when a method is not implemented.
var ErrNotImplemented = fmt.Errorf("not implemented")

// ErrTooBig is returned when a file is too big.
var ErrTooBig = fmt.Errorf("file too big")

// ErrTooManyFiles is returned when there are too many files.
var ErrTooManyFiles = fmt.Errorf("too many files")

var _ billy.Filesystem = (*LimitedFs)(nil)

// Chroot implements billy.Filesystem.
func (*LimitedFs) Chroot(_ string) (billy.Filesystem, error) {
	return nil, ErrNotImplemented
}

// Create implements billy.Filesystem.
func (f *LimitedFs) Create(filename string) (billy.File, error) {
	f.currentFiles++
	if f.currentFiles >= f.MaxFiles {
		return nil, fmt.Errorf("%w: current %d, max %d, %s", ErrTooManyFiles, f.currentFiles, f.MaxFiles, filename)
	}
	file, err := f.Fs.Create(filename)
	return &fileWrapper{f: file, fs: f}, err
}

// Join implements billy.Filesystem.
func (f *LimitedFs) Join(elem ...string) string {
	return f.Fs.Join(elem...)
}

// Lstat implements billy.Filesystem.
func (f *LimitedFs) Lstat(filename string) (fs.FileInfo, error) {
	return f.Fs.Lstat(filename)
}

// MkdirAll implements billy.Filesystem.
func (f *LimitedFs) MkdirAll(filename string, perm fs.FileMode) error {
	// TODO: account for path segments correctly
	f.currentFiles++
	if f.currentFiles >= f.MaxFiles {
		return fmt.Errorf("%w: current %d, max %d, %s", ErrTooManyFiles, f.currentFiles, f.MaxFiles, filename)
	}
	return f.Fs.MkdirAll(filename, perm)
}

// Open implements billy.Filesystem.
func (f *LimitedFs) Open(filename string) (billy.File, error) {
	return f.Fs.Open(filename)
}

// OpenFile implements billy.Filesystem.
func (f *LimitedFs) OpenFile(filename string, flag int, perm fs.FileMode) (billy.File, error) {
	if flag&os.O_CREATE != 0 {
		f.currentFiles++
		if f.currentFiles >= f.MaxFiles {
			return nil, fmt.Errorf("%w: current %d, max %d, %s", ErrTooManyFiles, f.currentFiles, f.MaxFiles, filename)
		}
	}
	file, err := f.Fs.OpenFile(filename, flag, perm)
	return &fileWrapper{f: file, fs: f}, err
}

// ReadDir implements billy.Filesystem.
func (f *LimitedFs) ReadDir(path string) ([]fs.FileInfo, error) {
	return f.Fs.ReadDir(path)
}

// Readlink implements billy.Filesystem.
func (f *LimitedFs) Readlink(link string) (string, error) {
	return f.Fs.Readlink(link)
}

// Remove implements billy.Filesystem.
func (f *LimitedFs) Remove(filename string) error {
	// TODO: should we decrement currentFiles here?  It's not clear if the underlying
	// fs will reclaim memory on Remove, so we are conservative.
	return f.Fs.Remove(filename)
}

// Rename implements billy.Filesystem.
func (f *LimitedFs) Rename(oldpath string, newpath string) error {
	return f.Fs.Rename(oldpath, newpath)
}

// Root implements billy.Filesystem.
func (f *LimitedFs) Root() string {
	return f.Fs.Root()
}

// Stat implements billy.Filesystem.
func (f *LimitedFs) Stat(filename string) (fs.FileInfo, error) {
	return f.Fs.Stat(filename)
}

// Symlink implements billy.Filesystem.
func (f *LimitedFs) Symlink(target string, link string) error {
	f.currentFiles++
	if f.currentFiles >= f.MaxFiles {
		return fmt.Errorf("%w: current %d, max %d, %s", ErrTooManyFiles, f.currentFiles, f.MaxFiles, link)
	}
	return f.Fs.Symlink(target, link)
}

// TempFile implements billy.Filesystem.
func (f *LimitedFs) TempFile(dir string, prefix string) (billy.File, error) {
	f.currentFiles++
	if f.currentFiles >= f.MaxFiles {
		return nil, fmt.Errorf("%w: current %d, max %d, %s/%s", ErrTooManyFiles, f.currentFiles, f.MaxFiles, dir, prefix)
	}
	file, err := f.Fs.TempFile(dir, prefix)
	return &fileWrapper{f: file, fs: f}, err
}

type fileWrapper struct {
	f billy.File

	fs *LimitedFs
}

var _ billy.File = (*fileWrapper)(nil)

// Close implements billy.File.
func (f *fileWrapper) Close() error {
	return f.f.Close()
}

// Lock implements billy.File.
func (f *fileWrapper) Lock() error {
	return f.f.Lock()
}

// Name implements billy.File.
func (f *fileWrapper) Name() string {
	return f.f.Name()
}

// Read implements billy.File.
func (f *fileWrapper) Read(p []byte) (n int, err error) {
	return f.f.Read(p)
}

// ReadAt implements billy.File.
func (f *fileWrapper) ReadAt(p []byte, off int64) (n int, err error) {
	return f.f.ReadAt(p, off)
}

// Seek implements billy.File.
func (f *fileWrapper) Seek(offset int64, whence int) (int64, error) {
	return f.f.Seek(offset, whence)
}

// Truncate implements billy.File.
func (f *fileWrapper) Truncate(size int64) error {
	existingSize, err := f.fileSize()
	if err != nil {
		return err
	}

	growth := size - existingSize
	if growth+f.fs.currentSize > f.fs.TotalFileSize {
		return fmt.Errorf("%w: current %d, total %d, %s", ErrTooBig, f.fs.currentSize, f.fs.TotalFileSize, f.Name())
	}

	f.fs.currentSize += growth
	return f.f.Truncate(size)
}

// Unlock implements billy.File.
func (f *fileWrapper) Unlock() error {
	return f.f.Unlock()
}

// Write implements billy.File.
func (f *fileWrapper) Write(p []byte) (n int, err error) {
	size, err := f.fileSize()
	if err != nil {
		return 0, err
	}
	offset, err := f.Seek(0, io.SeekCurrent)
	if err != nil {
		return 0, err
	}

	growth := offset + int64(len(p)) - size
	if growth < 0 {
		growth = 0
	}
	if growth+f.fs.currentSize > f.fs.TotalFileSize {
		return 0, fmt.Errorf("%w: %s", ErrTooBig, f.Name())
	}

	f.fs.currentSize += growth
	return f.f.Write(p)
}

func (f *fileWrapper) fileSize() (int64, error) {
	fi, err := f.fs.Stat(f.Name())
	if err != nil {
		return 0, err
	}

	return fi.Size(), nil
}
