// Package model holds the public data types shared across the permits library:
// the dependencies discovered in lockfiles, the raw license artifacts gathered
// for them, and the run summary. These types are intentionally free of any
// ecosystem-specific logic so new providers can reuse them unchanged.
package model

import (
	"crypto/sha256"
	"encoding/hex"
	"time"
)

// MaxLicenseBytes caps how much of any single license file is retained. License
// files are small; this guards against a pathological or hostile input.
const MaxLicenseBytes = 1 << 20

// Ecosystem identifies the dependency ecosystem a [Dependency] belongs to.
type Ecosystem string

const (
	// EcosystemNPM is the npm/pnpm ecosystem.
	EcosystemNPM Ecosystem = "npm"
	// EcosystemGo is the Go module ecosystem.
	EcosystemGo Ecosystem = "go"
)

// Dependency is a single resolved dependency from a lockfile.
type Dependency struct {
	Ecosystem Ecosystem `json:"ecosystem"`
	Name      string    `json:"name"`
	Version   string    `json:"version"`
}

// Key returns a stable identity used for de-duplication and result lookup.
func (d Dependency) Key() string {
	return string(d.Ecosystem) + " " + d.Name + "@" + d.Version
}

// LicenseArtifact is one raw license file recovered for a dependency. A single
// dependency may yield several artifacts (e.g. dual LICENSE-MIT/LICENSE-APACHE).
type LicenseArtifact struct {
	Dependency

	// DeclaredLicense is the SPDX expression the package declares about itself
	// (npm package.json "license"); empty when not available (Go modules).
	DeclaredLicense string `json:"declaredLicense,omitempty"`
	// SPDX is the set of SPDX license identifiers detected in
	// [LicenseArtifact.Text]. A single file may yield several (e.g. a dual
	// MIT/Apache-2.0 LICENSE). Empty means the text could not be classified,
	// not that it is unlicensed.
	SPDX []string `json:"spdx"`
	// FileName is the original in-package filename, e.g. "LICENSE-MIT".
	FileName string `json:"fileName"`
	// Source records where the bytes came from: "npm-tarball", "go-cache",
	// "go-proxy", or a custom provider's label.
	Source string `json:"source"`
	// SHA256 is the lowercase hex digest of [LicenseArtifact.Text].
	SHA256 string `json:"sha256"`
	// Path is the slash-separated location of the written Markdown file,
	// relative to the output directory (i.e. to summary.json). It is populated
	// by output.Write; it is empty for artifacts obtained directly from the
	// library without writing to disk.
	Path string `json:"path,omitempty"`
	// Text is the verbatim license text.
	Text string `json:"-"`
	// RetrievedAt is when the artifact was fetched.
	RetrievedAt time.Time `json:"retrievedAt"`
}

// NewLicenseArtifact builds an artifact from raw license bytes, applying the
// [MaxLicenseBytes] cap and computing the SHA-256 digest. It is the single
// place providers construct artifacts so capping, hashing, and timestamping
// stay consistent across ecosystems. [LicenseArtifact.SPDX] is left empty for
// the collector to fill.
func NewLicenseArtifact(dep Dependency, declared, fileName, source string, raw []byte) LicenseArtifact {
	if len(raw) > MaxLicenseBytes {
		raw = raw[:MaxLicenseBytes]
	}

	sum := sha256.Sum256(raw)

	return LicenseArtifact{
		Dependency:      dep,
		DeclaredLicense: declared,
		FileName:        fileName,
		Source:          source,
		SHA256:          hex.EncodeToString(sum[:]),
		Text:            string(raw),
		RetrievedAt:     time.Now().UTC(),
	}
}

// Status describes the outcome of processing a single dependency.
type Status string

const (
	// StatusResolved means at least one license artifact was recovered.
	StatusResolved Status = "resolved"
	// StatusNoLicenseFound means the dependency was fetched but no license
	// file could be located.
	StatusNoLicenseFound Status = "no-license-found"
	// StatusFailed means an error occurred while processing the dependency.
	StatusFailed Status = "failed"
)

// DepResult is the per-dependency outcome recorded in a [Summary].
type DepResult struct {
	Dependency
	Status    Status            `json:"status"`
	Artifacts []LicenseArtifact `json:"artifacts,omitempty"`
	Error     string            `json:"error,omitempty"`
}

// Stats aggregates run counts.
type Stats struct {
	Total          int `json:"total"`
	Resolved       int `json:"resolved"`
	NoLicenseFound int `json:"noLicenseFound"`
	Failed         int `json:"failed"`
}

// Summary is the machine-readable result of a collection run.
type Summary struct {
	GeneratedAt  time.Time   `json:"generatedAt"`
	Dependencies []DepResult `json:"dependencies"`
	Stats        Stats       `json:"stats"`
}
