package linkcore

import (
	"context"
	"log/slog"
	"os"
	"os/exec"
	"strconv"
	"time"
)

const (
	supervisorProbeTimeout = 2 * time.Second
	supervisorRetry        = 2 * time.Second
)

func powerAssertionArguments(processID int) []string {
	return []string{"-s", "-w", strconv.Itoa(processID)}
}

func holdSystemAwakeOnAC() {
	command := exec.Command("/usr/bin/caffeinate", powerAssertionArguments(os.Getpid())...)
	if err := command.Start(); err != nil {
		slog.Warn("Deploy Link could not request an AC power assertion", "errorCode", "power_assertion_failed")
		return
	}
	go func() {
		if err := command.Wait(); err != nil {
			slog.Warn("Deploy Link AC power assertion ended", "errorCode", "power_assertion_ended")
		}
	}()
}

func profileDaemonAvailable(ctx context.Context, profilePath string) bool {
	probeCtx, cancel := context.WithTimeout(ctx, supervisorProbeTimeout)
	defer cancel()
	return Call(probeCtx, profilePath, Request{Command: "status"}, nil) == nil
}

// Supervise keeps one profile daemon independent of the terminal or app that
// installed its LaunchAgent. It waits when an older foreground daemon already
// owns the profile socket, which permits a non-disruptive service handoff.
func Supervise(ctx context.Context, profilePath string) error {
	holdSystemAwakeOnAC()
	for ctx.Err() == nil {
		if profileDaemonAvailable(ctx, profilePath) {
			if !waitContext(ctx, supervisorRetry) {
				return nil
			}
			continue
		}
		config, err := LoadConfig(profilePath)
		if err != nil {
			return err
		}
		if err := ValidatePairRecord(config); err != nil {
			return err
		}
		daemon, err := NewDaemon(config, profilePath)
		if err != nil {
			return err
		}
		err = daemon.Serve(ctx)
		if ctx.Err() != nil {
			return nil
		}
		if err != nil && errorCode(err) != "deploy_link_already_running" {
			slog.Warn("Deploy Link supervised daemon ended", "errorCode", errorCode(err))
		}
		if !waitContext(ctx, supervisorRetry) {
			return nil
		}
	}
	return nil
}
