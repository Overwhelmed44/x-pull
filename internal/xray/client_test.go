package xray

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/x-pull/x-pull/internal/reconcile"
	"github.com/x-pull/x-pull/internal/user"
	"github.com/xtls/xray-core/app/commander"
	"github.com/xtls/xray-core/app/dispatcher"
	"github.com/xtls/xray-core/app/proxyman"
	"github.com/xtls/xray-core/app/proxyman/command"
	_ "github.com/xtls/xray-core/app/proxyman/inbound"
	_ "github.com/xtls/xray-core/app/proxyman/outbound"
	xnet "github.com/xtls/xray-core/common/net"
	"github.com/xtls/xray-core/common/protocol"
	"github.com/xtls/xray-core/common/serial"
	"github.com/xtls/xray-core/core"
	"github.com/xtls/xray-core/proxy/trojan"
	vlessin "github.com/xtls/xray-core/proxy/vless/inbound"
	vmessin "github.com/xtls/xray-core/proxy/vmess/inbound"
	_ "github.com/xtls/xray-core/transport/internet/tcp"
	"google.golang.org/protobuf/proto"
)

// This test runs the actual Xray core and its HandlerService, rather than a
// fake gRPC server, to verify runtime account conversion and reconciliation.
func TestLiveXraySynchronization(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	listener.Close()
	config := &core.Config{App: []*serial.TypedMessage{
		serial.ToTypedMessage(&proxyman.InboundConfig{}),
		serial.ToTypedMessage(&proxyman.OutboundConfig{}),
		serial.ToTypedMessage(&dispatcher.Config{}),
		serial.ToTypedMessage(&commander.Config{Tag: "api", Listen: address, Service: []*serial.TypedMessage{serial.ToTypedMessage(&command.Config{})}}),
	}}
	for _, kind := range []string{"vless", "vmess", "trojan"} {
		portListener, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		port := uint32(portListener.Addr().(*net.TCPAddr).Port)
		portListener.Close()
		static := user.User{Email: "static@example.com", ID: "6ba7b812-9dad-41d1-80b4-00c04fd430c8"}
		if kind == "trojan" {
			static.ID = ""
			static.Password = "static-password"
		}
		account, _ := encode(static, kind)
		p := &protocol.User{Email: static.Email, Account: serial.ToTypedMessage(account)}
		var settings proto.Message
		switch kind {
		case "vless":
			settings = &vlessin.Config{Clients: []*protocol.User{p}, Decryption: "none"}
		case "vmess":
			settings = &vmessin.Config{User: []*protocol.User{p}}
		case "trojan":
			settings = &trojan.ServerConfig{Users: []*protocol.User{p}}
		}
		config.Inbound = append(config.Inbound, &core.InboundHandlerConfig{Tag: kind,
			ReceiverSettings: serial.ToTypedMessage(&proxyman.ReceiverConfig{Listen: xnet.NewIPOrDomain(xnet.LocalHostIP), PortList: &xnet.PortList{Range: []*xnet.PortRange{{From: port, To: port}}}}),
			ProxySettings:    serial.ToTypedMessage(settings)})
	}
	instance, err := core.New(config)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { instance.Close() }()
	if err := instance.Start(); err != nil {
		t.Fatal(err)
	}
	c, err := New(address, 3*time.Second, 4<<20)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	r := reconcile.New(c)
	for _, kind := range []string{"vless", "vmess", "trojan"} {
		t.Run(kind, func(t *testing.T) {
			target := user.Target{Tag: kind, Protocol: kind, EmailPrefix: "pull:"}
			u := user.User{Email: "alice@example.com", ID: "6ba7b810-9dad-41d1-80b4-00c04fd430c8", Level: 1}
			if kind == "trojan" {
				u.ID = ""
				u.Password = "test-password"
			}
			result, err := r.Sync(ctx, target, []user.User{u}, false)
			if err != nil || result.Added != 1 {
				t.Fatalf("initial add: %+v %v", result, err)
			}
			result, err = r.Sync(ctx, target, []user.User{u}, false)
			if err != nil || result != (reconcile.Result{}) {
				t.Fatalf("idempotent sync: %+v %v", result, err)
			}
			u.Level = 2
			if kind == "vless" {
				u.Flow = "xtls-rprx-vision"
			}
			result, err = r.Sync(ctx, target, []user.User{u}, false)
			if err != nil || result.Updated != 1 {
				t.Fatalf("update: %+v %v", result, err)
			}
			// Real Trojan validators overwrite duplicate password keys; the
			// reconciler must catch unmanaged credential collisions before adding.
			conflict := u
			conflict.Email = "conflict"
			if kind == "trojan" {
				conflict.Password = "static-password"
			} else {
				conflict.ID = "6ba7b812-9dad-41d1-80b4-00c04fd430c8"
			}
			if _, err := r.Sync(ctx, target, []user.User{conflict}, false); err == nil {
				t.Fatal("accepted unmanaged credential collision")
			}
			result, err = r.Sync(ctx, target, nil, true)
			if err != nil || result.Removed != 1 {
				t.Fatalf("remove: %+v %v", result, err)
			}
			users, err := c.List(ctx, target)
			if err != nil || len(users) != 1 || users[0].Email != "static@example.com" {
				t.Fatalf("unmanaged user not preserved: %v", err)
			}
			// Simulate runtime users disappearing after a core restart.
			if _, err := r.Sync(ctx, target, []user.User{u}, false); err != nil {
				t.Fatal(err)
			}
			if err := c.Remove(ctx, target, "pull:"+u.Email); err != nil {
				t.Fatal(err)
			}
			result, err = r.Sync(ctx, target, []user.User{u}, false)
			if err != nil || result.Added != 1 {
				t.Fatalf("restore runtime state: %+v %v", result, err)
			}
		})
	}
	// Keep the companion's connection open while the actual core restarts.
	if err := instance.Close(); err != nil {
		t.Fatal(err)
	}
	instance, err = core.New(config)
	if err != nil {
		t.Fatal(err)
	}
	if err := instance.Start(); err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"vless", "vmess", "trojan"} {
		target := user.Target{Tag: kind, Protocol: kind, EmailPrefix: "pull:"}
		u := user.User{Email: "restored", ID: "6ba7b810-9dad-41d1-80b4-00c04fd430c8"}
		if kind == "trojan" {
			u.ID = ""
			u.Password = "restored-password"
		}
		for {
			result, syncErr := r.Sync(ctx, target, []user.User{u}, false)
			if syncErr == nil {
				if result.Added != 1 {
					t.Fatal("runtime user not restored after core restart")
				}
				break
			}
			select {
			case <-ctx.Done():
				t.Fatalf("reconnect after core restart: %v", syncErr)
			case <-time.After(25 * time.Millisecond):
			}
		}
	}
}
