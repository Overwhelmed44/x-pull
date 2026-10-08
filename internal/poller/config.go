package poller

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"
)

type Duration time.Duration

func (d *Duration) UnmarshalJSON(data []byte) error {
	var value string
	if err := json.Unmarshal(data, &value); err != nil {
		return err
	}
	duration, err := time.ParseDuration(value)
	*d = Duration(duration)
	return err
}

type Config struct {
	PollInterval Duration `json:"poll_interval"`
	Xray         struct {
		Address string   `json:"address"`
		Timeout Duration `json:"timeout"`
	} `json:"xray"`
	HTTP struct {
		Timeout Duration `json:"timeout"`
	} `json:"http"`
	Inbounds []Inbound `json:"inbounds"`
}

type Inbound struct {
	Tag         string `json:"tag"`
	Protocol    string `json:"protocol"`
	EmailPrefix string `json:"email_prefix"`
	Source      struct {
		URL        string            `json:"url"`
		Headers    map[string]string `json:"headers"`
		HeadersEnv map[string]string `json:"headers_env"`
	} `json:"source"`
}

type User struct {
	Email    string `json:"email"`
	ID       string `json:"id,omitempty"`
	Password string `json:"password,omitempty"`
	Flow     string `json:"flow,omitempty"`
	Level    uint32 `json:"level,omitempty"`
}

func LoadConfig(path string) (Config, error) {
	c := Config{PollInterval: Duration(30 * time.Second)}
	c.Xray.Address, c.Xray.Timeout = "127.0.0.1:10085", Duration(5*time.Second)
	c.HTTP.Timeout = Duration(10 * time.Second)
	data, err := os.ReadFile(path)
	if err != nil {
		return c, err
	}
	if err := json.Unmarshal(data, &c); err != nil {
		return c, err
	}
	if c.PollInterval <= 0 || c.Xray.Timeout <= 0 || c.HTTP.Timeout <= 0 {
		return c, fmt.Errorf("poll_interval and timeouts must be positive")
	}
	for i := range c.Inbounds {
		inbound := &c.Inbounds[i]
		if inbound.EmailPrefix == "" {
			inbound.EmailPrefix = "pull:"
		}
		inbound.EmailPrefix = strings.ToLower(inbound.EmailPrefix)
		if inbound.Source.Headers == nil {
			inbound.Source.Headers = map[string]string{}
		}
		for header, env := range inbound.Source.HeadersEnv {
			inbound.Source.Headers[header] = os.Getenv(env)
		}
	}
	return c, nil
}
