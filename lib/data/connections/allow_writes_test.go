package connections

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAllowWritesRoundTripsAndDefaultsToReadOnly(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".scrunch")
	writable, readOnly := storedProfile(), storedProfile()
	writable.AllowWrites = true
	readOnly.ID, readOnly.Name = "profile-two", "Read-only database"
	if err := Save(path, []Profile{writable, readOnly}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Count(data, []byte(`"allow_writes": true`)) != 1 || bytes.Contains(data, []byte(`"allow_writes": false`)) {
		t.Fatalf("allow_writes was not stored only for the writable entry:\n%s", data)
	}
	loaded, err := Load(path)
	if err != nil || len(loaded) != 2 || loaded[0] != writable || loaded[1] != readOnly {
		t.Fatalf("profiles did not round-trip: %v", err)
	}
	if !loaded[0].AllowWrites || loaded[1].AllowWrites {
		t.Fatal("write modes changed while loading")
	}
}

func TestExistingConfigurationWithoutAllowWritesLoadsReadOnly(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".scrunch")
	document := `{"version":1,"connections":[{"id":"old","name":"Old entry","host":"selected.invalid","port":"5432","database":"example","user":"reader","ssl_mode":"disable"}]}`
	if err := os.WriteFile(path, []byte(document), 0600); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(path)
	if err != nil || len(loaded) != 1 || loaded[0].AllowWrites {
		t.Fatalf("old configuration got %+v, %v", loaded, err)
	}
	explicit := strings.Replace(document, `"ssl_mode":"disable"`, `"ssl_mode":"disable","allow_writes":false`, 1)
	if err := os.WriteFile(path, []byte(explicit), 0600); err != nil {
		t.Fatal(err)
	}
	if loaded, err := Load(path); err != nil || loaded[0].AllowWrites {
		t.Fatalf("explicit false got %+v, %v", loaded, err)
	}
}

func TestAllowWritesMustBeOneJSONBoolean(t *testing.T) {
	profile := `{"id":"p","name":"P","host":"selected.invalid","port":"5432","database":"d","user":"u","ssl_mode":"disable"`
	for name, field := range map[string]string{
		"string":     `,"allow_writes":"true"`,
		"number":     `,"allow_writes":1`,
		"null":       `,"allow_writes":null`,
		"object":     `,"allow_writes":{"password":"sensitive-input-marker"}`,
		"array":      `,"allow_writes":[true]`,
		"duplicate":  `,"allow_writes":false,"allow_writes":true`,
		"misspelled": `,"allow_write":true`,
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), ".scrunch")
			document := `{"version":1,"connections":[` + profile + field + `}]}`
			if err := os.WriteFile(path, []byte(document), 0600); err != nil {
				t.Fatal(err)
			}
			_, err := Load(path)
			if err == nil {
				t.Fatal("invalid allow_writes was accepted")
			}
			if strings.Contains(err.Error(), "sensitive-input-marker") {
				t.Fatal("configuration error echoed file contents")
			}
		})
	}
}
