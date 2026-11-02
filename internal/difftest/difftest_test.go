package difftest_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/cruizba/slack-browser-mcp/internal/difftest"
)

func TestResult_Render(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		result difftest.Result
		want   string
	}{
		{
			name:   "empty streams",
			result: difftest.Result{Exit: 0},
			want:   "# exit: 0\n# stdout 0\n\n# stderr 0\n\n",
		},
		{
			name:   "output without a trailing newline keeps its length",
			result: difftest.Result{Stdout: "no newline", Exit: 2},
			want:   "# exit: 2\n# stdout 10\nno newline\n# stderr 0\n\n",
		},
		{
			name:   "blank lines at the end are part of the output",
			result: difftest.Result{Stdout: "two\n\n", Stderr: "warn\n", Exit: 1},
			want:   "# exit: 1\n# stdout 5\ntwo\n\n\n# stderr 5\nwarn\n\n",
		},
		{
			name:   "a stream that starts with the other marker",
			result: difftest.Result{Stdout: "# stderr 0\n", Exit: 0},
			want:   "# exit: 0\n# stdout 11\n# stderr 0\n\n# stderr 0\n\n",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if got := tt.result.Render(); got != tt.want {
				t.Fatalf("Render() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestParse(t *testing.T) {
	t.Parallel()

	t.Run("round trips every result through its golden", func(t *testing.T) {
		t.Parallel()

		results := []difftest.Result{
			{},
			{Stdout: "no newline", Exit: 2},
			{Stdout: "two\n\n", Stderr: "warn\n", Exit: 1},
			{Stdout: "# stderr 0\n", Exit: 0},
			{Stdout: "ok\n", Stderr: "no newline at the end", Exit: 0},
		}

		for _, want := range results {
			got, err := difftest.Parse([]byte(want.Render()))
			if err != nil {
				t.Fatalf("Parse(%q): %v", want.Render(), err)
			}

			if got != want {
				t.Fatalf("Parse() = %+v, want %+v", got, want)
			}
		}
	})

	t.Run("refuses a file it did not write", func(t *testing.T) {
		t.Parallel()

		_, err := difftest.Parse([]byte("hola\n"))
		if !errors.Is(err, difftest.ErrBadGolden) {
			t.Fatalf("Parse() error = %v, want ErrBadGolden", err)
		}
	})

	t.Run("refuses a body shorter than its header promises", func(t *testing.T) {
		t.Parallel()

		_, err := difftest.Parse([]byte("# exit: 0\n# stdout 99\nhola\n"))
		if !errors.Is(err, difftest.ErrBadGolden) {
			t.Fatalf("Parse() error = %v, want ErrBadGolden", err)
		}
	})
}

func TestNormalizer_Apply(t *testing.T) {
	t.Parallel()

	normalizer := difftest.Normalizer{
		Root: "/tmp/TestDifferential123/001",
		Home: "/tmp/TestDifferential123/001/home",
		Addr: "http://127.0.0.1:41234",
	}

	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "an ISO stamp",
			input: "saved at 2026-11-02T00:12:33.912Z",
			want:  "saved at <iso>",
		},
		{
			name:  "a printed date and a message ts",
			input: "[1793210711.712429] 2026-10-28 18:05:11 Carlos: hola",
			want:  "[<ts>] <date> Carlos: hola",
		},
		{
			name:  "the directory this run was given",
			input: "stored in /tmp/TestDifferential123/001/config",
			want:  "stored in <root>/config",
		},
		{
			name:  "the fake Slack address",
			input: "GET http://127.0.0.1:41234/api/auth.test",
			want:  "GET <fake>/api/auth.test",
		},
		{
			name:  "a version",
			input: "slack-browser v0.8.0-4-ge89fc3d-dirty",
			want:  "slack-browser <version>",
		},
		{
			name:  "node's experimental warning, which Go will never print",
			input: "(node:123) ExperimentalWarning: SQLite is experimental\n(Use `node --trace-warnings ...` to show where the warning was created)\nok\n",
			want:  "ok\n",
		},
		{
			name:  "text with nothing run-specific is untouched",
			input: "ok    CodeURJC (T000000001) as carlos.ruiz\n",
			want:  "ok    CodeURJC (T000000001) as carlos.ruiz\n",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if got := normalizer.Apply(tt.input); got != tt.want {
				t.Fatalf("Apply(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestDiff(t *testing.T) {
	t.Parallel()

	t.Run("says nothing when the two agree", func(t *testing.T) {
		t.Parallel()

		if got := difftest.Diff("a\nb\n", "a\nb\n"); got != "" {
			t.Fatalf("Diff() = %q, want empty", got)
		}
	})

	t.Run("points at the first differing line with context", func(t *testing.T) {
		t.Parallel()

		got := difftest.Diff("a\nb\nc\nd\n", "a\nb\nX\nd\n")

		for _, want := range []string{"first difference at line 3", "- c", "+ X", "  b"} {
			if !strings.Contains(got, want) {
				t.Fatalf("Diff() = %q, want it to contain %q", got, want)
			}
		}
	})

	t.Run("reports a truncated output instead of panicking", func(t *testing.T) {
		t.Parallel()

		got := difftest.Diff("a\nb", "a")

		if !strings.Contains(got, "+ <end of output>") {
			t.Fatalf("Diff() = %q, want an end-of-output line", got)
		}
	})

	t.Run("reports a golden that ends before the output does", func(t *testing.T) {
		t.Parallel()

		got := difftest.Diff("a", "a\nb")

		if !strings.Contains(got, "- <end of golden>") {
			t.Fatalf("Diff() = %q, want an end-of-golden line", got)
		}
	})
}
