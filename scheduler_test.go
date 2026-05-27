package main

import (
	"slices"
	"strings"
	"testing"

	"fclones-wrapper/internal/parsing"

	"pgregory.net/rapid"
)

// --- Tests: buildScanArgs ---

func TestBuildScanArgs(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		want    []string
		cfg     config
		wantLen int
		wantErr bool
	}{
		{
			name:    "basic config",
			cfg:     config{ScanPath: "/data", Args: "--min-size 1024"},
			want:    []string{"group", "/data", "--min-size", "1024", "--cache"},
			wantLen: 5,
		},
		{
			name:    "invalid scan path quotes",
			cfg:     config{ScanPath: `"/unclosed`, Args: ""},
			wantErr: true,
		},
		{
			name:    "invalid args quotes",
			cfg:     config{ScanPath: "/data", Args: `--flag "unclosed`},
			wantErr: true,
		},
		{
			name:    "empty args",
			cfg:     config{ScanPath: "/data", Args: ""},
			wantLen: 3,
		},
		{
			name:    "extra args",
			cfg:     config{ScanPath: "/data", Args: "--min-size 1M --threads 4"},
			wantLen: 7,
		},
		{
			name:    "invalid scan path unterminated",
			cfg:     config{ScanPath: `"unterminated`},
			wantErr: true,
		},
		{
			name:    "invalid extra args unterminated",
			cfg:     config{ScanPath: "/data", Args: `"unterminated`},
			wantErr: true,
		},
		{
			name:    "multiple paths",
			cfg:     config{ScanPath: "/data /media /backup", Args: ""},
			want:    []string{"group", "/data", "/media", "/backup", "--cache"},
			wantLen: 5,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := buildScanArgs(&tt.cfg)
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got[0] != "group" {
				t.Errorf("first arg = %q, want \"group\"", got[0])
			}
			if got[len(got)-1] != "--cache" {
				t.Errorf("last arg = %q, want \"--cache\"", got[len(got)-1])
			}
			if tt.wantLen > 0 && len(got) != tt.wantLen {
				t.Errorf("len = %d, want %d: %v", len(got), tt.wantLen, got)
			}
			if tt.want != nil && !slices.Equal(got, tt.want) {
				t.Errorf("got %v, want %v", got, tt.want)
			}
		})
	}
}

// --- Tests: releaseJobSlot ---

func TestReleaseJobSlot(t *testing.T) {
	t.Parallel()
	js := &jobSlot{}
	js.TryAcquire()

	js.Release()

	js.mu.Lock()
	defer js.mu.Unlock()
	if js.running {
		t.Error("running should be false after Release")
	}
}

// --- Tests: buildActionArgs ---

func TestBuildActionArgs(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		want    []string
		cfg     config
		wantNil bool
		wantErr bool
	}{
		{
			name:    "group action returns nil",
			cfg:     config{Action: actionGroup},
			wantNil: true,
		},
		{
			name: "remove action no extra args",
			cfg:  config{Action: actionRemove},
			want: []string{"remove"},
		},
		{
			name: "link action with extra args",
			cfg:  config{Action: actionLink, ActionArgs: "--soft"},
			want: []string{"link", "--soft"},
		},
		{
			name: "dedupe action with multiple args",
			cfg:  config{Action: actionDedupe, ActionArgs: "--rf-over 1 --dry-run"},
			want: []string{"dedupe", "--rf-over", "1", "--dry-run"},
		},
		{
			name:    "invalid action args quotes",
			cfg:     config{Action: actionRemove, ActionArgs: `"unterminated`},
			wantErr: true,
		},
		{
			name: "action args with quoted path",
			cfg:  config{Action: actionRemove, ActionArgs: `--path "/my dir"`},
			want: []string{"remove", "--path", "/my dir"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := buildActionArgs(&tt.cfg)
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if tt.wantNil {
				if got != nil {
					t.Errorf("expected nil, got %v", got)
				}
				return
			}
			if !slices.Equal(got, tt.want) {
				t.Errorf("got %v, want %v", got, tt.want)
			}
		})
	}
}

func TestBuildActionArgsAllActions(t *testing.T) {
	t.Parallel()
	for _, action := range []action{actionRemove, actionLink, actionDedupe} {
		t.Run(string(action), func(t *testing.T) {
			cfg := &config{Action: action}
			got, err := buildActionArgs(cfg)
			if err != nil {
				t.Fatalf("buildActionArgs(%q): unexpected error: %v", action, err)
			}
			if len(got) != 1 {
				t.Fatalf("buildActionArgs(%q) = %v, want [%q]", action, got, action)
			}
			if got[0] != string(action) {
				t.Errorf("buildActionArgs(%q)[0] = %q, want %q", action, got[0], action)
			}
		})
	}
}

// --- Tests: countDuplicateFiles ---

func TestCountDuplicateFiles(t *testing.T) {
	t.Parallel()
	groups := []parsing.DuplicateGroup{
		{Keeper: "/a", Duplicates: []string{"/b", "/c"}},
		{Keeper: "/d", Duplicates: []string{"/e"}},
	}
	if got := countDuplicateFiles(groups); got != 3 {
		t.Errorf("countDuplicateFiles = %d, want 3", got)
	}
}

// --- Property-based tests ---

func TestProperty_BuildActionArgsNilForGroup(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {
		cfg := &config{Action: actionGroup}
		got, err := buildActionArgs(cfg)
		if err != nil {
			rt.Fatalf("unexpected error: %v", err)
		}
		if got != nil {
			rt.Fatalf("expected nil for action %q, got %v", cfg.Action, got)
		}
	})
}

func TestProperty_BuildActionArgsStartsWithAction(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {
		action := rapid.SampledFrom([]action{actionRemove, actionLink, actionDedupe}).Draw(rt, "action")
		numArgs := rapid.IntRange(0, 5).Draw(rt, "numArgs")
		argTokens := make([]string, numArgs)
		for i := range numArgs {
			argTokens[i] = rapid.StringMatching(`--[a-z\-]{1,15}`).Draw(rt, "arg")
		}
		actionArgs := strings.Join(argTokens, " ")

		cfg := &config{Action: action, ActionArgs: actionArgs}
		got, err := buildActionArgs(cfg)
		if err != nil {
			rt.Fatalf("unexpected error: %v", err)
		}
		if got == nil {
			rt.Fatalf("expected non-nil for action %q", action)
		}
		if got[0] != string(action) {
			rt.Fatalf("first arg = %q, want %q", got[0], action)
		}
		if len(got) != 1+numArgs {
			rt.Fatalf("got %d args, want %d", len(got), 1+numArgs)
		}
	})
}

func TestProperty_BuildScanArgsStructure(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {
		scanPath := rapid.StringMatching(`/[a-z]{1,10}(/[a-z]{1,10}){0,3}`).Draw(rt, "scanPath")
		numArgs := rapid.IntRange(0, 3).Draw(rt, "numArgs")
		argTokens := make([]string, numArgs*2)
		for i := 0; i < numArgs*2; i += 2 {
			argTokens[i] = rapid.StringMatching(`--[a-z\-]{1,10}`).Draw(rt, "flag")
			argTokens[i+1] = rapid.StringMatching(`[a-zA-Z0-9]{1,10}`).Draw(rt, "value")
		}
		argsStr := strings.Join(argTokens, " ")

		cfg := &config{ScanPath: scanPath, Args: argsStr}
		got, err := buildScanArgs(cfg)
		if err != nil {
			rt.Fatalf("buildScanArgs(%+v): unexpected error: %v", cfg, err)
		}

		if got[0] != "group" {
			rt.Fatalf("buildScanArgs: first arg = %q, want \"group\"", got[0])
		}
		if got[len(got)-1] != "--cache" {
			rt.Fatalf("buildScanArgs: last arg = %q, want \"--cache\"", got[len(got)-1])
		}
	})
}
