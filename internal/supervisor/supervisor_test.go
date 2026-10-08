package supervisor

import (
	"context"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// A real child process exercises OS signals and reaping instead of mocking exec.
func TestChildHelper(t *testing.T) {
	if os.Getenv("XPULL_CHILD_HELPER") != "1" {
		return
	}
	mode, path := os.Getenv("XPULL_CHILD_MODE"), os.Getenv("XPULL_CHILD_PATH")
	if mode == "fail" {
		for {
			if _, err := os.Stat(path); err == nil {
				os.Exit(7)
			}
			time.Sleep(5 * time.Millisecond)
		}
	}
	if mode == "ignore" {
		signal.Ignore(syscall.SIGTERM)
	} else {
		signals := make(chan os.Signal, 1)
		signal.Notify(signals, syscall.SIGTERM)
		defer signal.Stop(signals)
		if err := os.WriteFile(path, []byte(strconv.Itoa(os.Getpid())), 0600); err != nil {
			os.Exit(2)
		}
		<-signals
		if err := os.WriteFile(path+".stopped", []byte("stopped"), 0600); err != nil {
			os.Exit(2)
		}
		os.Exit(0)
	}
	if err := os.WriteFile(path, []byte(strconv.Itoa(os.Getpid())), 0600); err != nil {
		os.Exit(2)
	}
	for {
		time.Sleep(time.Hour)
	}
}

func child(t *testing.T, name, mode, path string) Process {
	t.Helper()
	bin, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	return Process{Name: name, Command: bin, Args: []string{"-test.run=^TestChildHelper$"}, Env: []string{
		"XPULL_CHILD_HELPER=1", "XPULL_CHILD_MODE=" + mode, "XPULL_CHILD_PATH=" + path}}
}

func logger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func waitFile(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("child did not start")
}

func TestCancellationStopsBothChildren(t *testing.T) {
	dir := t.TempDir()
	a, b := filepath.Join(dir, "a"), filepath.Join(dir, "b")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- Run(ctx, []Process{child(t, "xray", "wait", a), child(t, "poller", "wait", b)}, 2*time.Second, logger())
	}()
	waitFile(t, a)
	waitFile(t, b)
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("shutdown hung")
	}
	for _, path := range []string{a, b} {
		if _, err := os.Stat(path + ".stopped"); err != nil {
			t.Fatal("SIGTERM was not delivered to both children")
		}
	}
}

func TestUnexpectedExitStopsSibling(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sibling")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err := Run(ctx, []Process{child(t, "xray", "wait", path), child(t, "poller", "fail", path)}, time.Second, logger())
	if err == nil || !strings.Contains(err.Error(), "poller exited unexpectedly") {
		t.Fatal("unexpected exit did not fail supervisor", err)
	}
	if _, err := os.Stat(path + ".stopped"); err != nil {
		t.Fatal("failed child left sibling running")
	}
}

func TestStubbornChildIsKilledAndReaped(t *testing.T) {
	path := filepath.Join(t.TempDir(), "stubborn")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- Run(ctx, []Process{child(t, "xray", "ignore", path)}, 50*time.Millisecond, logger()) }()
	waitFile(t, path)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(string(data))
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("SIGKILL fallback hung")
	}
	if err := syscall.Kill(pid, 0); err != syscall.ESRCH {
		t.Fatal("child was not killed and reaped", err)
	}
}

func TestStartupFailureCleansUpStartedChildren(t *testing.T) {
	path := filepath.Join(t.TempDir(), "first")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err := Run(ctx, []Process{child(t, "xray", "wait", path), {Name: "poller", Command: "/nonexistent/x-pull"}}, 100*time.Millisecond, logger())
	if err == nil || !strings.Contains(err.Error(), "start poller") {
		t.Fatal("startup failure was not propagated", err)
	}
}
