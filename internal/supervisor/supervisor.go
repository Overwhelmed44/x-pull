// Package supervisor owns the two long-running processes inside the container.
package supervisor

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"syscall"
	"time"
)

type Process struct {
	Name    string
	Command string
	Args    []string
	Env     []string
}

type exited struct {
	name string
	err  error
}

// Run starts children in separate process groups and reaps every direct child.
// Cancellation is a clean stop; any unexpected child exit fails the container.
// The owner sends SIGTERM to all groups, then SIGKILL after the grace period.
func Run(ctx context.Context, processes []Process, grace time.Duration, log *slog.Logger) error {
	if len(processes) == 0 || grace <= 0 {
		return fmt.Errorf("processes and positive shutdown grace are required")
	}
	results := make(chan exited, len(processes))
	commands := make([]*exec.Cmd, 0, len(processes))
	remaining := 0
	var failure error
	for _, p := range processes {
		if ctx.Err() != nil {
			break
		}
		cmd := exec.Command(p.Command, p.Args...)
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		cmd.Env = append(os.Environ(), p.Env...)
		cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
		if err := cmd.Start(); err != nil {
			failure = fmt.Errorf("start %s: %w", p.Name, err)
			break
		}
		commands = append(commands, cmd)
		remaining++
		log.Info("child started", "process", p.Name, "pid", cmd.Process.Pid)
		go func(name string, c *exec.Cmd) { results <- exited{name: name, err: c.Wait()} }(p.Name, cmd)
	}
	if failure == nil && ctx.Err() == nil {
		select {
		case <-ctx.Done():
		case result := <-results:
			remaining--
			if ctx.Err() == nil {
				if result.err == nil {
					failure = fmt.Errorf("%s exited unexpectedly", result.name)
				} else {
					failure = fmt.Errorf("%s exited unexpectedly: %w", result.name, result.err)
				}
			}
		}
	}
	for _, cmd := range commands {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)
	}
	timer := time.NewTimer(grace)
	defer timer.Stop()
	for remaining > 0 {
		select {
		case result := <-results:
			remaining--
			log.Info("child stopped", "process", result.name)
		case <-timer.C:
			log.Warn("shutdown grace expired; killing child process groups")
			for _, cmd := range commands {
				_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
			}
		}
	}
	return failure
}
