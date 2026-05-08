package drift

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// WriteSignature writes a signature to <dir>/<id>.shape as
// pretty-printed sorted-key JSON. Output is byte-compatible with
// shape.py's writes (json.dump(..., indent=2, sort_keys=True))
// because encoding/json sorts map keys alphabetically.
func WriteSignature(dir, id string, sig any) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("mkdir %s: %w", dir, err)
	}
	b, err := json.MarshalIndent(sig, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal %s: %w", id, err)
	}
	path := filepath.Join(dir, id+".shape")
	if err := os.WriteFile(path, b, 0o600); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}

// ReadSignature reads a signature previously written by
// WriteSignature (or by the shape.py prototype, which writes the
// same format). Returns os.ErrNotExist if no file is present.
func ReadSignature(dir, id string) (any, error) {
	path := filepath.Join(dir, id+".shape")
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var sig any
	if err := json.Unmarshal(b, &sig); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	return sig, nil
}
