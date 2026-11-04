package connections

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
)

func storedProfile() Profile {
	return Profile{ID: "profile-one", Name: "Example database", Host: "selected.invalid", Port: "5432", Database: "example", User: "reader", SSLMode: "disable"}
}

func TestMissingConfigurationDoesNotCreateFiles(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing", ".scrunch")
	profiles, err := Load(path)
	if err != nil || len(profiles) != 0 {
		t.Fatalf("missing configuration returned %d profiles and %v", len(profiles), err)
	}
	if _, err := os.Stat(filepath.Dir(path)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("loading missing configuration created its directory")
	}
}

func TestSaveRoundTripStoresOnlyNonsecretSettings(t *testing.T) {
	const secret = "profile-environment-password-sentinel"
	t.Setenv("SCRUNCH_TEST_PROFILE_PASSWORD", secret)
	profile := storedProfile()
	profile.PasswordEnv = "SCRUNCH_TEST_PROFILE_PASSWORD"
	profile.SSLRootCert = "/approved/CA certificate.pem"
	profile.SSLCert, profile.SSLKey = "/approved/client.pem", "/approved/client.key"
	if _, err := profile.DSN(""); err != nil {
		t.Fatal(err)
	}
	parent := t.TempDir()
	if err := os.Chmod(parent, 0755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(parent, "new-directory", ".scrunch")
	if err := Save(path, []Profile{profile}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(data, []byte(secret)) || bytes.Contains(data, []byte(`"password":`)) || bytes.Contains(data, []byte(`"dsn":`)) {
		t.Fatal("saved configuration contains credentials or a connection string")
	}
	if !bytes.Contains(data, []byte(`"password_env": "SCRUNCH_TEST_PROFILE_PASSWORD"`)) {
		t.Fatal("password environment reference was not retained")
	}
	loaded, err := Load(path)
	if err != nil || len(loaded) != 1 || loaded[0] != profile {
		t.Fatalf("profile did not round-trip: %v", err)
	}
	for _, check := range []struct {
		path string
		mode os.FileMode
	}{
		{path, 0600}, {filepath.Dir(path), 0700}, {parent, 0755},
	} {
		info, err := os.Stat(check.path)
		if err != nil || info.Mode().Perm() != check.mode {
			t.Fatalf("unexpected configuration file/directory permissions: %v", err)
		}
	}
	if err := Save(path, nil); err != nil {
		t.Fatal(err)
	}
	loaded, err = Load(path)
	if err != nil || len(loaded) != 0 {
		t.Fatal("saving an empty profile list did not remove the remembered entries")
	}
}

func TestInvalidConfigurationIsRejectedWithoutEchoingContents(t *testing.T) {
	profile, err := json.Marshal(storedProfile())
	if err != nil {
		t.Fatal(err)
	}
	valid := `{"version":1,"connections":[` + string(profile) + `]}`
	cases := []struct{ name, document string }{
		{"unknown secret field", strings.Replace(valid, `"host":`, `"password":"sensitive-input-marker","host":`, 1)},
		{"unknown secret key", strings.Replace(valid, `"host":`, `"sensitive-input-marker":"x","host":`, 1)},
		{"raw DSN", strings.Replace(valid, `"host":`, `"dsn":"sensitive-input-marker","host":`, 1)},
		{"duplicate root key", strings.Replace(valid, `"version":1`, `"version":1,"version":1`, 1)},
		{"duplicate profile key", strings.Replace(valid, `"host":`, `"host":"other.invalid","host":`, 1)},
		{"duplicate profile ID", `{"version":1,"connections":[` + string(profile) + `,` + string(profile) + `]}`},
		{"unsupported version", strings.Replace(valid, `"version":1`, `"version":2`, 1)},
		{"fractional version", strings.Replace(valid, `"version":1`, `"version":1.0`, 1)},
		{"missing version", `{"connections":[]}`},
		{"missing profiles", `{"version":1}`},
		{"null array", `{"version":1,"connections":null}`},
		{"null field", strings.Replace(valid, `"user":"reader"`, `"user":null`, 1)},
		{"nested field", strings.Replace(valid, `"user":"reader"`, `"user":{"password":"sensitive-input-marker"}`, 1)},
		{"trailing document", valid + `{}`},
		{"invalid UTF8", strings.Replace(valid, "reader", "read\xffer", 1)},
		{"too large", valid + strings.Repeat(" ", maxFileBytes)},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), ".scrunch")
			if err := os.WriteFile(path, []byte(test.document), 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := Load(path); err == nil {
				t.Fatal("invalid configuration was accepted")
			} else if strings.Contains(err.Error(), "sensitive-input-marker") {
				t.Fatal("configuration error echoed secret input")
			}
			data, err := os.ReadFile(path)
			if err != nil || string(data) != test.document {
				t.Fatal("reading invalid configuration changed the file")
			}
		})
	}
}

func TestProfileCountAndValidationPreserveExistingConfiguration(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".scrunch")
	original := []byte("original configuration remains intact")
	if err := os.WriteFile(path, original, 0600); err != nil {
		t.Fatal(err)
	}
	tooMany := make([]Profile, MaxProfiles+1)
	for i := range tooMany {
		tooMany[i] = storedProfile()
		tooMany[i].ID = fmt.Sprintf("profile-%d", i)
	}
	missingName := storedProfile()
	missingName.Name = ""
	duplicate := storedProfile()
	for _, profiles := range [][]Profile{tooMany, {missingName}, {duplicate, duplicate}} {
		if err := Save(path, profiles); err == nil {
			t.Fatal("invalid profiles were saved")
		}
		data, err := os.ReadFile(path)
		if err != nil || !bytes.Equal(data, original) {
			t.Fatal("failed save changed original configuration")
		}
	}
	document, err := json.Marshal(struct {
		Version     int       `json:"version"`
		Connections []Profile `json:"connections"`
	}{1, tooMany})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, document, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Fatal("oversize profile count was loaded")
	}
}

func TestConfigurationRejectsSymlinksAndSharedPermissions(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "actual.json")
	if err := Save(path, []Profile{storedProfile()}); err != nil {
		t.Fatal(err)
	}
	original, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(directory, ".scrunch")
	if err := os.Symlink(path, link); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(link); err == nil {
		t.Fatal("symlink configuration was loaded")
	}
	if err := Save(link, nil); err == nil {
		t.Fatal("symlink configuration was replaced")
	}
	data, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(data, original) {
		t.Fatal("symlink target was changed")
	}
	if err := os.Chmod(path, 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Fatal("shared configuration was loaded")
	}
	if err := Save(directory, nil); err == nil {
		t.Fatal("directory was replaced by configuration")
	}
}

func clearPostgresTestEnvironment(t *testing.T) {
	t.Helper()
	for _, key := range []string{"PGHOST", "PGPORT", "PGDATABASE", "PGUSER", "PGPASSWORD", "PGAPPNAME", "PGCONNECT_TIMEOUT", "PGSSLMODE", "PGSSLKEY", "PGSSLCERT", "PGSSLSNI", "PGSSLROOTCERT", "PGSSLPASSWORD", "PGSSLNEGOTIATION", "PGTARGETSESSIONATTRS", "PGSERVICE", "PGSERVICEFILE", "PGTZ", "PGOPTIONS", "PGMINPROTOCOLVERSION", "PGMAXPROTOCOLVERSION", "PGCHANNELBINDING", "PGREQUIREAUTH"} {
		t.Setenv(key, "")
	}
	t.Setenv("PGPASSFILE", filepath.Join(t.TempDir(), "absent-password-file"))
}

func TestDSNRoundTripQuotesWithoutChangingCredentials(t *testing.T) {
	clearPostgresTestEnvironment(t)
	t.Setenv("PGHOST", "unselected.invalid")
	t.Setenv("PGPORT", "6543")
	t.Setenv("PGDATABASE", "unselected")
	t.Setenv("PGUSER", "unselected")
	t.Setenv("PGPASSWORD", "unselected-password")
	for _, password := range []string{"plain-password", "q'uote\\slash\n終", "raw\xffpassword"} {
		profile := storedProfile()
		profile.Host = "/tmp/socket directory's\\name"
		profile.Database = "data 'quoted' \\ 終"
		profile.User = "reader 'quoted' \\ 終"
		dsn, err := profile.DSN(password)
		if err != nil {
			t.Fatal(err)
		}
		parsed, err := pgconn.ParseConfig(dsn)
		if err != nil {
			t.Fatal("generated connection settings did not parse")
		}
		if parsed.Host != profile.Host || parsed.Port != 5432 || parsed.Database != profile.Database || parsed.User != profile.User || parsed.Password != password || parsed.TLSConfig != nil {
			t.Fatal("connection settings or credentials changed during quoting, or inherited another target")
		}
	}
}

func TestNamedPasswordVariableResolvesAtEveryExplicitRequest(t *testing.T) {
	clearPostgresTestEnvironment(t)
	profile := storedProfile()
	profile.PasswordEnv = "SCRUNCH_TEST_DYNAMIC_PASSWORD"
	for _, password := range []string{"first-credential", "rotated-credential"} {
		t.Setenv(profile.PasswordEnv, password)
		dsn, err := profile.DSN("")
		if err != nil {
			t.Fatal(err)
		}
		parsed, err := pgconn.ParseConfig(dsn)
		if err != nil || parsed.Password != password {
			t.Fatal("environment reference was not resolved for this connection")
		}
	}
	t.Setenv(profile.PasswordEnv, "")
	if dsn, err := profile.DSN(""); err == nil || dsn != "" {
		t.Fatal("empty password reference fell back silently")
	}
	if err := os.Unsetenv(profile.PasswordEnv); err != nil {
		t.Fatal(err)
	}
	if dsn, err := profile.DSN(""); err == nil || dsn != "" {
		t.Fatal("missing password reference fell back silently")
	}
	t.Setenv(profile.PasswordEnv, "environment-secret-marker")
	if dsn, err := profile.DSN("typed-secret-marker"); err == nil || dsn != "" {
		t.Fatal("both password sources were accepted")
	} else if strings.Contains(err.Error(), "secret-marker") {
		t.Fatal("password conflict echoed a credential")
	}
	profile.PasswordEnv = ""
	t.Setenv("PGPASSWORD", "intentional-standard-environment-password")
	dsn, err := profile.DSN("")
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := pgconn.ParseConfig(dsn)
	if err != nil || parsed.Password != "intentional-standard-environment-password" {
		t.Fatal("ordinary PostgreSQL password fallback was lost")
	}
}

func TestSelectedProfileOverridesInheritedServiceTarget(t *testing.T) {
	clearPostgresTestEnvironment(t)
	path := filepath.Join(t.TempDir(), "service.conf")
	service := "[unselected]\nhost=unselected.invalid\nport=6543\ndbname=unselected\nuser=unselected\nsslmode=require\n"
	if err := os.WriteFile(path, []byte(service), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PGSERVICEFILE", path)
	t.Setenv("PGSERVICE", "unselected")
	profile := storedProfile()
	dsn, err := profile.DSN("selected-password")
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := pgconn.ParseConfig(dsn)
	if err != nil {
		t.Fatal("profile plus inherited service did not parse")
	}
	if parsed.Host != profile.Host || parsed.Port != 5432 || parsed.Database != profile.Database || parsed.User != profile.User || parsed.TLSConfig != nil {
		t.Fatal("inherited service redirected the selected profile target or changed TLS mode")
	}
}

func TestInvalidPasswordReferencesAndTargetsFailBeforeDSN(t *testing.T) {
	for _, name := range []string{"1PASSWORD", "WITH SPACE", "ENV=VALUE", "$(command)", "NAME\nOTHER", "秘密"} {
		profile := storedProfile()
		profile.PasswordEnv = name
		if dsn, err := profile.DSN("secret-marker"); err == nil || dsn != "" {
			t.Fatal("invalid password reference was accepted")
		}
	}
	for _, host := range []string{"", "one.invalid,two.invalid", "host.invalid:5432", "postgres://user:secret-marker@host", "host invalid", "bad\x00host"} {
		profile := storedProfile()
		profile.Host = host
		if dsn, err := profile.DSN("secret-marker"); err == nil || dsn != "" {
			t.Fatal("invalid target was accepted")
		} else if strings.Contains(err.Error(), "secret-marker") {
			t.Fatal("target validation echoed sensitive input")
		}
	}
}

func TestEditableEnvironmentDefaultsContainNoPasswordValue(t *testing.T) {
	clearPostgresTestEnvironment(t)
	t.Setenv("PGHOST", "editable.invalid")
	t.Setenv("PGDATABASE", "editable")
	t.Setenv("PGUSER", "reader")
	t.Setenv("PGPASSWORD", "environment-secret-marker")
	profile := FromEnvironment()
	data, err := json.Marshal(profile)
	if err != nil {
		t.Fatal(err)
	}
	if profile.Host != "editable.invalid" || profile.Database != "editable" || profile.User != "reader" || profile.Port != "5432" {
		t.Fatal("editable defaults lost the configured target")
	}
	if profile.PasswordEnv != "" || bytes.Contains(data, []byte("environment-secret-marker")) {
		t.Fatal("editable defaults captured a password")
	}
}
