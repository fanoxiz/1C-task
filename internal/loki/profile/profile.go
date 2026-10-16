package profile

import (
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

type Profile struct {
	Token       string `json:"token"`
	DisplayName string `json:"display_name"`
}

var ErrAddress = errors.New("укажите адрес вида http://host:port без пути и учётных данных")

type Data struct {
	LastServer string             `json:"last_server"`
	Reconnect  bool               `json:"reconnect"`
	Profiles   map[string]Profile `json:"profiles"`
}

type Store struct {
	dir  string
	lock *os.File
	data Data
}

func Open(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, fmt.Errorf("cannot create profile directory")
	}
	lock, err := os.OpenFile(filepath.Join(dir, ".lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, fmt.Errorf("cannot open profile lock")
	}
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = lock.Close()
		return nil, fmt.Errorf("profile directory is already used by another client")
	}
	s := &Store{dir: dir, lock: lock, data: Data{Profiles: map[string]Profile{}}}
	content, err := os.ReadFile(filepath.Join(dir, "profiles.json"))
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err != nil || json.Unmarshal(content, &s.data) != nil || s.data.Profiles == nil {
		_ = s.Close()
		return nil, fmt.Errorf("cannot read saved profiles; refusing to replace identities")
	}
	if err := os.Chmod(filepath.Join(dir, "profiles.json"), 0600); err != nil {
		_ = s.Close()
		return nil, fmt.Errorf("cannot protect saved profiles")
	}
	return s, nil
}

func (s *Store) Data() Data {
	data := s.data
	data.Profiles = maps.Clone(data.Profiles)
	return data
}

func (s *Store) Save(data Data) error {
	f, err := os.CreateTemp(s.dir, ".profiles-*")
	if err != nil {
		return fmt.Errorf("cannot save profile")
	}
	defer os.Remove(f.Name())
	err = json.NewEncoder(f).Encode(data)
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil || closeErr != nil {
		return fmt.Errorf("cannot save profile")
	}
	if err := os.Rename(f.Name(), filepath.Join(s.dir, "profiles.json")); err != nil {
		return fmt.Errorf("cannot save profile")
	}
	s.data = data
	s.data.Profiles = maps.Clone(data.Profiles)
	return nil
}

func (s *Store) Close() error {
	if err := syscall.Flock(int(s.lock.Fd()), syscall.LOCK_UN); err != nil {
		_ = s.lock.Close()
		return err
	}
	return s.lock.Close()
}

// Normalize binds a credential to a single server origin, never to a user-entered path.
func Normalize(address string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(address))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.User != nil ||
		u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || (u.Path != "" && u.Path != "/") || u.Opaque != "" {
		return "", ErrAddress
	}
	host, port := strings.ToLower(u.Hostname()), u.Port()
	if port != "" {
		number, err := strconv.Atoi(port)
		if err != nil || number < 1 || number > 65535 {
			return "", ErrAddress
		}
	}
	if (u.Scheme == "http" && port == "80") || (u.Scheme == "https" && port == "443") {
		port = ""
	}
	if port != "" {
		u.Host = net.JoinHostPort(host, port)
	} else if strings.Contains(host, ":") {
		u.Host = "[" + host + "]"
	} else {
		u.Host = host
	}
	u.Path, u.RawPath = "", ""
	return u.String(), nil
}
