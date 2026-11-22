package codemode

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Cache is an on-disk cache for two things that are safe to reuse:
//
//   - capability documents, keyed by CapabilityCacheKey(version, fingerprint,
//     authContext). Only the public capability document is stored; never
//     secrets, never the raw authorization context (it is hashed into the key).
//   - compiled-program plans (imports, referenced operations, diagnostics),
//     keyed by Program.CacheKey plus the policy inputs that change diagnostics.
//     A change in the entry or any imported source changes the key.
//
// Approvals and run results are never cached. A nil *Cache is "off": every
// method is a no-op miss.
type Cache struct {
	// Dir is the cache root. Files live under Dir/capabilities and Dir/programs.
	Dir string
}

// DefaultCacheDir is os.UserCacheDir()/codemode.
func DefaultCacheDir() (string, error) {
	d, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(d, "codemode"), nil
}

// NewFileCache returns a cache rooted at dir, or at DefaultCacheDir when dir
// is empty.
func NewFileCache(dir string) (*Cache, error) {
	if dir == "" {
		d, err := DefaultCacheDir()
		if err != nil {
			return nil, err
		}
		dir = d
	}
	return &Cache{Dir: dir}, nil
}

func hashKey(parts ...string) string {
	h := sha256.New()
	for _, p := range parts {
		fmt.Fprintf(h, "%d:%s\x00", len(p), p)
	}
	return hex.EncodeToString(h.Sum(nil))[:40]
}

// CapabilityCacheKey derives a file-safe key from the CLI version, the
// capability fingerprint and the authorization context the document was
// fetched under. The auth context is hashed, never stored.
func CapabilityCacheKey(version, fingerprint, authContext string) string {
	return "cap-" + hashKey("capabilities", version, fingerprint, authContext)
}

// LoadCapabilities returns the cached document for key.
func (c *Cache) LoadCapabilities(key string) (*Capabilities, bool, error) {
	var caps Capabilities
	ok, err := c.load("capabilities", key, &caps)
	if !ok || err != nil {
		return nil, false, err
	}
	return &caps, true, nil
}

// SaveCapabilities stores the capability document under key.
func (c *Cache) SaveCapabilities(key string, caps Capabilities) error {
	return c.save("capabilities", key, caps)
}

// compiledEntry is what the program cache stores: the plan and diagnostics,
// never ASTs, approvals or results.
type compiledEntry struct {
	CacheKey    string       `json:"cache_key"`
	Hash        string       `json:"hash"`
	Imports     []string     `json:"imports"`
	Operations  []string     `json:"operations"`
	Diagnostics []Diagnostic `json:"diagnostics"`
}

// programIndex maps an entry file + its source to the imports it had, so a
// lookup can re-read exactly those imports and recompute Program.CacheKey.
type programIndex struct {
	Imports  []string `json:"imports"`
	CacheKey string   `json:"cache_key"`
}

// policyKey covers the policy inputs that change diagnostics but are not in
// Program.CacheKey.
func (e *Engine) policyKey() string {
	progs := append([]string(nil), e.Policy.Exec.Programs...)
	sort.Strings(progs)
	return fmt.Sprintf("exec=%s|allowfn=%v|root=%s", strings.Join(progs, ","), e.Policy.Exec.Allow != nil, e.Policy.ModuleRoot)
}

func (e *Engine) indexKey(src, file string) string {
	abs := file
	if !strings.HasPrefix(file, "<") {
		abs, _ = filepath.Abs(file)
	}
	return "idx-" + hashKey(abs, src, CompilerVersion, e.Registry.Capabilities().Fingerprint, e.policyKey())
}

func (e *Engine) planKey(cacheKey string) string {
	return "plan-" + hashKey(cacheKey, e.policyKey())
}

// CompilePlan is Compile for check/compile: it returns the plan and
// diagnostics, served from Engine.Cache when the entry and every import are
// unchanged. The returned Program carries no AST, so it cannot be passed to
// a runner; use Run for that. cached reports a cache hit.
func (e *Engine) CompilePlan(src, file string) (p *Program, diags []Diagnostic, cached bool) {
	if e.Cache != nil {
		if p, diags, ok := e.cachedPlan(src, file); ok {
			return p, diags, true
		}
	}
	p, diags = e.Compile(src, file)
	if e.Cache != nil && p != nil && cacheable(diags) {
		ent := compiledEntry{CacheKey: p.CacheKey, Hash: p.Hash, Imports: p.Imports, Operations: p.Operations, Diagnostics: diags}
		if e.Cache.save("programs", e.planKey(p.CacheKey), ent) == nil {
			_ = e.Cache.save("programs", e.indexKey(src, file), programIndex{Imports: p.Imports, CacheKey: p.CacheKey})
		}
	}
	return p, diags, false
}

// cacheable rejects results that depend on files missing from the key: a
// failed import or parse error might resolve once a file appears.
func cacheable(diags []Diagnostic) bool {
	for _, d := range diags {
		if strings.HasPrefix(d.Message, "cannot import") || strings.Contains(d.Message, "syntax error") || strings.Contains(d.Message, "parse error") {
			return false
		}
	}
	return true
}

func (e *Engine) cachedPlan(src, file string) (*Program, []Diagnostic, bool) {
	var idx programIndex
	if ok, err := e.Cache.load("programs", e.indexKey(src, file), &idx); !ok || err != nil {
		return nil, nil, false
	}
	// Re-read every import and recompute the hash exactly as Compile does.
	sources := map[string]string{file: src}
	for _, imp := range idx.Imports {
		b, err := os.ReadFile(imp)
		if err != nil {
			return nil, nil, false
		}
		s := string(b)
		if filepath.Ext(imp) == ".json" {
			s = "export default " + s
		}
		sources[imp] = s
	}
	hash, key := e.programKeys(sources)
	if key != idx.CacheKey {
		return nil, nil, false
	}
	var ent compiledEntry
	if ok, err := e.Cache.load("programs", e.planKey(key), &ent); !ok || err != nil || ent.CacheKey != key {
		return nil, nil, false
	}
	return &Program{File: file, Hash: hash, Imports: ent.Imports, Operations: ent.Operations, CacheKey: key}, ent.Diagnostics, true
}

// programKeys computes Program.Hash and Program.CacheKey from all sources.
func (e *Engine) programKeys(sources map[string]string) (hash, cacheKey string) {
	h := sha256.New()
	keys := make([]string, 0, len(sources))
	for k := range sources {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		fmt.Fprintf(h, "%s\x00%s\x00", k, sources[k])
	}
	hash = "sha256:" + hex.EncodeToString(h.Sum(nil))[:32]
	ck := sha256.Sum256([]byte(hash + "|" + CompilerVersion + "|" + e.Registry.Capabilities().Fingerprint))
	return hash, "sha256:" + hex.EncodeToString(ck[:])[:32]
}

func (c *Cache) path(kind, key string) (string, error) {
	if c == nil || c.Dir == "" {
		return "", errors.New("codemode: cache is off")
	}
	if key == "" || strings.ContainsAny(key, `/\.`) {
		return "", fmt.Errorf("codemode: bad cache key %q", key)
	}
	return filepath.Join(c.Dir, kind, key+".json"), nil
}

func (c *Cache) load(kind, key string, v any) (bool, error) {
	if c == nil {
		return false, nil
	}
	p, err := c.path(kind, key)
	if err != nil {
		return false, err
	}
	b, err := os.ReadFile(p)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if err := json.Unmarshal(b, v); err != nil {
		return false, nil // corrupt entry = miss
	}
	return true, nil
}

func (c *Cache) save(kind, key string, v any) error {
	if c == nil {
		return nil
	}
	p, err := c.path(kind, key)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(p), ".tmp-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	if _, err := f.Write(b); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Chmod(0o600); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, p)
}
