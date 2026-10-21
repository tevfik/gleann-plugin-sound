package speaker

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

// Profile is an enrolled voice: the normalised mean of its sample embeddings.
type Profile struct {
	Name    string    `json:"name"`
	Vec     []float32 `json:"vec"`
	Samples int       `json:"samples"`
}

// Registry stores enrolled voices in ~/.gleann/speakers.json.
type Registry struct {
	mu       sync.RWMutex
	path     string
	Profiles []Profile `json:"profiles"`
}

// DefaultPath is ~/.gleann/speakers.json.
func DefaultPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".gleann", "speakers.json")
}

// LoadRegistry reads the file; a missing file is an empty registry.
func LoadRegistry(path string) (*Registry, error) {
	r := &Registry{path: path}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return r, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(data, r); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return r, nil
}

// Save writes the registry (0600: voice prints are personal data).
func (r *Registry) Save() error {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if err := os.MkdirAll(filepath.Dir(r.path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(r.path, data, 0o600)
}

// Enroll adds or replaces a voice from sample embeddings (each unit length).
func (r *Registry) Enroll(name string, samples [][]float32) error {
	name = strings.TrimSpace(name)
	if name == "" || len(samples) == 0 {
		return errors.New("need a name and at least one sample")
	}
	acc := make([]float64, len(samples[0]))
	for _, s := range samples {
		if len(s) != len(acc) {
			return errors.New("samples have different dimensions")
		}
		for i, x := range s {
			acc[i] += float64(x)
		}
	}
	p := Profile{Name: name, Vec: normalizeVec(acc), Samples: len(samples)}
	r.mu.Lock()
	defer r.mu.Unlock()
	for i := range r.Profiles {
		if strings.EqualFold(r.Profiles[i].Name, name) {
			r.Profiles[i] = p
			return nil
		}
	}
	r.Profiles = append(r.Profiles, p)
	sort.Slice(r.Profiles, func(i, j int) bool { return r.Profiles[i].Name < r.Profiles[j].Name })
	return nil
}

// Remove deletes a voice; it reports whether it existed.
func (r *Registry) Remove(name string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	for i := range r.Profiles {
		if strings.EqualFold(r.Profiles[i].Name, name) {
			r.Profiles = append(r.Profiles[:i], r.Profiles[i+1:]...)
			return true
		}
	}
	return false
}

// Match returns the enrolled voice closest to v (unit length) and the cosine.
func (r *Registry) Match(v []float32) (name string, score float64) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	score = -2
	for _, p := range r.Profiles {
		if len(p.Vec) != len(v) {
			continue
		}
		var dot float64
		for i := range v {
			dot += float64(v[i]) * float64(p.Vec[i])
		}
		if dot > score {
			name, score = p.Name, dot
		}
	}
	return name, score
}

// Len is the number of enrolled voices.
func (r *Registry) Len() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.Profiles)
}

func normalizeVec(v []float64) []float32 {
	var n float64
	for _, x := range v {
		n += x * x
	}
	n = math.Sqrt(n)
	out := make([]float32, len(v))
	if n == 0 {
		return out
	}
	for i, x := range v {
		out[i] = float32(x / n)
	}
	return out
}
