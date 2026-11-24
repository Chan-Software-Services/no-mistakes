package config

import (
	"slices"
	"strings"
	"testing"
)

// TestEffectiveRepoConfig_NoSurfacePathsTrustedOnly proves test.no_surface_paths
// is honored only from the trusted default-branch copy, regardless of
// allow_repo_commands. The rule lets the Test step pass a no-surface verdict
// without an operator, so a pushed branch that could add or widen it could
// exempt its own code from live validation.
func TestEffectiveRepoConfig_NoSurfacePathsTrustedOnly(t *testing.T) {
	trustedRules := []string{"docs/**", "*.md"}
	for _, allow := range []bool{false, true} {
		pushed := &RepoConfig{Test: TestRaw{NoSurfacePaths: []string{"*"}}}
		trusted := &RepoConfig{Test: TestRaw{NoSurfacePaths: append([]string(nil), trustedRules...)}}

		effective := EffectiveRepoConfig(pushed, trusted, allow)
		if !slices.Equal(effective.Test.NoSurfacePaths, trustedRules) {
			t.Fatalf("allow=%v: NoSurfacePaths = %q, want the trusted %q", allow, effective.Test.NoSurfacePaths, trustedRules)
		}
		// The effective value is a copy, so editing it cannot reach back
		// into the trusted config another caller may still read.
		effective.Test.NoSurfacePaths[0] = "mutated/**"
		if trusted.Test.NoSurfacePaths[0] != "docs/**" {
			t.Fatalf("allow=%v: effective NoSurfacePaths aliases the trusted slice", allow)
		}

		// A pushed-only value resolves to none: the trusted copy sets no rule.
		effective = EffectiveRepoConfig(pushed, &RepoConfig{}, allow)
		if effective.Test.NoSurfacePaths != nil {
			t.Fatalf("allow=%v: pushed-only NoSurfacePaths leaked: %q", allow, effective.Test.NoSurfacePaths)
		}
		if got := Merge(&GlobalConfig{}, effective).Test.NoSurfacePaths; got != nil {
			t.Fatalf("allow=%v: pushed-only NoSurfacePaths resolved to %q", allow, got)
		}

		// Without a trusted copy the pushed value is dropped, never used as a
		// fallback.
		effective = EffectiveRepoConfig(pushed, nil, allow)
		if effective.Test.NoSurfacePaths != nil {
			t.Fatalf("allow=%v: without a trusted copy NoSurfacePaths = %q, want nil", allow, effective.Test.NoSurfacePaths)
		}

		if !slices.Equal(pushed.Test.NoSurfacePaths, []string{"*"}) {
			t.Fatalf("allow=%v: pushed config was mutated", allow)
		}
	}
}

func TestLoadRepo_NoSurfacePaths(t *testing.T) {
	cfg, err := LoadRepoFromBytes([]byte("test:\n  no_surface_paths:\n    - \"docs/**\"\n    - \"  README.md  \"\n"))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if want := []string{"docs/**", "  README.md  "}; !slices.Equal(cfg.Test.NoSurfacePaths, want) {
		t.Fatalf("raw NoSurfacePaths = %q, want %q", cfg.Test.NoSurfacePaths, want)
	}
	got := Merge(&GlobalConfig{}, cfg).Test.NoSurfacePaths
	if want := []string{"docs/**", "README.md"}; !slices.Equal(got, want) {
		t.Fatalf("resolved NoSurfacePaths = %q, want trimmed %q", got, want)
	}
}

func TestMerge_NoSurfacePathsDefaultEmpty(t *testing.T) {
	if got := Merge(&GlobalConfig{}, &RepoConfig{}).Test.NoSurfacePaths; len(got) != 0 {
		t.Fatalf("NoSurfacePaths must default to none, got %q", got)
	}
}

// The rule describes ONE repository's tree, so a global value has no
// repository to describe and must never reach a run.
func TestMerge_GlobalNoSurfacePathsAreNotUsed(t *testing.T) {
	global, err := LoadGlobalFromBytes([]byte("test:\n  no_surface_paths:\n    - \"*\"\n"))
	if err != nil {
		t.Fatalf("parse global: %v", err)
	}
	if got := Merge(global, &RepoConfig{}).Test.NoSurfacePaths; len(got) != 0 {
		t.Fatalf("global NoSurfacePaths leaked into the resolved config: %q", got)
	}
	repo := &RepoConfig{Test: TestRaw{NoSurfacePaths: []string{"docs/**"}}}
	if got := Merge(global, repo).Test.NoSurfacePaths; !slices.Equal(got, []string{"docs/**"}) {
		t.Fatalf("NoSurfacePaths = %q, want the repository's alone", got)
	}
}

// Validation runs on whichever copy is parsed, the pushed branch's included:
// an invalid trusted copy fails every run, so a branch carrying one must fail
// before it merges.
func TestLoadRepo_NoSurfacePathsRefusesEmptyAndMalformedEntries(t *testing.T) {
	cases := []struct {
		name string
		yaml string
		want string
	}{
		{
			name: "empty after trimming",
			yaml: "test:\n  no_surface_paths:\n    - \"docs/**\"\n    - \"   \"\n",
			want: "test.no_surface_paths[1] must not be empty",
		},
		{
			name: "malformed glob",
			yaml: "test:\n  no_surface_paths:\n    - \"docs/[\"\n",
			want: `test.no_surface_paths[0] "docs/[" is not a valid glob: `,
		},
		{
			name: "subtree with no directory",
			yaml: "test:\n  no_surface_paths:\n    - \"README.md\"\n    - \" /** \"\n",
			want: `test.no_surface_paths[1] "/**" is not a valid glob: subtree pattern needs a directory before /**`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := LoadRepoFromBytes([]byte(tc.yaml))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("repo err = %v, want it to contain %q", err, tc.want)
			}
		})
	}
}
