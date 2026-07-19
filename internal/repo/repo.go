package repo

import (
	"fmt"
	"io/ioutil"
	"os"
	"path/filepath"
)

var LoDir = ".qin"

type Repository struct {
	Path   string
	Config *Config
}

// Init creates a new repository at the given path.
func Init(path string) (*Repository, error) {
	if path == "" {
		path = "."
	}
	absPath, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("resolve path: %w", err)
	}

	loDir := filepath.Join(absPath, LoDir)
	if _, err := os.Stat(loDir); err == nil {
		return nil, fmt.Errorf("already a repository: %s", absPath)
	}

	dirs := []string{
		filepath.Join(loDir, "objects"),
		filepath.Join(loDir, "refs", "heads"),
		filepath.Join(loDir, "refs", "tags"),
	}
	for _, d := range dirs {
		if err := os.MkdirAll(d, 0755); err != nil {
			return nil, fmt.Errorf("create %s: %w", d, err)
		}
	}

	if err := ioutil.WriteFile(filepath.Join(loDir, "HEAD"), []byte("ref: refs/heads/main\n"), 0644); err != nil {
		return nil, fmt.Errorf("write HEAD: %w", err)
	}

	cfg := DefaultConfig()
	if err := SaveConfig(absPath, cfg); err != nil {
		return nil, err
	}

	return &Repository{
		Path:   absPath,
		Config: cfg,
	}, nil
}

// Open opens an existing repository by searching from path upward.
func Open(path string) (*Repository, error) {
	absPath, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("resolve path: %w", err)
	}

	dir := absPath
	for {
		loDir := filepath.Join(dir, LoDir)
		if info, err := os.Stat(loDir); err == nil && info.IsDir() {
			cfg, err := LoadConfig(dir)
			if err != nil {
				return nil, err
			}
			return &Repository{Path: dir, Config: cfg}, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return nil, fmt.Errorf("not a repository (no .lo found)")
		}
		dir = parent
	}
}

func (r *Repository) LoDir() string {
	return filepath.Join(r.Path, LoDir)
}

func (r *Repository) ObjectsDir() string {
	return filepath.Join(r.Path, LoDir, "objects")
}

func (r *Repository) RefsDir() string {
	return filepath.Join(r.Path, LoDir, "refs")
}

func (r *Repository) HeadPath() string {
	return filepath.Join(r.Path, LoDir, "HEAD")
}

// ReadHEAD returns the current HEAD value.
// Returns "ref: refs/heads/main" (symbolic) or a commit hash.
func (r *Repository) ReadHEAD() (string, error) {
	data, err := ioutil.ReadFile(r.HeadPath())
	if err != nil {
		return "", err
	}
	ref := string(data)
	if len(ref) > 0 && ref[len(ref)-1] == '\n' {
		ref = ref[:len(ref)-1]
	}
	return ref, nil
}

// SetHEAD sets HEAD to a symbolic ref.
func (r *Repository) SetHEAD(ref string) error {
	return ioutil.WriteFile(r.HeadPath(), []byte(ref+"\n"), 0644)
}

// ReadRef reads the commit hash a ref points to.
func (r *Repository) ReadRef(ref string) (string, error) {
	refPath := filepath.Join(r.Path, LoDir, ref)
	data, err := ioutil.ReadFile(refPath)
	if err != nil {
		return "", err
	}
	hash := string(data)
	if len(hash) > 0 && hash[len(hash)-1] == '\n' {
		hash = hash[:len(hash)-1]
	}
	return hash, nil
}

// WriteRef writes a commit hash to a ref.
func (r *Repository) WriteRef(ref, hash string) error {
	refPath := filepath.Join(r.Path, LoDir, ref)
	if err := os.MkdirAll(filepath.Dir(refPath), 0755); err != nil {
		return err
	}
	return ioutil.WriteFile(refPath, []byte(hash+"\n"), 0644)
}

// ResolveHEAD resolves HEAD to the current commit hash (or empty if no commits).
func (r *Repository) ResolveHEAD() (string, error) {
	head, err := r.ReadHEAD()
	if err != nil {
		return "", err
	}
	if len(head) > 5 && head[:4] == "ref:" {
		ref := head[5:] // skip "ref: "
		hash, err := r.ReadRef(ref)
		if err != nil {
			if os.IsNotExist(err) {
				return "", nil // no commits yet
			}
			return "", err
		}
		return hash, nil
	}
	return head, nil
}

// CurrentBranch returns the current branch name, or empty if detached.
func (r *Repository) CurrentBranch() string {
	head, err := r.ReadHEAD()
	if err != nil {
		return ""
	}
	if len(head) > 5 && head[:4] == "ref:" {
		ref := head[5:]
		// refs/heads/main -> main
		if len(ref) > 11 && ref[:11] == "refs/heads/" {
			return ref[11:]
		}
		return ref
	}
	return ""
}
