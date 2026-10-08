// Package config loads and validates service configuration.
package config

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/x-pull/x-pull/internal/user"
)

type Duration time.Duration

func (d *Duration) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return fmt.Errorf("duration must be a string such as 30s")
	}
	v, err := time.ParseDuration(s)
	if err != nil {
		return fmt.Errorf("invalid duration")
	}
	*d = Duration(v)
	return nil
}

type Config struct {
	PollInterval Duration  `json:"poll_interval"`
	Workers      int       `json:"workers"`
	Xray         Xray      `json:"xray"`
	HTTP         HTTP      `json:"http"`
	Inbounds     []Inbound `json:"inbounds"`
}

type Xray struct {
	Address string   `json:"address"`
	Timeout Duration `json:"timeout"`
}

type HTTP struct {
	Timeout          Duration `json:"timeout"`
	MaxResponseBytes int64    `json:"max_response_bytes"`
}

type Inbound struct {
	Tag         string `json:"tag"`
	Protocol    string `json:"protocol"`
	EmailPrefix string `json:"email_prefix"`
	AllowEmpty  bool   `json:"allow_empty"`
	Source      Source `json:"source"`
}

type Source struct {
	URL        string            `json:"url"`
	Headers    map[string]string `json:"headers"`
	HeadersEnv map[string]string `json:"headers_env"`
}

func (i Inbound) Target() user.Target {
	return user.Target{Tag: i.Tag, Protocol: i.Protocol, EmailPrefix: i.EmailPrefix}
}

func Load(path string) (Config, error) {
	f, err := os.Open(path)
	if err != nil {
		return Config{}, fmt.Errorf("open config: %w", err)
	}
	defer f.Close()
	c := Config{PollInterval: Duration(30 * time.Second), Workers: 4,
		Xray: Xray{Address: "127.0.0.1:10085", Timeout: Duration(5 * time.Second)},
		HTTP: HTTP{Timeout: Duration(10 * time.Second), MaxResponseBytes: 4 << 20}}
	d := json.NewDecoder(io.LimitReader(f, 1<<20))
	d.DisallowUnknownFields()
	if err := d.Decode(&c); err != nil {
		return Config{}, fmt.Errorf("invalid config JSON: %w", err)
	}
	if err := d.Decode(new(any)); err != io.EOF {
		return Config{}, fmt.Errorf("config must contain exactly one JSON object")
	}
	if err := c.Validate(); err != nil {
		return Config{}, err
	}
	return c, nil
}

func (c *Config) Validate() error {
	if c.PollInterval <= 0 || c.Xray.Timeout <= 0 || c.HTTP.Timeout <= 0 {
		return fmt.Errorf("all intervals and timeouts must be positive")
	}
	if c.Workers < 1 || c.Workers > 256 {
		return fmt.Errorf("workers must be between 1 and 256")
	}
	if c.HTTP.MaxResponseBytes < 1 || c.HTTP.MaxResponseBytes > 64<<20 {
		return fmt.Errorf("max_response_bytes must be between 1 and 67108864")
	}
	host, port, err := net.SplitHostPort(c.Xray.Address)
	if err != nil || host == "" || port == "" {
		return fmt.Errorf("xray.address must be host:port")
	}
	if len(c.Inbounds) == 0 {
		return fmt.Errorf("at least one inbound is required")
	}
	tags := map[string]bool{}
	for idx := range c.Inbounds {
		i := &c.Inbounds[idx]
		if i.Tag == "" || tags[i.Tag] {
			return fmt.Errorf("inbound %d: tag must be nonempty and unique", idx)
		}
		tags[i.Tag] = true
		if !user.Supported(i.Protocol) {
			return fmt.Errorf("inbound %d: protocol must be vless, vmess, or trojan", idx)
		}
		if i.EmailPrefix == "" {
			i.EmailPrefix = "pull:"
		}
		i.EmailPrefix = strings.ToLower(i.EmailPrefix)
		if strings.TrimSpace(i.EmailPrefix) != i.EmailPrefix || strings.ContainsAny(i.EmailPrefix, "\r\n\t") {
			return fmt.Errorf("inbound %d: invalid email_prefix", idx)
		}
		u, err := url.Parse(i.Source.URL)
		if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.Fragment != "" {
			return fmt.Errorf("inbound %d: source.url must be an HTTP(S) URL without credentials or fragment", idx)
		}
		resolved := make(map[string]string)
		for k, v := range i.Source.Headers {
			if err := putHeader(resolved, k, v); err != nil {
				return fmt.Errorf("inbound %d: %w", idx, err)
			}
		}
		for k, name := range i.Source.HeadersEnv {
			v, exists := os.LookupEnv(name)
			if !exists || v == "" {
				return fmt.Errorf("inbound %d: header environment variable %q is missing or empty", idx, name)
			}
			if err := putHeader(resolved, k, v); err != nil {
				return fmt.Errorf("inbound %d: %w", idx, err)
			}
		}
		i.Source.Headers = resolved
		i.Source.HeadersEnv = nil
	}
	return nil
}

func putHeader(headers map[string]string, name, value string) error {
	if name == "" {
		return fmt.Errorf("empty HTTP header name")
	}
	for _, r := range name {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("!#$%&'*+-.^_`|~", r)) {
			return fmt.Errorf("invalid HTTP header name")
		}
	}
	name = http.CanonicalHeaderKey(name)
	if name == "Host" || name == "Content-Length" || name == "Transfer-Encoding" {
		return fmt.Errorf("reserved HTTP header")
	}
	for _, r := range value {
		if r == 127 || r < 32 && r != '\t' {
			return fmt.Errorf("invalid HTTP header value")
		}
	}
	if _, exists := headers[name]; exists {
		return fmt.Errorf("duplicate HTTP header")
	}
	headers[name] = value
	return nil
}
