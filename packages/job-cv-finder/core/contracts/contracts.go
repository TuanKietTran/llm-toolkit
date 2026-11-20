// Package contracts holds the JSON Schemas shared between the Go core and the Python
// intelligence worker, and validates payloads against them. The schema files are the
// source of truth; the Python worker's tests load the same files from disk.
package contracts

import (
	"bytes"
	"embed"
	"encoding/json"
	"fmt"
	"sync"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

//go:embed *.schema.json
var files embed.FS

// Names of the schemas, by file.
const (
	CandidateSnapshot = "candidate-snapshot.schema.json"
	MatchResult       = "match-result.schema.json"
	PrepareResult     = "prepare-result.schema.json"
)

var (
	once     sync.Once
	compiled map[string]*jsonschema.Schema
	loadErr  error
)

func load() {
	c := jsonschema.NewCompiler()
	c.AssertFormat()
	names := []string{CandidateSnapshot, MatchResult, PrepareResult}
	for _, n := range names {
		raw, err := files.ReadFile(n)
		if err != nil {
			loadErr = err
			return
		}
		doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
		if err != nil {
			loadErr = fmt.Errorf("%s: %w", n, err)
			return
		}
		if err := c.AddResource(n, doc); err != nil {
			loadErr = fmt.Errorf("%s: %w", n, err)
			return
		}
	}
	compiled = map[string]*jsonschema.Schema{}
	for _, n := range names {
		s, err := c.Compile(n)
		if err != nil {
			loadErr = fmt.Errorf("compile %s: %w", n, err)
			return
		}
		compiled[n] = s
	}
}

// Validate checks raw JSON against the named schema. The error lists every violation.
func Validate(name string, raw []byte) error {
	once.Do(load)
	if loadErr != nil {
		return loadErr
	}
	s, ok := compiled[name]
	if !ok {
		return fmt.Errorf("unknown contract %q", name)
	}
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
	if err != nil {
		return fmt.Errorf("invalid JSON: %w", err)
	}
	if err := s.Validate(doc); err != nil {
		return fmt.Errorf("contract %s: %w", name, err)
	}
	return nil
}

// ValidateValue marshals v and validates it, for producers inside Go.
func ValidateValue(name string, v any) error {
	raw, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return Validate(name, raw)
}
