package reconcile

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/x-pull/x-pull/internal/user"
)

type memoryBackend struct {
	users        map[string]user.User
	ops          int
	listErr      bool
	failPassword string
	failRemove   string
}

func (b *memoryBackend) List(context.Context, user.Target) ([]user.User, error) {
	if b.listErr {
		return nil, errors.New("unavailable")
	}
	result := []user.User{}
	for _, u := range b.users {
		result = append(result, u)
	}
	return result, nil
}
func (b *memoryBackend) Add(_ context.Context, target user.Target, u user.User) error {
	b.ops++
	if b.failPassword == u.Password && b.failPassword != "" {
		return errors.New("injected add failure")
	}
	for _, existing := range b.users {
		if existing.Email == u.Email || existing.Credential(target.Protocol) == u.Credential(target.Protocol) {
			return errors.New("duplicate")
		}
	}
	b.users[u.Email] = u
	return nil
}
func (b *memoryBackend) Remove(_ context.Context, _ user.Target, email string) error {
	b.ops++
	if b.failRemove == email {
		return errors.New("injected remove failure")
	}
	delete(b.users, email)
	return nil
}
func memory(users ...user.User) *memoryBackend {
	b := &memoryBackend{users: map[string]user.User{}}
	for _, u := range users {
		b.users[u.Email] = u
	}
	return b
}

var target = user.Target{Tag: "test", Protocol: "trojan", EmailPrefix: "pull:"}

func TestConvergesPreservesUnmanagedAndRecoversAfterRestart(t *testing.T) {
	unmanaged := user.User{Email: "static", Password: "static-secret"}
	b := memory(unmanaged, user.User{Email: "pull:stale", Password: "stale-secret"}, user.User{Email: "pull:alice", Password: "old"})
	r := New(b)
	snapshot := []user.User{{Email: "alice", Password: "new"}, {Email: "bob", Password: "bob-secret", Level: 2}}
	result, err := r.Sync(context.Background(), target, snapshot, false)
	if err != nil || result != (Result{Added: 1, Updated: 1, Removed: 1}) {
		t.Fatalf("sync failed: %+v %v", result, err)
	}
	if b.users["static"] != unmanaged || len(b.users) != 3 {
		t.Fatal("unexpected final state")
	}
	ops := b.ops
	result, err = r.Sync(context.Background(), target, snapshot, false)
	if err != nil || result != (Result{}) || b.ops != ops {
		t.Fatal("unchanged snapshot caused mutations")
	}
	b.users = map[string]user.User{"static": unmanaged}
	result, err = r.Sync(context.Background(), target, snapshot, false)
	if err != nil || result.Added != 2 {
		t.Fatal("did not recover after Xray restart")
	}
}

func TestInvalidSnapshotAndListFailureNeverMutate(t *testing.T) {
	for _, snapshot := range [][]user.User{nil, {{Email: "bad"}}, {{Email: "a", Password: "same"}, {Email: "b", Password: "same"}}} {
		b := memory(user.User{Email: "pull:existing", Password: "keep"})
		if _, err := New(b).Sync(context.Background(), target, snapshot, false); err == nil || b.ops != 0 {
			t.Fatal("unsafe invalid snapshot handling")
		}
	}
	b := memory()
	b.listErr = true
	if _, err := New(b).Sync(context.Background(), target, []user.User{{Email: "a", Password: "new"}}, false); err == nil || b.ops != 0 {
		t.Fatal("unsafe API read failure")
	}
}

func TestExplicitEmptySnapshotOnlyRemovesOwnedUsers(t *testing.T) {
	b := memory(user.User{Email: "pull:a", Password: "a"}, user.User{Email: "static", Password: "s"})
	result, err := New(b).Sync(context.Background(), target, nil, true)
	if err != nil || result.Removed != 1 || len(b.users) != 1 || b.users["static"].Email != "static" {
		t.Fatal("empty snapshot handling failed")
	}
}

func TestRollbackAndNoPruningOnAddFailure(t *testing.T) {
	old := user.User{Email: "pull:alice", Password: "old"}
	stale := user.User{Email: "pull:stale", Password: "stale"}
	b := memory(old, stale)
	b.failPassword = "new"
	if _, err := New(b).Sync(context.Background(), target, []user.User{{Email: "alice", Password: "new"}}, false); err == nil {
		t.Fatal("expected failure")
	}
	if !reflect.DeepEqual(b.users, memory(old, stale).users) {
		t.Fatal("failed replacement lost old or stale users")
	}
	b.failPassword = ""
	if _, err := New(b).Sync(context.Background(), target, []user.User{{Email: "alice", Password: "new"}}, false); err != nil {
		t.Fatal("retry failed", err)
	}
}

func TestRenameAndCredentialSwap(t *testing.T) {
	b := memory(user.User{Email: "pull:old", Password: "same"})
	if _, err := New(b).Sync(context.Background(), target, []user.User{{Email: "new", Password: "same"}}, false); err != nil || len(b.users) != 1 || b.users["pull:new"].Password != "same" {
		t.Fatal("rename failed", err)
	}
	b = memory(user.User{Email: "pull:a", Password: "one"}, user.User{Email: "pull:b", Password: "two"})
	if result, err := New(b).Sync(context.Background(), target, []user.User{{Email: "a", Password: "two"}, {Email: "b", Password: "one"}}, false); err != nil || result != (Result{Updated: 2}) {
		t.Fatal("swap failed", result, err)
	}
	if b.users["pull:a"].Password != "two" || b.users["pull:b"].Password != "one" {
		t.Fatal("swap incorrect")
	}
}

func TestRemoveFailureAndUnmanagedCredentialConflict(t *testing.T) {
	old := user.User{Email: "pull:a", Password: "old"}
	b := memory(old)
	b.failRemove = "pull:a"
	if _, err := New(b).Sync(context.Background(), target, []user.User{{Email: "a", Password: "new"}}, false); err == nil || b.users["pull:a"] != old {
		t.Fatal("unsafe removal failure")
	}
	static := user.User{Email: "static", Password: "same"}
	stale := user.User{Email: "pull:stale", Password: "stale"}
	b = memory(static, stale)
	if _, err := New(b).Sync(context.Background(), target, []user.User{{Email: "a", Password: "same"}}, false); err == nil || !reflect.DeepEqual(b.users, memory(static, stale).users) {
		t.Fatal("unmanaged credential conflict lost users")
	}
}
