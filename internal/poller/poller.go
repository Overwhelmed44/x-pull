package poller

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"
)

type Poller struct {
	config Config
	http   *http.Client
	xray   *xrayClient
}

func New(c Config) (*Poller, error) {
	xray, err := newXrayClient(c.Xray.Address, time.Duration(c.Xray.Timeout))
	if err != nil {
		return nil, err
	}
	return &Poller{config: c, http: &http.Client{Timeout: time.Duration(c.HTTP.Timeout)}, xray: xray}, nil
}

func (p *Poller) Close() error {
	p.http.CloseIdleConnections()
	return p.xray.conn.Close()
}

func (p *Poller) Run(ctx context.Context) {
	for ctx.Err() == nil {
		_ = p.Once(ctx)
		timer := time.NewTimer(time.Duration(p.config.PollInterval))
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}

func (p *Poller) Once(ctx context.Context) error {
	var failures []error
	for _, inbound := range p.config.Inbounds {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		users, err := p.fetch(ctx, inbound)
		if err == nil {
			err = p.sync(ctx, inbound, users)
		}
		if err != nil {
			err = fmt.Errorf("inbound %s: %w", inbound.Tag, err)
			log.Print(err)
			failures = append(failures, err)
		} else {
			log.Printf("inbound %s: synced %d users", inbound.Tag, len(users))
		}
	}
	return errors.Join(failures...)
}

func (p *Poller) fetch(ctx context.Context, inbound Inbound) ([]User, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, inbound.Source.URL, nil)
	if err != nil {
		return nil, err
	}
	for header, value := range inbound.Source.Headers {
		req.Header.Set(header, value)
	}
	resp, err := p.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP source: %s", resp.Status)
	}
	var snapshot struct {
		Users []User `json:"users"`
	}
	err = json.NewDecoder(resp.Body).Decode(&snapshot)
	return snapshot.Users, err
}

func (p *Poller) sync(ctx context.Context, inbound Inbound, users []User) error {
	current, err := p.xray.list(ctx, inbound)
	if err != nil {
		return err
	}
	desired := make(map[string]User, len(users))
	for _, u := range users {
		u.Email = strings.ToLower(inbound.EmailPrefix + u.Email)
		u.ID = strings.ToLower(u.ID)
		desired[u.Email] = u
	}
	// Remove stale/changed users first so credentials can move between emails.
	for email, u := range current {
		if next, exists := desired[email]; exists && next == u {
			continue
		}
		if err := p.xray.remove(ctx, inbound.Tag, email); err != nil {
			return err
		}
	}
	for email, u := range desired {
		if old, exists := current[email]; exists && old == u {
			continue
		}
		if err := p.xray.add(ctx, inbound, u); err != nil {
			return err
		}
	}
	return nil
}
