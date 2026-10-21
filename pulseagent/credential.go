// Credential persistence: the one piece of state an agent owns. Everything
// else — jobs, results, configuration — is disposable and rebuilt from the
// server, which is why wiping the data directory is a supported way to
// re-enroll.
package pulseagent

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// credentialFileMode is deliberately strict. The credential is a long-lived
// bearer token for this agent's identity; anything on the box that can read it
// can impersonate the agent to the server.
const credentialFileMode = 0o600

// Credential is the JSON document stored in the credential file.
type Credential struct {
	AgentID    string `json:"agent_id"`
	Credential string `json:"credential"`
}

// LoadCredential reads the credential file. A missing file is not an error —
// it is the normal first-start state — so it reports exists=false instead.
func LoadCredential(path string) (Credential, bool, error) {
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return Credential{}, false, nil
	}
	if err != nil {
		return Credential{}, false, fmt.Errorf("pulseagent: read credential %s: %w", path, err)
	}
	var c Credential
	if err := json.Unmarshal(raw, &c); err != nil {
		// Corrupt rather than fatal: the caller can fall back to re-registering
		// if a bootstrap token is configured, so surface it as an error on the
		// file the operator can see and fix.
		return Credential{}, true, fmt.Errorf("pulseagent: credential %s is corrupt: %w", path, err)
	}
	if c.AgentID == "" || c.Credential == "" {
		return Credential{}, true, fmt.Errorf("pulseagent: credential %s is missing agent_id or credential", path)
	}
	return c, true, nil
}

// SaveCredential writes the credential file with mode 0600 via a temp file and
// rename, so a crash mid-write can never leave a half-written credential that
// parses as empty, and the file never exists with looser permissions than
// intended.
func SaveCredential(path string, c Credential) error {
	raw, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return fmt.Errorf("pulseagent: encode credential: %w", err)
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("pulseagent: create credential dir %s: %w", dir, err)
	}
	tmp, err := os.CreateTemp(dir, ".credential-*")
	if err != nil {
		return fmt.Errorf("pulseagent: temp credential file: %w", err)
	}
	name := tmp.Name()
	defer os.Remove(name) // no-op after a successful rename
	if _, err := tmp.Write(raw); err != nil {
		tmp.Close()
		return fmt.Errorf("pulseagent: write credential: %w", err)
	}
	if err := tmp.Chmod(credentialFileMode); err != nil {
		tmp.Close()
		return fmt.Errorf("pulseagent: chmod credential: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("pulseagent: close credential: %w", err)
	}
	if err := os.Rename(name, path); err != nil {
		return fmt.Errorf("pulseagent: install credential %s: %w", path, err)
	}
	return nil
}

// RemoveCredential deletes the credential file, ignoring absence. Called when
// the server rejects the credential and re-registration will produce a fresh
// one — keeping the dead file around would only confuse the next start.
func RemoveCredential(path string) error {
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("pulseagent: remove credential %s: %w", path, err)
	}
	return nil
}
