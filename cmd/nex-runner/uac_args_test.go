package main

import (
	"reflect"
	"testing"
)

func TestBuildElevatedArgsLoopGuard(t *testing.T) {
	in := []string{"-server", "ws://127.0.0.1:8080/ws/runner", "-no-uac"}
	got := buildElevatedArgs(in)
	want := []string{"-server", "ws://127.0.0.1:8080/ws/runner", "-uac-attempted"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("buildElevatedArgs=%v, want %v", got, want)
	}

	// Should not duplicate -uac-attempted
	got2 := buildElevatedArgs(got)
	if !reflect.DeepEqual(got2, want) {
		t.Fatalf("buildElevatedArgs idempotent=%v, want %v", got2, want)
	}
}

func TestBuildAutostartCommandLineQuoting(t *testing.T) {
	cmdLine := buildAutostartCommandLine(
		`C:\Program Files\Nex\nex-runner.exe`,
		[]string{"-server", "wss://example.com/ws/runner", "-workspace", `D:\My Projects\Repo`, "-uac-attempted"},
	)
	want := `"C:\Program Files\Nex\nex-runner.exe" -server wss://example.com/ws/runner -workspace "D:\My Projects\Repo"`
	if cmdLine != want {
		t.Fatalf("buildAutostartCommandLine=%q, want %q", cmdLine, want)
	}
}

func TestHasFlagName(t *testing.T) {
	args := []string{"-server", "ws://x", "--no-uac=true"}
	if !hasFlagName(args, "no-uac") {
		t.Fatal("expected hasFlagName(no-uac)=true")
	}
	if hasFlagName(args, "uac-attempted") {
		t.Fatal("expected hasFlagName(uac-attempted)=false")
	}
}
