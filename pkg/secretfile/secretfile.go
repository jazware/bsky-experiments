// Package secretfile reads a secret flag from the file named by <env>_FILE,
// so yeet can hand secrets over as tmpfs files instead of env vars.
package secretfile

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strings"

	"github.com/urfave/cli/v2"
)

// Flag returns a secret flag's value, or the contents of the file named by
// <env>_FILE when that's set. Setting both is an error, so a stale env var
// can't quietly win over a mounted file.
func Flag(cctx *cli.Context, flag, env string) (string, error) {
	path := os.Getenv(env + "_FILE")
	if path == "" {
		return cctx.String(flag), nil
	}
	var value string
	if cctx.IsSet(flag) {
		value = cctx.String(flag)
	}
	return Read(env, value, path)
}

// Read returns value, or the contents of the file at path less one trailing
// newline (what echo and most editors leave). Errors name the variable and
// path, never the value.
func Read(name, value, path string) (string, error) {
	if path == "" {
		return value, nil
	}
	if value != "" {
		return "", fmt.Errorf("set %s or %s_FILE, not both", name, name)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		var pe *fs.PathError
		if errors.As(err, &pe) {
			err = pe.Err
		}
		return "", fmt.Errorf("%s_FILE: can't read %s: %v", name, path, err)
	}
	s := string(b)
	if t, ok := strings.CutSuffix(s, "\r\n"); ok {
		s = t
	} else {
		s = strings.TrimSuffix(s, "\n")
	}
	if s == "" {
		return "", fmt.Errorf("%s_FILE: %s is empty", name, path)
	}
	return s, nil
}
