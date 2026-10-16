package profile

import (
	"os"
	"path/filepath"
	"testing"
)

func TestPersistencePermissionsAndExclusiveLock(t *testing.T) {
	dir := t.TempDir()
	store, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if second, err := Open(dir); err == nil {
		second.Close()
		t.Fatal("same profile can be opened concurrently")
	}
	data := store.Data()
	data.LastServer, data.Reconnect = "http://localhost:8081", true
	data.Profiles[data.LastServer] = Profile{Token: "saved-secret", DisplayName: "Loki"}
	if err := store.Save(data); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(filepath.Join(dir, "profiles.json"))
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("profile permissions: %v %v", info, err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if got := reopened.Data(); got.Profiles[data.LastServer].Token != "saved-secret" || !got.Reconnect {
		t.Fatal("saved identity was lost")
	}
	copy := reopened.Data()
	delete(copy.Profiles, data.LastServer)
	if len(reopened.Data().Profiles) != 1 {
		t.Fatal("profile can be mutated through a snapshot")
	}
}

func TestCorruptProfilesAreNotReplaced(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "profiles.json"), []byte("broken"), 0600); err != nil {
		t.Fatal(err)
	}
	if store, err := Open(dir); err == nil {
		store.Close()
		t.Fatal("corrupt identity file was accepted")
	}
}

func TestNormalizeServerOrigins(t *testing.T) {
	for input, want := range map[string]string{
		" http://LOCALHOST:80/ ":  "http://localhost",
		"https://example.org:443": "https://example.org",
		"http://[::1]:8081/":      "http://[::1]:8081",
	} {
		got, err := Normalize(input)
		if err != nil || got != want {
			t.Fatalf("%q -> %q, %v; want %q", input, got, err, want)
		}
	}
	for _, input := range []string{"localhost:8081", "ftp://host", "http://user:secret@host", "http://host/path", "http://host?x=1", "http://host/#fragment", "http://host:99999", "http://host:0"} {
		if _, err := Normalize(input); err == nil {
			t.Fatalf("invalid server address accepted: %q", input)
		}
	}
}
