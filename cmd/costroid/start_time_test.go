//go:build starttime

package main

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"slices"
	"testing"
	"time"
)

// This file reads the wall clock, so it is behind a build tag and stays out of
// go test ./... . make bench-start builds the binary as a release is built and
// names it in startBinaryVariable.

const (
	startBinaryVariable = "COSTROID_START_BINARY"
	startRuns           = 30
	versionBar          = 100 * time.Millisecond
	policyBar           = time.Second
	// A policy with three rules that read the rows, so the pack runs each.
	startPolicy = "policy:\n  run_budget_usd: \"5\"\n  max_run_cost_usd: \"1\"\n  models: [\"claude-*\"]\n"
)

type startTimes []time.Duration

// timeStarts runs the binary runs times, a fresh process each time, and
// returns each process's wall time from start to exit, sorted.
func timeStarts(t *testing.T, binary string, args []string, runs int) startTimes {
	t.Helper()
	times := make(startTimes, 0, runs)
	for range runs {
		var stdout, stderr bytes.Buffer
		command := exec.Command(binary, args...)
		command.Stdout, command.Stderr = &stdout, &stderr
		started := time.Now()
		err := command.Run()
		elapsed := time.Since(started)
		if err != nil || stdout.Len() == 0 {
			t.Fatalf("%s %v: %v, stdout %q, stderr %q", binary, args, err, stdout.String(), stderr.String())
		}
		times = append(times, elapsed)
	}
	slices.Sort(times)
	return times
}

// at is the nearest-rank percentile, the one the per-PR view's benchmark uses.
func (times startTimes) at(percent int) time.Duration {
	return times[((len(times)-1)*percent+50)/100]
}

func (times startTimes) median() time.Duration {
	middle := len(times) / 2
	if len(times)%2 == 1 {
		return times[middle]
	}
	return (times[middle-1] + times[middle]) / 2
}

func (times startTimes) String() string {
	return fmt.Sprintf("min %s, median %s, p95 %s, max %s",
		times[0].Round(time.Microsecond), times.median().Round(time.Microsecond),
		times.at(95).Round(time.Microsecond), times[len(times)-1].Round(time.Microsecond))
}

func TestVersionAndPolicyP95HoldTheirBarsOverThirtyFreshProcesses(t *testing.T) {
	binary := os.Getenv(startBinaryVariable)
	if binary == "" {
		t.Fatalf("set %s to the binary make build writes", startBinaryVariable)
	}
	config := writeBaseConfig(t, startPolicy)
	for _, command := range []struct {
		line string
		args []string
		bar  time.Duration
	}{
		{"costroid --version", []string{"--version"}, versionBar},
		{"costroid policy", []string{"policy", "--rows", realRows, "--config", config, "--at", "2026-11-06T00:00:00Z"}, policyBar},
	} {
		times := timeStarts(t, binary, command.args, startRuns)
		fmt.Printf("%s, %d fresh processes on %d cores: %s; bar %s at p95\n",
			command.line, startRuns, runtime.NumCPU(), times, command.bar)
		if p95 := times.at(95); p95 >= command.bar {
			t.Errorf("%s: p95 %s is at or over %s", command.line, p95, command.bar)
		}
	}
}
