package deprule

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
)

var workflowDir = filepath.Join("..", "..", ".github", "workflows")

// shaPin is an action reference pinned by a full commit SHA.
var shaPin = regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_./-]+@[0-9a-f]{40}$`)

// workflow is the part of a workflow file the checks read.
type workflow struct {
	name string
	root map[string]any
	raw  string
}

func loadWorkflows(t *testing.T) []workflow {
	t.Helper()
	files, err := filepath.Glob(filepath.Join(workflowDir, "*.yml"))
	if err != nil || len(files) == 0 {
		t.Fatalf("no workflow files in %s (%v)", workflowDir, err)
	}
	more, _ := filepath.Glob(filepath.Join(workflowDir, "*.yaml"))
	var out []workflow
	for _, f := range append(files, more...) {
		b, err := os.ReadFile(f) //nolint:gosec // G304: the repository's workflow files
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, parseWorkflow(t, filepath.Base(f), string(b)))
	}
	return out
}

func parseWorkflow(t *testing.T, name, text string) workflow {
	t.Helper()
	var root map[string]any
	if err := yaml.Unmarshal([]byte(text), &root); err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	return workflow{name: name, root: root, raw: text}
}

func asMap(v any) map[string]any {
	m, _ := v.(map[string]any)
	return m
}

// jobs returns the workflow's jobs by id.
func (w workflow) jobs() map[string]map[string]any {
	out := map[string]map[string]any{}
	for id, j := range asMap(w.root["jobs"]) {
		out[id] = asMap(j)
	}
	return out
}

// usesRefs returns every `uses:` value (job level and step level).
func (w workflow) usesRefs() []string {
	var refs []string
	for _, j := range w.jobs() {
		if u, ok := j["uses"].(string); ok {
			refs = append(refs, u)
		}
		steps, _ := j["steps"].([]any)
		for _, s := range steps {
			if u, ok := asMap(s)["uses"].(string); ok {
				refs = append(refs, u)
			}
		}
	}
	return refs
}

// unpinned returns the problems with the workflow's action references: each
// must be a local workflow or action (./...) or owner/repo@<40 hex digits>,
// and the line must carry the version in a comment.
func (w workflow) unpinned() []string {
	var bad []string
	for _, u := range w.usesRefs() {
		if strings.HasPrefix(u, "./") {
			continue
		}
		if !shaPin.MatchString(u) {
			bad = append(bad, fmt.Sprintf("%s: %q is not pinned by a full commit SHA", w.name, u))
			continue
		}
		hasComment := false
		for _, l := range strings.Split(w.raw, "\n") {
			if strings.Contains(l, u) && regexp.MustCompile(`#\s*v?\d`).MatchString(l) {
				hasComment = true
			}
		}
		if !hasComment {
			bad = append(bad, fmt.Sprintf("%s: %q has no version in a comment", w.name, u))
		}
	}
	return bad
}

// writes returns the permission scopes set to write (or write-all) in a
// permissions value.
func writes(perm any) []string {
	switch p := perm.(type) {
	case string:
		if p != "read-all" && p != "" {
			return []string{p}
		}
	case map[string]any:
		var out []string
		for k, v := range p {
			if v != "read" && v != "none" {
				out = append(out, k)
			}
		}
		return out
	}
	return nil
}

// permissionProblems returns what is wrong with a workflow's permissions: no
// top-level block, a write at the top, or a write in any job other than the
// ones in allowWrite.
func (w workflow) permissionProblems(allowWrite map[string]bool) []string {
	var bad []string
	top, ok := w.root["permissions"]
	if !ok {
		bad = append(bad, w.name+": no top-level permissions block (the default token is broader)")
	}
	if wr := writes(top); len(wr) > 0 {
		bad = append(bad, fmt.Sprintf("%s: top-level permissions grant %v", w.name, wr))
	}
	for id, j := range w.jobs() {
		if wr := writes(j["permissions"]); len(wr) > 0 && !allowWrite[id] {
			bad = append(bad, fmt.Sprintf("%s: job %s is granted %v", w.name, id, wr))
		}
	}
	return bad
}

// TestWorkflowActionsPinned: no `uses:` without a 40-hex SHA in any workflow.
func TestWorkflowActionsPinned(t *testing.T) {
	ws := loadWorkflows(t)
	for _, w := range ws {
		for _, msg := range w.unpinned() {
			t.Error(msg)
		}
	}
	t.Run("sad/a tag or a branch is not a pin", func(t *testing.T) {
		w := parseWorkflow(t, "x.yml", "jobs:\n  a:\n    steps:\n      - uses: actions/checkout@v4\n      - uses: owner/repo@3d3c42e5aac5ba805825da76410c181273ba90b1\n")
		if got := w.unpinned(); len(got) != 2 {
			t.Fatalf("flagged %v", got)
		}
	})
}

// TestWorkflowPermissions: top-level permissions are read-only and the release
// job of release.yml is the only job in any workflow with a write.
func TestWorkflowPermissions(t *testing.T) {
	ws := loadWorkflows(t)
	seenRelease := false
	for _, w := range ws {
		allow := map[string]bool{}
		if w.name == "release.yml" {
			allow["release"] = true
			seenRelease = true
			rel := w.jobs()["release"]
			if got := writes(rel["permissions"]); len(got) != 1 || got[0] != "contents" {
				t.Errorf("release.yml: the release job must write contents and nothing else, got %v", got)
			}
		}
		for _, msg := range w.permissionProblems(allow) {
			t.Error(msg)
		}
	}
	if !seenRelease {
		t.Error("no release.yml")
	}
	t.Run("sad/writes are found", func(t *testing.T) {
		w := parseWorkflow(t, "x.yml", "permissions: write-all\njobs:\n  a:\n    permissions:\n      contents: write\n")
		if got := w.permissionProblems(nil); len(got) != 2 {
			t.Fatalf("flagged %v", got)
		}
		w = parseWorkflow(t, "y.yml", "jobs:\n  a:\n    runs-on: x\n")
		if got := w.permissionProblems(nil); len(got) != 1 {
			t.Fatalf("flagged %v", got)
		}
	})
}

// TestWorkflowTriggers: release.yml runs on a v* tag push and on nothing else;
// no workflow uses pull_request_target.
func TestWorkflowTriggers(t *testing.T) {
	for _, w := range loadWorkflows(t) {
		if strings.Contains(w.raw, "pull_request_target") {
			t.Errorf("%s names pull_request_target", w.name)
		}
		if w.name != "release.yml" {
			continue
		}
		on := asMap(w.root["on"])
		if len(on) != 1 {
			t.Errorf("release.yml has %d triggers, want only push: %v", len(on), on)
		}
		push := asMap(on["push"])
		tags, _ := push["tags"].([]any)
		if len(tags) != 1 || tags[0] != "v*" {
			t.Errorf("release.yml push tags = %v, want [v*]", tags)
		}
		if _, ok := push["branches"]; ok {
			t.Error("release.yml filters branches: a release is triggered by a tag only")
		}
		if len(push) != 1 {
			t.Errorf("release.yml push has more than tags: %v", push)
		}
		jobs := w.jobs()
		if jobs["check"]["uses"] != "./.github/workflows/ci.yml" {
			t.Errorf("release.yml: the check job does not reuse ci.yml: %v", jobs["check"]["uses"])
		}
		need := jobs["release"]["needs"]
		if need != "check" {
			t.Errorf("release.yml: the release job needs %v, want check", need)
		}
		if _, ok := asMap(w.root["concurrency"])["group"]; !ok {
			t.Error("release.yml has no concurrency group")
		}
	}
}

// TestWorkflowNoInterpolationInRun: no ${{ }} expression inside a run: line
// (pass values through env:), and no secret but the workflow token.
func TestWorkflowNoInterpolationInRun(t *testing.T) {
	secret := regexp.MustCompile(`secrets\.([A-Za-z0-9_]+)`)
	for _, w := range loadWorkflows(t) {
		for id, j := range w.jobs() {
			steps, _ := j["steps"].([]any)
			for i, s := range steps {
				if run, ok := asMap(s)["run"].(string); ok && strings.Contains(run, "${{") {
					t.Errorf("%s: job %s step %d interpolates an expression into run:", w.name, id, i)
				}
			}
		}
		for _, m := range secret.FindAllStringSubmatch(w.raw, -1) {
			if m[1] != "GITHUB_TOKEN" {
				t.Errorf("%s uses the secret %s", w.name, m[1])
			}
		}
	}
	t.Run("sad/an interpolated run is found", func(t *testing.T) {
		w := parseWorkflow(t, "x.yml", "jobs:\n  a:\n    steps:\n      - run: echo ${{ github.ref_name }}\n")
		steps, _ := w.jobs()["a"]["steps"].([]any)
		if !strings.Contains(asMap(steps[0])["run"].(string), "${{") {
			t.Fatal("not found")
		}
	})
}

// TestWorkflowTokenOnlyInSteps: no job-level env carries a token or a secret,
// so the workflow's token reaches only the steps that ask for it (not the tool
// installs or the build).
func TestWorkflowTokenOnlyInSteps(t *testing.T) {
	for _, w := range loadWorkflows(t) {
		for _, msg := range w.jobEnvTokens() {
			t.Error(msg)
		}
	}
	t.Run("sad/a job-level token is found", func(t *testing.T) {
		w := parseWorkflow(t, "x.yml", "jobs:\n  a:\n    env:\n      GH_TOKEN: ${{ secrets.GITHUB_TOKEN }}\n      TAG: x\n    steps:\n      - run: true\n")
		if got := w.jobEnvTokens(); len(got) != 1 {
			t.Fatalf("flagged %v", got)
		}
		w = parseWorkflow(t, "y.yml", "jobs:\n  a:\n    env:\n      TAG: x\n    steps:\n      - env:\n          GH_TOKEN: ${{ secrets.GITHUB_TOKEN }}\n        run: true\n")
		if got := w.jobEnvTokens(); len(got) != 0 {
			t.Fatalf("flagged a step-level token: %v", got)
		}
	})
}

// jobEnvTokens returns the job-level env entries that hold a secret or are
// named like a token.
func (w workflow) jobEnvTokens() []string {
	var bad []string
	for id, j := range w.jobs() {
		for k, v := range asMap(j["env"]) {
			val, _ := v.(string)
			if strings.Contains(val, "secrets.") || strings.Contains(strings.ToUpper(k), "TOKEN") {
				bad = append(bad, fmt.Sprintf("%s: job %s sets %s in its job-level env", w.name, id, k))
			}
		}
	}
	return bad
}
