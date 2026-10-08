package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func loadJSON(t *testing.T, s string) (Config, error) {
	t.Helper()
	p := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(p, []byte(s), 0600); err != nil {
		t.Fatal(err)
	}
	return Load(p)
}

func TestDefaultsAndHeaders(t *testing.T) {
	t.Setenv("XPULL_TEST_AUTH", "Bearer secret")
	c, err := loadJSON(t, `{"inbounds":[{"tag":"vless-in","protocol":"vless","source":{"url":"https://example.com/users","headers_env":{"authorization":"XPULL_TEST_AUTH"}}}]}`)
	if err != nil {
		t.Fatal(err)
	}
	if c.PollInterval != Duration(30*time.Second) || c.Workers != 4 || c.Inbounds[0].EmailPrefix != "pull:" || c.Inbounds[0].Source.Headers["Authorization"] != "Bearer secret" {
		t.Fatal("defaults or environment header resolution failed")
	}
}

func TestInvalidConfigurations(t *testing.T) {
	for _, s := range []string{
		`{}`, `null`, `{"unknown":true}`, `{"workers":0}`, `{"poll_interval":"-1s"}`, `{"poll_interval":30}`, `{} {}`,
		`{"inbounds":[{"tag":"a","protocol":"unsupported","source":{"url":"https://example.com"}}]}`,
		`{"inbounds":[{"tag":"a","protocol":"vless","source":{"url":"file:///tmp/users"}}]}`,
		`{"inbounds":[{"tag":"a","protocol":"vless","source":{"url":"https://user:secret@example.com"}}]}`,
		`{"inbounds":[{"tag":"a","protocol":"vless","source":{"url":"https://example.com","headers":{"X-A":"a\nb"}}}]}`,
		`{"inbounds":[{"tag":"a","protocol":"vless","source":{"url":"https://example.com","headers":{"X-A":"a","x-a":"b"}}}]}`,
		`{"inbounds":[{"tag":"a","protocol":"vless","source":{"url":"https://example.com","headers_env":{"Authorization":"XPULL_NONEXISTENT_TEST_ENV"}}}]}`,
		`{"inbounds":[{"tag":"a","protocol":"vless","source":{"url":"https://example.com"}},{"tag":"a","protocol":"trojan","source":{"url":"https://example.com"}}]}`,
	} {
		t.Run(s, func(t *testing.T) {
			if _, err := loadJSON(t, s); err == nil {
				t.Fatal("accepted invalid configuration")
			}
		})
	}
}
