package mysql

import (
	"errors"
	"fmt"
	"os"
	"strings"
)

// clientCredentials holds the parsed [client] section of a MySQL credential
// file. Values are private: they are never logged, never included in error
// text, and never serialized.
type clientCredentials struct {
	user     string
	password string
	database string
}

// loadClientCredentials reads and parses an INI credential file with a
// [client] section containing user, password and an optional database.
// The file must not be world-readable or group/other-writable (fail closed,
// matching the root:vshm 0640 policy). Error text never contains the path
// or any file content.
func loadClientCredentials(path string) (*clientCredentials, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, errors.New("credential file not accessible")
	}
	if !info.Mode().IsRegular() {
		return nil, errors.New("credential file is not a regular file")
	}
	mode := info.Mode().Perm()
	if mode&0o004 != 0 {
		return nil, errors.New("credential file is world-readable")
	}
	if mode&0o022 != 0 {
		return nil, errors.New("credential file is group- or other-writable")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, errors.New("credential file not readable")
	}
	return parseClientINI(string(data))
}

// parseClientINI parses a minimal INI document and returns the [client]
// section. Keys are case-insensitive; values may be double- or
// single-quoted; lines starting with ';' or '#' are comments.
func parseClientINI(data string) (*clientCredentials, error) {
	creds := &clientCredentials{}
	inClient := false
	foundClient := false
	seen := map[string]bool{}
	for lineNo, raw := range strings.Split(data, "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, ";") || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.HasPrefix(line, "[") {
			if !strings.HasSuffix(line, "]") {
				return nil, fmt.Errorf("malformed INI section header on line %d", lineNo+1)
			}
			section := strings.ToLower(strings.TrimSpace(line[1 : len(line)-1]))
			inClient = section == "client"
			if inClient {
				foundClient = true
			}
			continue
		}
		eq := strings.IndexByte(line, '=')
		if eq < 0 {
			return nil, fmt.Errorf("malformed INI line %d", lineNo+1)
		}
		if !inClient {
			continue
		}
		key := strings.ToLower(strings.TrimSpace(line[:eq]))
		val := strings.TrimSpace(line[eq+1:])
		if len(val) >= 2 {
			if (val[0] == '"' && val[len(val)-1] == '"') || (val[0] == '\'' && val[len(val)-1] == '\'') {
				val = val[1 : len(val)-1]
			}
		}
		if seen[key] {
			return nil, fmt.Errorf("duplicate key %q in [client]", key)
		}
		seen[key] = true
		switch key {
		case "user":
			creds.user = val
		case "password":
			creds.password = val
		case "database":
			creds.database = val
		}
	}
	if !foundClient {
		return nil, errors.New("INI file missing [client] section")
	}
	if creds.user == "" {
		return nil, errors.New("[client] missing user")
	}
	if creds.password == "" {
		return nil, errors.New("[client] missing password")
	}
	return creds, nil
}
