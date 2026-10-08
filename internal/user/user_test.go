package user

import "testing"

const testID = "6BA7B810-9DAD-41D1-80B4-00C04FD430C8"

func TestPrepare(t *testing.T) {
	target := Target{Protocol: "vless", EmailPrefix: "pull:"}
	got, err := Prepare(target, []User{{Email: "Alice@Example.com", ID: testID, Flow: "xtls-rprx-vision", Level: 2}})
	if err != nil || got[0].Email != "pull:alice@example.com" || got[0].ID != "6ba7b810-9dad-41d1-80b4-00c04fd430c8" {
		t.Fatalf("unexpected normalized snapshot: %v", err)
	}
	cases := []struct {
		name, protocol string
		users          []User
	}{
		{"invalid UUID", "vless", []User{{Email: "a", ID: "invalid"}}},
		{"missing email", "vless", []User{{ID: testID}}},
		{"email whitespace", "vless", []User{{Email: " a", ID: testID}}},
		{"wrong credential", "vless", []User{{Email: "a", ID: testID, Password: "secret"}}},
		{"duplicate normalized email", "vless", []User{{Email: "A", ID: testID}, {Email: "a", ID: "6ba7b811-9dad-41d1-80b4-00c04fd430c8"}}},
		{"duplicate credential", "vless", []User{{Email: "a", ID: testID}, {Email: "b", ID: testID}}},
		{"VMess flow", "vmess", []User{{Email: "a", ID: testID, Flow: "xtls-rprx-vision"}}},
		{"unsupported VLESS flow", "vless", []User{{Email: "a", ID: testID, Flow: "unknown"}}},
		{"missing Trojan password", "trojan", []User{{Email: "a"}}},
		{"Trojan id", "trojan", []User{{Email: "a", Password: "secret", ID: testID}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			target.Protocol = tc.protocol
			if _, err := Prepare(target, tc.users); err == nil {
				t.Fatal("accepted invalid snapshot")
			}
		})
	}
	for _, kind := range []string{"vmess", "trojan"} {
		target.Protocol = kind
		u := User{Email: "a", ID: testID}
		if kind == "trojan" {
			u.ID = ""
			u.Password = "secret"
		}
		if _, err := Prepare(target, []User{u}); err != nil {
			t.Fatal(err)
		}
	}
}
