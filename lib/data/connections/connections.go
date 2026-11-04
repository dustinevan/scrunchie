// Package connections stores named PostgreSQL settings without passwords.
package connections

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/netip"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"unicode/utf8"
)

const (
	MaxProfiles  = 100
	maxFileBytes = 1 << 20
)

// Profile contains only named connection settings and certificate paths.
// PasswordEnv is an environment variable name, never its value. ID and Name
// may be empty for an unsaved connection, but are required in a saved file.
// AllowWrites opens writable sessions; absent or false means read-only.
type Profile struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Host        string `json:"host"`
	Port        string `json:"port"`
	Database    string `json:"database"`
	User        string `json:"user"`
	SSLMode     string `json:"ssl_mode"`
	SSLRootCert string `json:"ssl_root_cert,omitempty"`
	SSLCert     string `json:"ssl_cert,omitempty"`
	SSLKey      string `json:"ssl_key,omitempty"`
	PasswordEnv string `json:"password_env,omitempty"`
	AllowWrites bool   `json:"allow_writes,omitempty"`
}

// Validate checks connection settings without reading credentials or making a
// connection. Empty port and TLS mode mean 5432 and prefer, respectively.
func (p Profile) Validate() error {
	fields := []struct {
		name  string
		value string
		limit int
	}{
		{"ID", p.ID, 128},
		{"name", p.Name, 256},
		{"host", p.Host, 4096},
		{"port", p.Port, 5},
		{"database", p.Database, 1024},
		{"user", p.User, 1024},
		{"TLS mode", p.SSLMode, 16},
		{"root certificate path", p.SSLRootCert, 4096},
		{"client certificate path", p.SSLCert, 4096},
		{"client key path", p.SSLKey, 4096},
		{"password environment variable", p.PasswordEnv, 256},
	}
	for _, field := range fields {
		if len(field.value) > field.limit {
			return fmt.Errorf("validating connection: %s exceeds %d bytes", field.name, field.limit)
		}
		if !utf8.ValidString(field.value) || strings.IndexByte(field.value, 0) >= 0 {
			return fmt.Errorf("validating connection: %s must be UTF-8 without NUL bytes", field.name)
		}
	}
	if strings.TrimSpace(p.Host) == "" {
		return errors.New("validating connection: host is required")
	}
	if strings.Contains(p.Host, ",") {
		return errors.New("validating connection: use one host per connection")
	}
	if !strings.HasPrefix(p.Host, "/") {
		if strings.ContainsAny(p.Host, " /\\\t\r\n@") {
			return errors.New("validating connection: host must be a hostname, IP address, or absolute socket directory")
		}
		if strings.Contains(p.Host, ":") {
			if _, err := netip.ParseAddr(p.Host); err != nil {
				return errors.New("validating connection: enter the port separately from the host; IPv6 addresses have no brackets")
			}
		}
	}
	if p.Port != "" {
		for _, digit := range p.Port {
			if digit < '0' || digit > '9' {
				return errors.New("validating connection: port must be a number from 1 to 65535")
			}
		}
		port, err := strconv.Atoi(p.Port)
		if err != nil || port < 1 || port > 65535 {
			return errors.New("validating connection: port must be a number from 1 to 65535")
		}
	}
	if strings.TrimSpace(p.Database) == "" {
		return errors.New("validating connection: database is required")
	}
	if strings.TrimSpace(p.User) == "" {
		return errors.New("validating connection: user is required")
	}
	switch p.SSLMode {
	case "", "disable", "allow", "prefer", "require", "verify-ca", "verify-full":
	default:
		return errors.New("validating connection: TLS mode must be disable, allow, prefer, require, verify-ca, or verify-full")
	}
	if (p.SSLCert == "") != (p.SSLKey == "") {
		return errors.New("validating connection: client certificate and client key paths are both required")
	}
	for i, char := range p.PasswordEnv {
		if char == '_' || char >= 'A' && char <= 'Z' || char >= 'a' && char <= 'z' || i > 0 && char >= '0' && char <= '9' {
			continue
		}
		return errors.New("validating connection: password environment variable must contain letters, digits, or underscores and cannot start with a digit")
	}
	return nil
}

// DSN builds PostgreSQL keyword syntax for one explicit endpoint. A named
// password variable is read on each call and must be set and nonempty. If
// neither password source is selected, pgx retains its PGPASSWORD and pgpass
// behavior. The returned string contains credentials and must not be logged.
func (p Profile) DSN(password string) (string, error) {
	if err := p.Validate(); err != nil {
		return "", fmt.Errorf("building connection settings: %w", err)
	}
	if p.PasswordEnv != "" {
		if password != "" {
			return "", errors.New("building connection settings: choose a password or a password environment variable, not both")
		}
		value, present := os.LookupEnv(p.PasswordEnv)
		if !present || value == "" {
			return "", fmt.Errorf("building connection settings: password environment variable %s is missing or empty", p.PasswordEnv)
		}
		password = value
	}
	if strings.IndexByte(password, 0) >= 0 || len(password) > 65536 {
		return "", errors.New("building connection settings: password contains a NUL byte or exceeds 64 KiB")
	}
	port := p.Port
	if port == "" {
		port = "5432"
	}
	sslMode := p.SSLMode
	if sslMode == "" {
		sslMode = "prefer"
	}
	settings := []struct{ key, value string }{
		{"host", p.Host},
		{"port", port},
		{"dbname", p.Database},
		{"user", p.User},
		{"sslmode", sslMode},
		{"sslrootcert", p.SSLRootCert},
		{"sslcert", p.SSLCert},
		{"sslkey", p.SSLKey},
	}
	if password != "" {
		settings = append(settings, struct{ key, value string }{"password", password})
	}
	var dsn strings.Builder
	for i, setting := range settings {
		if i > 0 {
			dsn.WriteByte(' ')
		}
		dsn.WriteString(setting.key)
		dsn.WriteString("='")
		for i := 0; i < len(setting.value); i++ {
			char := setting.value[i]
			if char == '\\' || char == '\'' {
				dsn.WriteByte('\\')
			}
			dsn.WriteByte(char)
		}
		dsn.WriteByte('\'')
	}
	return dsn.String(), nil
}

// FromEnvironment returns editable defaults from the usual PostgreSQL target
// and certificate variables. It never reads a password or connects to a server.
func FromEnvironment() Profile {
	p := Profile{
		Host:        os.Getenv("PGHOST"),
		Port:        os.Getenv("PGPORT"),
		Database:    os.Getenv("PGDATABASE"),
		User:        os.Getenv("PGUSER"),
		SSLMode:     os.Getenv("PGSSLMODE"),
		SSLRootCert: os.Getenv("PGSSLROOTCERT"),
		SSLCert:     os.Getenv("PGSSLCERT"),
		SSLKey:      os.Getenv("PGSSLKEY"),
	}
	if p.Host == "" {
		p.Host = "localhost"
	}
	if p.Port == "" {
		p.Port = "5432"
	}
	if p.User == "" {
		// Read the shell's identity instead of using an OS directory service.
		p.User = os.Getenv("USER")
	}
	if p.User == "" {
		p.User = os.Getenv("USERNAME")
	}
	if p.User == "" {
		p.User = "postgres"
	}
	if p.Database == "" {
		p.Database = p.User
	}
	if p.SSLMode == "" {
		p.SSLMode = "prefer"
	}
	return p
}

// DefaultPath returns ~/.scrunch without creating or reading the file.
func DefaultPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("finding connection configuration: %w", err)
	}
	return filepath.Join(home, ".scrunch"), nil
}

// Load reads a version-1 JSON configuration with at most 100 profiles and a
// 1 MiB size limit. A missing file is empty; no file or directory is created.
// Symlinks, nonregular files, and group/other file access are rejected.
func Load(path string) ([]Profile, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("loading connections: %w", err)
	}
	if !info.Mode().IsRegular() {
		return nil, errors.New("loading connections: configuration must be a regular file, not a symlink or directory")
	}
	if info.Mode().Perm()&0077 != 0 {
		return nil, errors.New("loading connections: configuration permissions must allow only the owner (chmod 600)")
	}
	if info.Size() > maxFileBytes {
		return nil, errors.New("loading connections: configuration exceeds 1 MiB")
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("opening connection configuration: %w", err)
	}
	openedInfo, statErr := f.Stat()
	if statErr != nil || !os.SameFile(info, openedInfo) || openedInfo.Mode().Perm()&0077 != 0 {
		closeErr := f.Close()
		if statErr != nil {
			return nil, fmt.Errorf("checking connection configuration: %w", errors.Join(statErr, closeErr))
		}
		return nil, fmt.Errorf("checking connection configuration: %w", errors.Join(errors.New("configuration changed while opening; try again"), closeErr))
	}
	data, readErr := io.ReadAll(io.LimitReader(f, maxFileBytes+1))
	closeErr := f.Close()
	if readErr != nil || closeErr != nil {
		return nil, fmt.Errorf("reading connection configuration: %w", errors.Join(readErr, closeErr))
	}
	if len(data) > maxFileBytes {
		return nil, errors.New("loading connections: configuration exceeds 1 MiB")
	}
	if !utf8.Valid(data) {
		return nil, errors.New("loading connections: configuration must be UTF-8")
	}
	profiles, err := decode(data)
	if err != nil {
		return nil, fmt.Errorf("loading connections: %w", err)
	}
	if err := validateStored(profiles); err != nil {
		return nil, fmt.Errorf("loading connections: %w", err)
	}
	return profiles, nil
}

// Save atomically replaces a configuration with an owner-only file. Newly
// created directories are owner-only; existing directory modes are unchanged.
// Write/replacement errors preserve the original and retain a recovery file.
// A directory sync error is reported after a successful replacement.
func Save(path string, profiles []Profile) error {
	if strings.TrimSpace(path) == "" {
		return errors.New("saving connections: configuration path is empty")
	}
	if err := validateStored(profiles); err != nil {
		return fmt.Errorf("saving connections: %w", err)
	}
	if profiles == nil {
		profiles = []Profile{}
	}
	document := struct {
		Version     int       `json:"version"`
		Connections []Profile `json:"connections"`
	}{Version: 1, Connections: profiles}
	data, err := json.MarshalIndent(document, "", "  ")
	if err != nil {
		return errors.New("saving connections: could not encode configuration")
	}
	data = append(data, '\n')
	if len(data) > maxFileBytes {
		return errors.New("saving connections: configuration exceeds 1 MiB")
	}
	directory := filepath.Dir(path)
	if err := os.MkdirAll(directory, 0700); err != nil {
		return fmt.Errorf("creating connection configuration directory: %w", err)
	}
	info, err := os.Lstat(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("checking connection configuration: %w", err)
	}
	if err == nil && !info.Mode().IsRegular() {
		return errors.New("saving connections: configuration must be a regular file, not a symlink or directory")
	}
	f, err := os.CreateTemp(directory, ".scrunch-connections-*")
	if err != nil {
		return fmt.Errorf("creating connection configuration: %w", err)
	}
	writeErr := f.Chmod(0600)
	if writeErr == nil {
		_, writeErr = f.Write(data)
	}
	if writeErr == nil {
		writeErr = f.Sync()
	}
	closeErr := f.Close()
	if writeErr != nil || closeErr != nil {
		return fmt.Errorf("saving connections (recovery file %s): %w", f.Name(), errors.Join(writeErr, closeErr))
	}
	if err := os.Rename(f.Name(), path); err != nil {
		return fmt.Errorf("replacing connection configuration (recovery file %s): %w", f.Name(), err)
	}
	dir, err := os.Open(directory)
	if err != nil {
		return fmt.Errorf("opening connection configuration directory after replacement: %w", err)
	}
	syncErr := dir.Sync()
	closeErr = dir.Close()
	if syncErr != nil || closeErr != nil {
		return fmt.Errorf("syncing connection configuration directory after replacement: %w", errors.Join(syncErr, closeErr))
	}
	return nil
}

func validateStored(profiles []Profile) error {
	if len(profiles) > MaxProfiles {
		return errors.New("configuration exceeds 100 connections")
	}
	ids := make(map[string]bool, len(profiles))
	for i, profile := range profiles {
		if err := profile.Validate(); err != nil {
			return fmt.Errorf("checking connection %d: %w", i+1, err)
		}
		if strings.TrimSpace(profile.ID) == "" || strings.TrimSpace(profile.Name) == "" {
			return fmt.Errorf("checking connection %d: saved connections require an ID and name", i+1)
		}
		if ids[profile.ID] {
			return errors.New("configuration contains duplicate connection IDs")
		}
		ids[profile.ID] = true
	}
	return nil
}

// The configuration schema has only two nesting levels. Read its exact keys
// and scalar types so duplicate keys, null strings, and unknown secret fields
// cannot be accepted silently. Decoder errors never echo file contents.
func decode(data []byte) ([]Profile, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	token, err := decoder.Token()
	if err != nil || token != json.Delim('{') {
		return nil, errors.New("invalid configuration JSON object")
	}
	seen := make(map[string]bool)
	var profiles []Profile
	for decoder.More() {
		token, err = decoder.Token()
		key, ok := token.(string)
		if err != nil || !ok || seen[key] {
			return nil, errors.New("invalid or repeated configuration field")
		}
		seen[key] = true
		switch key {
		case "version":
			token, err = decoder.Token()
			if err != nil || token != json.Number("1") {
				return nil, errors.New("unsupported connection configuration version; expected 1")
			}
		case "connections":
			token, err = decoder.Token()
			if err != nil || token != json.Delim('[') {
				return nil, errors.New("configuration connections must be an array")
			}
			for decoder.More() {
				if len(profiles) >= MaxProfiles {
					return nil, errors.New("configuration exceeds 100 connections")
				}
				profile, err := decodeProfile(decoder)
				if err != nil {
					return nil, err
				}
				profiles = append(profiles, profile)
			}
			token, err = decoder.Token()
			if err != nil || token != json.Delim(']') {
				return nil, errors.New("invalid configuration connections array")
			}
		default:
			return nil, errors.New("unknown configuration field")
		}
	}
	token, err = decoder.Token()
	if err != nil || token != json.Delim('}') || !seen["version"] || !seen["connections"] {
		return nil, errors.New("configuration requires version and connections fields")
	}
	if _, err = decoder.Token(); !errors.Is(err, io.EOF) {
		return nil, errors.New("unexpected content after connection configuration")
	}
	return profiles, nil
}

func decodeProfile(decoder *json.Decoder) (Profile, error) {
	var profile Profile
	token, err := decoder.Token()
	if err != nil || token != json.Delim('{') {
		return profile, errors.New("each connection must be a JSON object")
	}
	seen := make(map[string]bool)
	for decoder.More() {
		token, err = decoder.Token()
		key, ok := token.(string)
		if err != nil || !ok || seen[key] {
			return profile, errors.New("invalid or repeated connection field")
		}
		seen[key] = true
		token, err = decoder.Token()
		if key == "allow_writes" {
			allow, ok := token.(bool)
			if err != nil || !ok {
				return profile, errors.New("connection allow_writes must be true or false")
			}
			profile.AllowWrites = allow
			continue
		}
		value, ok := token.(string)
		if err != nil || !ok {
			return profile, errors.New("connection fields must be JSON strings")
		}
		switch key {
		case "id":
			profile.ID = value
		case "name":
			profile.Name = value
		case "host":
			profile.Host = value
		case "port":
			profile.Port = value
		case "database":
			profile.Database = value
		case "user":
			profile.User = value
		case "ssl_mode":
			profile.SSLMode = value
		case "ssl_root_cert":
			profile.SSLRootCert = value
		case "ssl_cert":
			profile.SSLCert = value
		case "ssl_key":
			profile.SSLKey = value
		case "password_env":
			profile.PasswordEnv = value
		default:
			return profile, errors.New("unknown connection field; passwords and connection strings cannot be stored")
		}
	}
	token, err = decoder.Token()
	if err != nil || token != json.Delim('}') {
		return profile, errors.New("invalid connection JSON object")
	}
	return profile, nil
}
