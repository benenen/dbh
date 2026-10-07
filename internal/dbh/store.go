package dbh

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"

	"github.com/benenen/dbh/internal/database/registry"
	"github.com/gofrs/flock"
)

type Profile struct {
	Name   string `json:"name"`
	Driver string `json:"driver"`
	DSN    string `json:"dsn"`
}

type Store struct{ Dir string }

func defaultDir() (string, error) {
	if dir := os.Getenv("DBH_CONFIG_DIR"); dir != "" {
		return dir, nil
	}
	dir, err := os.UserConfigDir()
	return filepath.Join(dir, "dbh"), err
}

func (s Store) prepare() error {
	if err := os.MkdirAll(s.Dir, 0700); err != nil {
		return err
	}
	return os.Chmod(s.Dir, 0700)
}

func (s Store) load() (map[string]Profile, error) {
	profiles := map[string]Profile{}
	data, err := os.ReadFile(filepath.Join(s.Dir, "connections.json"))
	if errors.Is(err, os.ErrNotExist) {
		return profiles, nil
	}
	if err != nil {
		return nil, err
	}
	if err = json.Unmarshal(data, &profiles); err != nil {
		return nil, fmt.Errorf("invalid connections.json: %w", err)
	}
	if profiles == nil {
		profiles = map[string]Profile{}
	}
	return profiles, nil
}

func (s Store) List() ([]Profile, error) {
	m, err := s.load()
	if err != nil {
		return nil, err
	}
	result := make([]Profile, 0, len(m))
	for _, p := range m {
		result = append(result, p)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Name < result[j].Name })
	return result, nil
}

func (s Store) Get(name string) (Profile, error) {
	m, err := s.load()
	if err != nil {
		return Profile{}, err
	}
	p, ok := m[name]
	if !ok {
		return Profile{}, fmt.Errorf("connection %q does not exist", name)
	}
	return p, nil
}

func (s Store) update(fn func(map[string]Profile) error) error {
	if err := s.prepare(); err != nil {
		return err
	}
	lock := flock.New(filepath.Join(s.Dir, ".connections.lock"))
	if err := lock.Lock(); err != nil {
		return err
	}
	defer func() { _ = lock.Unlock() }()
	m, err := s.load()
	if err != nil {
		return err
	}
	if err := fn(m); err != nil {
		return err
	}
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(s.Dir, ".connections-*")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(f.Name()) }()
	if _, err = f.Write(append(data, '\n')); err != nil {
		_ = f.Close()
		return err
	}
	if err = f.Sync(); err != nil {
		_ = f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), filepath.Join(s.Dir, "connections.json"))
}

var validName = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]*$`)

func validate(p Profile) error {
	if !validName.MatchString(p.Name) {
		return fmt.Errorf("name must contain letters, digits, dots, underscores or hyphens and start with a letter or digit")
	}
	if _, err := registry.Lookup(p.Driver); err != nil {
		return err
	}
	if p.DSN == "" {
		return errors.New("DSN cannot be empty")
	}
	return nil
}

func (s Store) Put(p Profile, create bool) error {
	if err := validate(p); err != nil {
		return err
	}
	return s.update(func(m map[string]Profile) error {
		_, exists := m[p.Name]
		if create && exists {
			return fmt.Errorf("connection %q already exists", p.Name)
		}
		if !create && !exists {
			return fmt.Errorf("connection %q does not exist", p.Name)
		}
		m[p.Name] = p
		return nil
	})
}

func (s Store) Remove(name string) error {
	return s.update(func(m map[string]Profile) error {
		if _, ok := m[name]; !ok {
			return fmt.Errorf("connection %q does not exist", name)
		}
		delete(m, name)
		return nil
	})
}
