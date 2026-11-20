package webdav

import (
	"errors"
	"fmt"
	"os"
	"strings"
)

// basicCredentials holds the parsed [basic] section of a WebDAV credential
// file. Values are private: they are never logged, never included in error
// text or CheckResult.Reason, and never serialized.
type basicCredentials struct {
	username string
	password string
}

// loadBasicCredentials reads and parses an INI credential file with a [basic]
// section containing username and password. The file must not be world-readable
// or group/other-writable (fail closed, matching the root:vshm 0640 policy).
// Error text never contains the path or any file content.
func loadBasicCredentials(path string) (*basicCredentials, error) {
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
	return parseBasicINI(string(data))
}

// parseBasicINI parses a minimal INI document and returns the [basic] section.
// Keys are case-insensitive; values may be double- or single-quoted; lines
// starting with ';' or '#' are comments.
func parseBasicINI(data string) (*basicCredentials, error) {
	creds := &basicCredentials{}
	inBasic := false
	foundBasic := false
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
			inBasic = section == "basic"
			if inBasic {
				foundBasic = true
			}
			continue
		}
		eq := strings.IndexByte(line, '=')
		if eq < 0 {
			return nil, fmt.Errorf("malformed INI line %d", lineNo+1)
		}
		if !inBasic {
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
			return nil, fmt.Errorf("duplicate key %q in [basic]", key)
		}
		seen[key] = true
		switch key {
		case "username":
			creds.username = val
		case "password":
			creds.password = val
		}
	}
	if !foundBasic {
		return nil, errors.New("INI file missing [basic] section")
	}
	if creds.username == "" {
		return nil, errors.New("[basic] missing username")
	}
	if creds.password == "" {
		return nil, errors.New("[basic] missing password")
	}
	return creds, nil
}
