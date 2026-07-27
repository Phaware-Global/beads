package main

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCheckedCatalogIsCanonicalAndComplete(t *testing.T) {
	catalog := readCheckedCatalog(t)
	if len(catalog.Versions) != 122 || len(catalog.Exclusions.RepositoryOnlyStable) != 49 ||
		len(catalog.Exclusions.RepositoryOnlyPrereleases) != 3 {
		t.Fatalf("catalog counts = %d/%d/%d, want 122/49/3",
			len(catalog.Versions), len(catalog.Exclusions.RepositoryOnlyStable),
			len(catalog.Exclusions.RepositoryOnlyPrereleases))
	}
	refs := map[string]string{}
	for _, entry := range catalog.Versions {
		refs[entry.Version] = entry.Origin.Ref
	}
	if refs["v0.56.0"] == "" {
		t.Fatal("missing proxy-preserved v0.56.0")
	}
	if refs["v0.9.11"] != "refs/heads/main" || refs["v0.17.2"] != "refs/heads/main" {
		t.Fatalf("preserved non-tag refs = %q, %q", refs["v0.9.11"], refs["v0.17.2"])
	}
	catalog.Versions[0].Sum = ""
	if err := validateCatalog(catalog); err == nil {
		t.Fatal("validator accepted missing authenticated provenance")
	}
}

func TestCheckedCatalogRejectsWellFormedIdentitySubstitution(t *testing.T) {
	base := readCheckedCatalog(t)
	tests := []struct {
		name   string
		mutate func(*Catalog)
	}{
		{"version", func(c *Catalog) { c.Versions[13].Version = "v0.13.0" }},
		{"module sum", func(c *Catalog) { c.Versions[13].Sum = testH1 }},
		{"origin", func(c *Catalog) { c.Versions[13].Origin.Hash = strings.Repeat("a", 40) }},
		{"source zip", func(c *Catalog) { c.Versions[13].SourceZip.SHA256 = strings.Repeat("b", 64) }},
		{"exclusion", func(c *Catalog) { c.Exclusions.RepositoryOnlyStable[0] = "v0.57.11" }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			catalog := cloneCatalog(t, base)
			tc.mutate(&catalog)
			raw, err := encodeCatalog(catalog)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := decodeCatalog(raw); err == nil {
				t.Fatal("decodeCatalog accepted a well-formed identity substitution")
			}
		})
	}
}

func TestClassifyVersionsUsesProxyAsStableUniverse(t *testing.T) {
	stable, excluded, err := classifyVersions(
		[]string{"v1.1.2", "v1.1.0-rc.2", "v0.56.0", "v1.1.0-rc.1", "v0.9.1", "v1.2.0"},
		[]string{"v0.9.1", "v0.57.12", "v0.58.8-nosqlite", "v1.1.0-rc.1", "v1.1.2", "2026.218.0"},
	)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(stable, ","); got != "v0.9.1,v0.56.0,v1.1.2" {
		t.Fatalf("stable = %s", got)
	}
	if got := strings.Join(excluded.ProxyPrereleases, ","); got != "v1.1.0-rc.1,v1.1.0-rc.2" {
		t.Fatalf("proxy prereleases = %s", got)
	}
	if got := strings.Join(excluded.RepositoryOnlyStable, ","); got != "v0.57.12" {
		t.Fatalf("repository-only stable = %s", got)
	}
	if got := strings.Join(excluded.RepositoryOnlyPrereleases, ","); got != "v0.58.8-nosqlite" {
		t.Fatalf("repository-only prereleases = %s", got)
	}
}

func TestClassifyCatalogVersionsIncludesNewRemoteOnlyTags(t *testing.T) {
	_, excluded, err := classifyCatalogVersions(
		[]string{"v0.9.1", "v1.1.2"},
		[]string{"v0.57.12"},
		map[string]string{
			"v0.9.1":           strings.Repeat("a", 40),
			"v0.58.8-nosqlite": strings.Repeat("b", 40),
			"v1.1.2":           strings.Repeat("c", 40),
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(excluded.RepositoryOnlyStable, ","); got != "v0.57.12" {
		t.Fatalf("repository-only stable = %s", got)
	}
	if got := strings.Join(excluded.RepositoryOnlyPrereleases, ","); got != "v0.58.8-nosqlite" {
		t.Fatalf("repository-only prereleases = %s", got)
	}
}

func TestCatalogEntryHashesExactProxyZip(t *testing.T) {
	zipPath := filepath.Join(t.TempDir(), "module.zip")
	content := []byte("exact proxy zip bytes")
	if err := os.WriteFile(zipPath, content, 0o600); err != nil {
		t.Fatal(err)
	}
	download := downloadJSON{Version: "v0.9.1", Sum: testH1, GoModSum: testH1, Zip: zipPath,
		Origin: Origin{Hash: strings.Repeat("a", 40), Ref: "refs/tags/v0.9.1"}}
	entry, err := catalogEntry(download, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	want := fmt.Sprintf("%x", sha256.Sum256(content))
	if entry.SourceZip.SHA256 != want || entry.SourceZip.Size != int64(len(content)) {
		t.Fatalf("source zip = %+v, want sha256 %s size %d", entry.SourceZip, want, len(content))
	}
}

const testH1 = "h1:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="

func readCheckedCatalog(t *testing.T) Catalog {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "release-catalog.json"))
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := decodeCatalog(raw)
	if err != nil {
		t.Fatal(err)
	}
	return catalog
}

func cloneCatalog(t *testing.T, catalog Catalog) Catalog {
	t.Helper()
	raw, err := json.Marshal(catalog)
	if err != nil {
		t.Fatal(err)
	}
	var clone Catalog
	if err := json.Unmarshal(raw, &clone); err != nil {
		t.Fatal(err)
	}
	return clone
}
