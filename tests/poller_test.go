package tests

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/x-pull/x-pull/internal/poller"
	"github.com/xtls/xray-core/app/proxyman/command"
	"github.com/xtls/xray-core/common/protocol"
	"google.golang.org/grpc"
)

type api struct {
	command.UnimplementedHandlerServiceServer
	mu     sync.Mutex
	users  map[string]*protocol.User
	writes int
}

func (a *api) GetInboundUsers(context.Context, *command.GetInboundUserRequest) (*command.GetInboundUserResponse, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	response := &command.GetInboundUserResponse{}
	for _, u := range a.users {
		response.Users = append(response.Users, u)
	}
	return response, nil
}

func (a *api) AlterInbound(_ context.Context, req *command.AlterInboundRequest) (*command.AlterInboundResponse, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	op, err := req.Operation.GetInstance()
	if err != nil {
		return nil, err
	}
	switch op := op.(type) {
	case *command.AddUserOperation:
		a.users[op.User.Email] = op.User
	case *command.RemoveUserOperation:
		delete(a.users, op.Email)
	}
	a.writes++
	return &command.AlterInboundResponse{}, nil
}

func TestPollingSynchronizesUsersAndRetriesHTTPFailures(t *testing.T) {
	backend := &api{users: map[string]*protocol.User{"static": {Email: "static"}}}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := grpc.NewServer()
	command.RegisterHandlerServiceServer(server, backend)
	go server.Serve(listener)
	defer server.Stop()
	var mu sync.Mutex
	status := http.StatusOK
	snapshot := []poller.User{{Email: "alice", ID: "6ba7b810-9dad-41d1-80b4-00c04fd430c8"}}
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if r.Header.Get("Authorization") != "Bearer test" {
			t.Error("missing authentication header")
		}
		w.WriteHeader(status)
		json.NewEncoder(w).Encode(map[string]any{"users": snapshot, "extra_admin_field": "ignored"})
	}))
	defer source.Close()
	t.Setenv("XPULL_TEST_AUTH", "Bearer test")
	path := filepath.Join(t.TempDir(), "config.json")
	data, _ := json.Marshal(map[string]any{"xray": map[string]any{"address": listener.Addr().String()}, "inbounds": []any{
		map[string]any{"tag": "vless-in", "protocol": "vless", "source": map[string]any{"url": source.URL, "headers_env": map[string]string{"Authorization": "XPULL_TEST_AUTH"}}},
	}})
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	c, err := poller.LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	p, err := poller.New(c)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	ctx := context.Background()
	if err := p.Once(ctx); err != nil {
		t.Fatal(err)
	}
	if backend.users["pull:alice"] == nil || backend.users["static"] == nil {
		t.Fatal("user addition or ownership failed")
	}
	writes := backend.writes
	if err := p.Once(ctx); err != nil || backend.writes != writes {
		t.Fatal("unchanged snapshot caused API writes", err)
	}
	mu.Lock()
	snapshot[0].Level = 2
	mu.Unlock()
	if err := p.Once(ctx); err != nil || backend.users["pull:alice"].Level != 2 {
		t.Fatal("user update failed", err)
	}
	mu.Lock()
	status = http.StatusServiceUnavailable
	mu.Unlock()
	if err := p.Once(ctx); err == nil || backend.users["pull:alice"] == nil {
		t.Fatal("HTTP failure handling failed")
	}
	mu.Lock()
	status = http.StatusOK
	snapshot = []poller.User{}
	mu.Unlock()
	if err := p.Once(ctx); err != nil || len(backend.users) != 1 || backend.users["static"] == nil {
		t.Fatal("user removal failed", err)
	}
}
