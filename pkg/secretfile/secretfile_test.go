package secretfile

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/urfave/cli/v2"
)

func writeFile(t *testing.T, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "secret")
	if err := os.WriteFile(p, []byte(content), 0o400); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestRead(t *testing.T) {
	for _, tc := range []struct{ name, content, want string }{
		{"no newline", "s3cret", "s3cret"},
		{"one newline", "s3cret\n", "s3cret"},
		{"crlf", "s3cret\r\n", "s3cret"},
		{"only one newline trimmed", "s3cret\n\n", "s3cret\n"},
		{"inner whitespace kept", " a b \n", " a b "},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Read("X", "", writeFile(t, tc.content))
			if err != nil {
				t.Fatal(err)
			}
			if got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}

	t.Run("no file", func(t *testing.T) {
		got, err := Read("X", "plain", "")
		if err != nil || got != "plain" {
			t.Fatalf("got %q, %v", got, err)
		}
	})

	for _, tc := range []struct{ name, value, content, want string }{
		{"both set", "plain-value", "file-value", "set X or X_FILE, not both"},
		{"empty file", "", "", "is empty"},
		{"newline only", "", "\n", "is empty"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Read("X", tc.value, writeFile(t, tc.content))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("got %v, want %q", err, tc.want)
			}
			if strings.Contains(err.Error(), "value") {
				t.Fatalf("error leaks a value: %v", err)
			}
		})
	}

	t.Run("missing file", func(t *testing.T) {
		p := filepath.Join(t.TempDir(), "nope")
		_, err := Read("X", "", p)
		if err == nil || err.Error() != "X_FILE: can't read "+p+": no such file or directory" {
			t.Fatalf("got %v", err)
		}
	})
}

// runFlags parses args and env through a cli.App declaring the flags the way
// the commands do, and returns what Flag resolves for both.
func runFlags(t *testing.T, args ...string) (string, error) {
	t.Helper()
	var got string
	var gotErr error
	app := &cli.App{
		Flags: []cli.Flag{
			&cli.StringFlag{Name: "bsky-pds-host", EnvVars: []string{"BSKY_PDS_HOST"}, Value: "https://default"},
			&cli.StringFlag{Name: "clickhouse-password", EnvVars: []string{"CLICKHOUSE_PASSWORD"}},
		},
		Action: func(cctx *cli.Context) error {
			if got, gotErr = Flag(cctx, "bsky-pds-host", "BSKY_PDS_HOST"); gotErr != nil {
				return nil
			}
			pw, err := Flag(cctx, "clickhouse-password", "CLICKHOUSE_PASSWORD")
			got += "|" + pw
			gotErr = err
			return nil
		},
	}
	if err := app.Run(append([]string{"atproto"}, args...)); err != nil {
		t.Fatal(err)
	}
	return got, gotErr
}

func TestFlag(t *testing.T) {
	t.Run("defaults", func(t *testing.T) {
		got, err := runFlags(t)
		if err != nil || got != "https://default|" {
			t.Fatalf("got %q, %v", got, err)
		}
	})

	t.Run("plain env", func(t *testing.T) {
		t.Setenv("BSKY_PDS_HOST", "https://env")
		t.Setenv("CLICKHOUSE_PASSWORD", "zPass")
		got, err := runFlags(t)
		if err != nil || got != "https://env|zPass" {
			t.Fatalf("got %q, %v", got, err)
		}
	})

	t.Run("files over defaults", func(t *testing.T) {
		t.Setenv("BSKY_PDS_HOST_FILE", writeFile(t, "https://file\n"))
		t.Setenv("CLICKHOUSE_PASSWORD_FILE", writeFile(t, "zFilePass\n"))
		got, err := runFlags(t)
		if err != nil || got != "https://file|zFilePass" {
			t.Fatalf("got %q, %v", got, err)
		}
	})

	t.Run("empty env is unset", func(t *testing.T) {
		t.Setenv("CLICKHOUSE_PASSWORD", "")
		t.Setenv("CLICKHOUSE_PASSWORD_FILE", writeFile(t, "zFilePass"))
		got, err := runFlags(t)
		if err != nil || got != "https://default|zFilePass" {
			t.Fatalf("got %q, %v", got, err)
		}
	})

	t.Run("env and file", func(t *testing.T) {
		t.Setenv("CLICKHOUSE_PASSWORD", "zEnvPass")
		t.Setenv("CLICKHOUSE_PASSWORD_FILE", writeFile(t, "zFilePass"))
		_, err := runFlags(t)
		if err == nil || err.Error() != "set CLICKHOUSE_PASSWORD or CLICKHOUSE_PASSWORD_FILE, not both" {
			t.Fatalf("got %v", err)
		}
	})

	t.Run("flag and file", func(t *testing.T) {
		t.Setenv("CLICKHOUSE_PASSWORD_FILE", writeFile(t, "zFilePass"))
		_, err := runFlags(t, "--clickhouse-password", "zFlagPass")
		if err == nil || !strings.Contains(err.Error(), "not both") || strings.Contains(err.Error(), "zFlagPass") {
			t.Fatalf("got %v", err)
		}
	})
}
