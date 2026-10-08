// Package user defines the protocol-independent synchronization model.
package user

import (
	"encoding/hex"
	"fmt"
	"strings"
)

type User struct {
	Email    string `json:"email"`
	ID       string `json:"id,omitempty"`
	Password string `json:"password,omitempty"`
	Flow     string `json:"flow,omitempty"`
	Level    uint32 `json:"level,omitempty"`
}

type Target struct {
	Tag         string
	Protocol    string
	EmailPrefix string
}

func Supported(protocol string) bool {
	return protocol == "vless" || protocol == "vmess" || protocol == "trojan"
}

func (u User) Credential(protocol string) string {
	if protocol == "trojan" {
		return u.Password
	}
	return u.ID
}

// Prepare validates the entire snapshot before any mutations and assigns owned
// emails. Errors deliberately omit credentials and user-provided field values.
func Prepare(target Target, users []User) ([]User, error) {
	if !Supported(target.Protocol) || target.EmailPrefix == "" {
		return nil, fmt.Errorf("invalid synchronization target")
	}
	result := make([]User, len(users))
	emails, credentials := map[string]bool{}, map[string]bool{}
	for i, u := range users {
		if u.Email == "" || u.Email != strings.TrimSpace(u.Email) || strings.ContainsAny(u.Email, "\r\n\t") {
			return nil, fmt.Errorf("user %d: email must be nonempty without surrounding whitespace or control characters", i)
		}
		u.Email = strings.ToLower(target.EmailPrefix + u.Email)
		if target.Protocol == "trojan" {
			if u.Password == "" || u.ID != "" || u.Flow != "" {
				return nil, fmt.Errorf("user %d: Trojan requires password and forbids id and flow", i)
			}
		} else {
			if !validUUID(u.ID) || u.Password != "" {
				return nil, fmt.Errorf("user %d: VLESS/VMess require a UUID id and forbid password", i)
			}
			u.ID = strings.ToLower(u.ID)
			if (target.Protocol == "vmess" && u.Flow != "") || (u.Flow != "" && u.Flow != "xtls-rprx-vision") {
				return nil, fmt.Errorf("user %d: unsupported flow for protocol", i)
			}
		}
		credential := u.Credential(target.Protocol)
		if emails[u.Email] || credentials[credential] {
			return nil, fmt.Errorf("user %d: duplicate email or credential", i)
		}
		emails[u.Email], credentials[credential] = true, true
		result[i] = u
	}
	return result, nil
}

func validUUID(s string) bool {
	if len(s) != 36 || s[8] != '-' || s[13] != '-' || s[18] != '-' || s[23] != '-' {
		return false
	}
	_, err := hex.DecodeString(strings.ReplaceAll(s, "-", ""))
	return err == nil
}
