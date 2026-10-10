// Package store persists a private registry and journals client file updates.
package store

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/gofrs/flock"
	"golang.org/x/sys/unix"
)

var ErrConflict = errors.New("registry changed; reload before retrying")
var ErrPending = errors.New("pending transaction; recover before retrying")

type Store struct {
	Dir string
	// hook is a per-store test seam. Returning an error simulates a failed phase;
	// panicking simulates process death without running transaction rollback.
	hook func(string) error
}

func New(dir string) *Store           { return &Store{Dir: dir} }
func (s *Store) RegistryPath() string { return filepath.Join(s.Dir, "registry.json") }
func (s *Store) journalPath() string  { return filepath.Join(s.Dir, "journal.json") }
func (s *Store) phase(name string) error {
	if s.hook != nil {
		return s.hook(name)
	}
	return nil
}

func (s *Store) Load() (Registry, error) {
	b, _, err := ReadTarget(s.RegistryPath())
	if err != nil {
		return Registry{}, err
	}
	if len(b) == 0 {
		return emptyRegistry(), nil
	}
	return decodeRegistry(b)
}

// Pending is conservative: an unsafe or unreadable journal also blocks writes.
func (s *Store) Pending() bool {
	_, exists, err := ReadTarget(s.journalPath())
	return exists || err != nil
}

func (s *Store) locked(fn func() error) (err error) {
	dir, _, err := parent(s.RegistryPath(), true)
	if err != nil {
		return err
	}
	defer dir.Close()
	if err = dir.Chmod(0700); err != nil {
		return err
	}
	lockName := ".lock"
	fd, err := unix.Openat(int(dir.Fd()), lockName, unix.O_RDWR|unix.O_CREAT|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0600)
	// APFS may transiently return ENOENT when concurrent O_CREAT calls race
	// for a previously absent entry. Retry only that result, still no-follow.
	for attempt := 0; errors.Is(err, unix.ENOENT) && attempt < 8; attempt++ {
		fd, err = unix.Openat(int(dir.Fd()), lockName, unix.O_RDWR|unix.O_CREAT|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0600)
	}
	if err != nil {
		return fmt.Errorf("open store lock: %w", err)
	}
	file := os.NewFile(uintptr(fd), lockName)
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return errors.New("unsafe lock file")
	}
	if err = file.Chmod(0600); err != nil {
		return err
	}
	// Open the already-checked inode through its descriptor: no path component
	// can redirect the flock to a different lock during acquisition.
	lock := flock.New(fmt.Sprintf("/dev/fd/%d", fd), flock.SetFlag(os.O_RDWR), flock.SetPermissions(0600))
	if err = lock.Lock(); err != nil {
		return fmt.Errorf("acquire store lock: %w", err)
	}
	defer func() { err = errors.Join(err, lock.Unlock()) }()
	current, err := os.Stat(filepath.Join(s.Dir, lockName))
	if err != nil || !os.SameFile(info, current) {
		return errors.New("lock path changed externally")
	}
	checked, _, err := parent(s.RegistryPath(), false)
	if err != nil {
		return err
	}
	checked.Close()
	return fn()
}

func (s *Store) compare(expected Registry) ([]byte, bool, error) {
	want, err := canonical(expected)
	if err != nil {
		return nil, false, err
	}
	raw, exists, err := ReadTarget(s.RegistryPath())
	if err != nil {
		return nil, false, err
	}
	r := emptyRegistry()
	if len(raw) > 0 {
		r, err = decodeRegistry(raw)
		if err != nil {
			return nil, false, err
		}
	}
	got, err := canonical(r)
	if err != nil {
		return nil, false, err
	}
	var wantRegistry, gotRegistry Registry
	if err = strict(want, &wantRegistry); err != nil {
		return nil, false, err
	}
	if err = strict(got, &gotRegistry); err != nil {
		return nil, false, err
	}
	want, _ = json.Marshal(semanticJSON(wantRegistry))
	got, _ = json.Marshal(semanticJSON(gotRegistry))
	if !bytes.Equal(want, got) {
		return nil, false, ErrConflict
	}
	return raw, exists, nil
}

func (s *Store) Save(expected Registry, next Registry) error {
	data, err := canonical(next)
	if err != nil {
		return err
	}
	return s.locked(func() error {
		if s.Pending() {
			return ErrPending
		}
		before, exists, err := s.compare(expected)
		if err != nil {
			return err
		}
		if err = s.phase("before-registry"); err != nil {
			return err
		}
		return replace(s.RegistryPath(), before, exists, data, 0600)
	})
}

type journal struct {
	Version         int      `json:"version"`
	Path            string   `json:"path"`
	Backup          string   `json:"backup"`
	BeforeHash      string   `json:"before_hash"`
	BeforeExisted   bool     `json:"before_existed"`
	AfterHash       string   `json:"after_hash"`
	RegistryHash    string   `json:"registry_hash"`
	RegistryExisted bool     `json:"registry_existed"`
	Next            Registry `json:"next"`
}

func hash(b []byte) string { sum := sha256.Sum256(b); return hex.EncodeToString(sum[:]) }
func fingerprint(b []byte, exists bool) string {
	if !exists {
		return "absent"
	}
	return hash(b)
}

// Apply leaves the backup beside the client file, even if the transaction fails.
// New client contents, private store files, and backups use 0600. On rollback,
// the original client mode is restored along with its original contents.
func (s *Store) Apply(path string, before []byte, existed bool, after []byte, expected Registry, next Registry) (backup string, err error) {
	if len(before) > maxBytes || len(after) > maxBytes {
		return "", errors.New("target exceeds 4 MiB")
	}
	if !existed && len(before) != 0 {
		return "", errors.New("absent target has before bytes")
	}
	data, err := canonical(next)
	if err != nil {
		return "", err
	}
	path, err = filepath.Abs(path)
	if err != nil {
		return "", err
	}
	storeDir, err := filepath.Abs(s.Dir)
	if err != nil {
		return "", err
	}
	rel, err := filepath.Rel(storeDir, path)
	if err != nil {
		return "", err
	}
	if rel == "." || (rel != ".." && !bytes.HasPrefix([]byte(rel), []byte(".."+string(filepath.Separator)))) {
		return "", errors.New("client target must be outside store directory")
	}
	err = s.locked(func() error {
		if s.Pending() {
			return ErrPending
		}
		reg, regExists, e := s.compare(expected)
		if e != nil {
			return e
		}
		var mode os.FileMode
		backup, mode, e = backupTarget(path, before, existed)
		if e != nil {
			return e
		}
		// Journal the normalized, validated value, not caller-owned nil maps or
		// slices (which would otherwise become unrecoverable JSON null fields).
		var frozen Registry
		if e = strict(data, &frozen); e != nil {
			return e
		}
		j := journal{Version: 1, Path: path, Backup: backup, BeforeHash: hash(before), BeforeExisted: existed, AfterHash: hash(after), RegistryHash: fingerprint(reg, regExists), RegistryExisted: regExists, Next: frozen}
		jb, e := json.Marshal(j)
		if e != nil || len(jb) > maxBytes {
			return errors.New("journal exceeds 4 MiB")
		}
		if e = s.phase("before-journal"); e != nil {
			return e
		}
		if e = replace(s.journalPath(), nil, false, jb, 0600); e != nil {
			return e
		}
		// This explicit error path is not a defer: a crash must leave the journal.
		wroteClient := false
		e = s.phase("after-journal")
		if e == nil {
			wroteClient, e = replaceResult(path, before, existed, after, 0600)
		}
		if e == nil {
			e = s.phase("after-client")
		}
		if e == nil {
			e = s.phase("before-registry")
		}
		if e == nil {
			var config []byte
			var configExists bool
			config, configExists, e = ReadTarget(path)
			if e == nil && (!configExists || !bytes.Equal(config, after)) {
				e = errors.New("client changed externally")
			}
		}
		if e == nil {
			e = replace(s.RegistryPath(), reg, regExists, data, 0600)
		}
		if e != nil {
			// A rename may have succeeded even if its directory fsync failed. Never
			// roll back a committed registry or an external registry/config edit.
			now, exists, re := ReadTarget(s.RegistryPath())
			if re != nil || fingerprint(now, exists) != j.RegistryHash {
				return errors.Join(e, re, errors.New("transaction retained for recovery"))
			}
			got, exists, re := ReadTarget(path)
			if re != nil {
				return errors.Join(e, re)
			}
			if wroteClient && exists && bytes.Equal(got, after) {
				if existed {
					re = replace(path, after, true, before, mode)
				} else {
					re = removeMatching(path, after)
				}
			} else if exists != existed || !bytes.Equal(got, before) {
				re = errors.New("client changed externally; transaction retained")
			}
			if re == nil {
				re = removeMatching(s.journalPath(), jb)
			}
			return errors.Join(e, re)
		}
		if e = s.phase("after-registry"); e != nil {
			return e
		}
		if e = s.phase("before-cleanup"); e != nil {
			return e
		}
		return removeMatching(s.journalPath(), jb)
	})
	return backup, err
}

func (s *Store) Recover() error {
	// Avoid creating a store or lock for the normal no-journal case.
	if !s.Pending() {
		return nil
	}
	return s.locked(func() error {
		raw, exists, err := ReadTarget(s.journalPath())
		if err != nil || !exists {
			return err
		}
		var j journal
		if err = strict(raw, &j); err != nil {
			return errors.New("invalid recovery journal")
		}
		if j.Version != 1 || !filepath.IsAbs(j.Path) || !validHash(j.BeforeHash) || !validHash(j.AfterHash) || (j.RegistryExisted && !validHash(j.RegistryHash)) || (!j.RegistryExisted && j.RegistryHash != "absent") {
			return errors.New("invalid recovery journal")
		}
		next, err := canonical(j.Next)
		if err != nil {
			return errors.New("invalid recovery registry")
		}
		reg, regExists, err := ReadTarget(s.RegistryPath())
		if err != nil {
			return err
		}
		expected := fingerprint(reg, regExists) == j.RegistryHash
		committed := regExists && bytes.Equal(reg, next)
		if !expected && !committed {
			return ErrConflict
		}
		config, configExists, err := ReadTarget(j.Path)
		if err != nil {
			return err
		}
		before := configExists == j.BeforeExisted && hash(config) == j.BeforeHash
		after := configExists && hash(config) == j.AfterHash
		if before && expected {
			return removeMatching(s.journalPath(), raw)
		}
		if !after {
			return errors.New("client changed externally; recovery refused")
		}
		if !committed {
			if err = replace(s.RegistryPath(), reg, regExists, next, 0600); err != nil {
				return err
			}
		}
		return removeMatching(s.journalPath(), raw)
	})
}

func validHash(s string) bool { b, e := hex.DecodeString(s); return e == nil && len(b) == sha256.Size }
