package main

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

func TestParseArgsCommands(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want command
	}{
		{
			name: "no command serves",
			args: nil,
			want: command{mode: "serve", cfgPath: "config.yaml"},
		},
		{
			name: "flags before the command",
			args: []string{"-config", "/etc/dolmen.yaml", "scrub"},
			want: command{mode: "scrub", cfgPath: "/etc/dolmen.yaml"},
		},
		{
			name: "flags after the command",
			args: []string{"scrub", "-config", "/etc/dolmen.yaml"},
			want: command{mode: "scrub", cfgPath: "/etc/dolmen.yaml"},
		},
		{
			name: "version",
			args: []string{"-version"},
			want: command{mode: "serve", cfgPath: "config.yaml", showVersion: true},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			got, err := parseArgs(tt.args, &stdout, &stderr)
			if err != nil {
				t.Fatalf("parseArgs(%v): %v\n%s", tt.args, err, stderr.String())
			}
			if got != tt.want {
				t.Errorf("parseArgs(%v) = %+v, want %+v", tt.args, got, tt.want)
			}
			if stdout.Len() > 0 || stderr.Len() > 0 {
				t.Errorf("parseArgs(%v) wrote out: %s%s", tt.args, stdout.String(), stderr.String())
			}
		})
	}
}

func TestParseArgsHelp(t *testing.T) {
	for _, args := range [][]string{
		{"-h"}, {"--help"}, {"-help"},
		{"scrub", "-h"}, {"-config", "x.yaml", "--help"},
	} {
		var stdout, stderr bytes.Buffer
		_, err := parseArgs(args, &stdout, &stderr)
		if !errors.Is(err, errHelp) {
			t.Errorf("parseArgs(%v) err = %v, want errHelp", args, err)
		}
		got := stdout.String()
		for _, want := range []string{
			"Usage:",
			"dolmen [flags] scrub",
			"-config",
			"-help",
			"-version",
		} {
			if !strings.Contains(got, want) {
				t.Errorf("parseArgs(%v) help missing %q:\n%s", args, want, got)
			}
		}
		if stderr.Len() > 0 {
			t.Errorf("parseArgs(%v) wrote to stderr: %s", args, stderr.String())
		}
	}
}

func TestParseArgsErrors(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want string // expected in the stderr message
	}{
		{name: "unknown flag", args: []string{"-nope"}, want: "flag provided but not defined: -nope"},
		{name: "unknown command", args: []string{"bogus"}, want: `unknown command "bogus"`},
		{name: "extra argument", args: []string{"scrub", "extra"}, want: `unexpected argument "extra"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			_, err := parseArgs(tt.args, &stdout, &stderr)
			if err == nil || errors.Is(err, errHelp) {
				t.Fatalf("parseArgs(%v) err = %v, want a plain error", tt.args, err)
			}
			got := stderr.String()
			if !strings.Contains(got, tt.want) {
				t.Errorf("parseArgs(%v) stderr missing %q:\n%s", tt.args, tt.want, got)
			}
			if !strings.Contains(got, "Usage:") {
				t.Errorf("parseArgs(%v) stderr has no usage:\n%s", tt.args, got)
			}
			if stdout.Len() > 0 {
				t.Errorf("parseArgs(%v) wrote to stdout: %s", tt.args, stdout.String())
			}
		})
	}
}
