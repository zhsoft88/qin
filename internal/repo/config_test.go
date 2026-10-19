package repo

import (
	"io/ioutil"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestConfigSymlinksDefaultByHost pins the git-compatible platform default:
// Windows checks links out as regular files, everywhere else as links.
func TestConfigSymlinksDefaultByHost(t *testing.T) {
	old := hostGOOS
	defer func() { hostGOOS = old }()

	for _, tc := range []struct {
		goos string
		want string
	}{
		{"windows", "false"},
		{"linux", "true"},
		{"darwin", "true"},
	} {
		goos := tc.goos
		hostGOOS = func() string { return goos }
		got, err := ConfigGet(DefaultConfig(), "core.symlinks")
		if err != nil {
			t.Fatalf("%s: %v", tc.goos, err)
		}
		if got != tc.want {
			t.Errorf("%s: core.symlinks = %q, want %q", tc.goos, got, tc.want)
		}
	}

	if _, ok := ConfigKeys()["core.symlinks"]; !ok {
		t.Error("core.symlinks is not a known config key")
	}
}

// TestConfigSymlinksRoundTrip covers the tri-state storage: an explicit false
// must survive Save/Load (a plain bool with omitempty would lose it), and
// unset must go back to meaning "platform default", not a frozen value.
func TestConfigSymlinksRoundTrip(t *testing.T) {
	dir, err := ioutil.TempDir("", "qin-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)

	r, err := Init(dir)
	if err != nil {
		t.Fatal(err)
	}

	if err := ConfigSet(r.Config, "core.symlinks", "false"); err != nil {
		t.Fatal(err)
	}
	if r.Config.Core.Symlinks == nil || *r.Config.Core.Symlinks {
		t.Fatalf("ConfigSet did not record an explicit false: %v", r.Config.Core.Symlinks)
	}
	if err := SaveConfig(r.Path, r.Config); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadConfig(r.Path)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Core.Symlinks == nil || *loaded.Core.Symlinks {
		t.Fatalf("explicit false did not survive Save/Load: %v", loaded.Core.Symlinks)
	}
	data, err := ioutil.ReadFile(filepath.Join(r.Path, QinDir, "config"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"symlinks": false`) {
		t.Fatalf("config does not record an explicit false:\n%s", data)
	}

	if err := ConfigSet(loaded, "core.symlinks", "maybe"); err == nil {
		t.Error("ConfigSet accepted an invalid boolean")
	}
	if err := ConfigUnset(loaded, "core.symlinks"); err != nil {
		t.Fatal(err)
	}
	if loaded.Core.Symlinks != nil {
		t.Fatalf("ConfigUnset left a value behind: %v", *loaded.Core.Symlinks)
	}
	if err := SaveConfig(r.Path, loaded); err != nil {
		t.Fatal(err)
	}
	data, err = ioutil.ReadFile(filepath.Join(r.Path, QinDir, "config"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), `"symlinks"`) {
		t.Fatalf("unset key is still written:\n%s", data)
	}
	got, err := ConfigGet(loaded, "core.symlinks")
	if err != nil {
		t.Fatal(err)
	}
	want := "true"
	if hostGOOS() == "windows" {
		want = "false"
	}
	if got != want {
		t.Errorf("after unset, core.symlinks = %q, want the platform default %q", got, want)
	}
}
