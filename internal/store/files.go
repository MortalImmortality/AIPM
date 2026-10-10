package store

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

const maxBytes = 4 << 20

// parent opens every path component without following symlinks. Holding the
// directory descriptor also keeps a renamed ancestor from redirecting writes.
func parent(path string, create bool) (*os.File, string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, "", err
	}
	if abs == string(filepath.Separator) {
		return nil, "", errors.New("target is not a regular file")
	}
	fd, err := unix.Open("/", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, "", err
	}
	parts := strings.Split(strings.TrimPrefix(abs, "/"), "/")
	for _, part := range parts[:len(parts)-1] {
		next, e := unix.Openat(fd, part, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		if errors.Is(e, unix.ENOENT) && create {
			e = unix.Mkdirat(fd, part, 0700)
			if e == nil {
				e = unix.Fsync(fd)
			}
			if e == nil || errors.Is(e, unix.EEXIST) {
				next, e = unix.Openat(fd, part, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
			}
		}
		unix.Close(fd)
		if e != nil {
			return nil, "", fmt.Errorf("unsafe or inaccessible ancestor: %w", e)
		}
		fd = next
	}
	return os.NewFile(uintptr(fd), filepath.Dir(abs)), parts[len(parts)-1], nil
}

func readAt(dir *os.File, name string) ([]byte, bool, os.FileMode, error) {
	fd, err := unix.Openat(int(dir.Fd()), name, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if errors.Is(err, unix.ENOENT) {
		return nil, false, 0600, nil
	}
	if err != nil {
		return nil, false, 0, fmt.Errorf("unsafe or inaccessible target: %w", err)
	}
	f := os.NewFile(uintptr(fd), name)
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, false, 0, err
	}
	if !info.Mode().IsRegular() {
		return nil, false, 0, errors.New("target is not a regular file")
	}
	if info.Size() > maxBytes {
		return nil, false, 0, errors.New("target exceeds 4 MiB")
	}
	data, err := io.ReadAll(io.LimitReader(f, maxBytes+1))
	if err != nil {
		return nil, false, 0, err
	}
	if len(data) > maxBytes {
		return nil, false, 0, errors.New("target exceeds 4 MiB")
	}
	return data, true, info.Mode().Perm(), nil
}

// ReadTarget is read-only and refuses symlinks, including in ancestors.
func ReadTarget(path string) ([]byte, bool, error) {
	dir, name, err := parent(path, false)
	if errors.Is(err, unix.ENOENT) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	defer dir.Close()
	b, exists, _, err := readAt(dir, name)
	return b, exists, err
}

func sameAt(dir *os.File, name string, want []byte, existed bool) error {
	got, exists, _, err := readAt(dir, name)
	if err != nil {
		return err
	}
	if exists != existed || !bytes.Equal(got, want) {
		return errors.New("target changed externally")
	}
	return nil
}

func temporary(dir *os.File, prefix string, data []byte, mode os.FileMode) (string, error) {
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		return "", err
	}
	name := prefix + hex.EncodeToString(id[:])
	fd, err := unix.Openat(int(dir.Fd()), name, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0600)
	if err != nil {
		return "", fmt.Errorf("stage file: %w", err)
	}
	f := os.NewFile(uintptr(fd), name)
	err = f.Chmod(mode)
	if err == nil {
		_, err = f.Write(data)
	}
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		unix.Unlinkat(int(dir.Fd()), name, 0)
		return "", err
	}
	return name, nil
}

// replace checks bytes AND existence after staging, immediately before rename.
func replace(path string, before []byte, existed bool, after []byte, mode os.FileMode) error {
	_, err := replaceResult(path, before, existed, after, mode)
	return err
}

// The boolean records whether the rename happened, including fsync failures.
func replaceResult(path string, before []byte, existed bool, after []byte, mode os.FileMode) (written bool, err error) {
	if len(after) > maxBytes {
		return false, errors.New("target exceeds 4 MiB")
	}
	dir, name, err := parent(path, false)
	if err != nil {
		return false, err
	}
	defer dir.Close()
	tmp, err := temporary(dir, ".aipm-tmp-", after, mode)
	if err != nil {
		return false, err
	}
	defer unix.Unlinkat(int(dir.Fd()), tmp, 0)
	if err = sameAt(dir, name, before, existed); err != nil {
		return false, err
	}
	if err = unix.Renameat(int(dir.Fd()), tmp, int(dir.Fd()), name); err != nil {
		return false, fmt.Errorf("rename staged file: %w", err)
	}
	return true, dir.Sync()
}

func removeMatching(path string, before []byte) error {
	dir, name, err := parent(path, false)
	if err != nil {
		return err
	}
	defer dir.Close()
	if err = sameAt(dir, name, before, true); err != nil {
		return err
	}
	if err = unix.Unlinkat(int(dir.Fd()), name, 0); err != nil {
		return err
	}
	return dir.Sync()
}

func backupTarget(path string, before []byte, existed bool) (string, os.FileMode, error) {
	dir, name, err := parent(path, !existed)
	if err != nil {
		return "", 0, err
	}
	defer dir.Close()
	if err = sameAt(dir, name, before, existed); err != nil {
		return "", 0, err
	}
	_, _, mode, err := readAt(dir, name)
	if err != nil {
		return "", 0, err
	}
	if !existed {
		return "", 0600, nil
	}
	backup, err := temporary(dir, ".aipm-backup-", before, 0600)
	if err != nil {
		return "", 0, err
	}
	if err = dir.Sync(); err != nil {
		return "", 0, err
	}
	return filepath.Join(filepath.Dir(path), backup), mode, nil
}
