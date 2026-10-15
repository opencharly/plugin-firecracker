package firecracker

// TestDeclaredWordsMatchServedClasses — the repo-local check that fails when the candy's
// `plugin.providers:` claims a class this plugin does not serve.
//
// WHY IT EXISTS. A provider word in charly.yml is a claim: the kernel will route that
// class to this plugin at runtime. If the candy claims a class the binary cannot answer,
// the word is a route to a refusal — worse than an absent word, because it is a route the
// registry will happily hand out (opencharly/plugin-firecracker#8, opencharly/charly#916).
//
// FAILS WITHOUT the served-class side of a declaration: add a class to `providers:` that
// this plugin does not implement and this test exits non-zero naming it. It is the
// in-repo half of the coverage that A2/R10 requires for a `providers:` change.
//
// It reads the manifest rather than asserting a copy of it, so there is no second list to
// drift: the candy is the claim, these constants are the capability.

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// servedClasses returns the provider classes this plugin implements, as the word prefixes
// it can answer. Derived from the same constants the dispatcher uses, so a class cannot be
// served in code yet missing here (or vice versa).
func servedClasses() []string {
	return []string{classCommand, classVerb}
}

// declaredWords reads `plugin.providers:` out of the candy manifest beside this file.
func declaredWords(t *testing.T) []string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(".", "charly.yml"))
	if err != nil {
		t.Fatalf("read charly.yml: %v", err)
	}
	re := regexp.MustCompile(`(?m)^\s*-\s*([a-z-]+):([A-Za-z0-9_.-]+)\s*$`)
	var out []string
	inProviders := false
	for _, line := range strings.Split(string(b), "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "providers:" {
			inProviders = true
			continue
		}
		if inProviders {
			if m := re.FindStringSubmatch(line); m != nil {
				out = append(out, m[1]+":"+m[2])
				continue
			}
			if trimmed != "" && !strings.HasPrefix(trimmed, "#") && !strings.HasPrefix(trimmed, "-") {
				inProviders = false
			}
		}
	}
	if len(out) == 0 {
		t.Fatalf("no providers: entries parsed from charly.yml — the parser or the manifest moved")
	}
	return out
}

func TestDeclaredWordsMatchServedClasses(t *testing.T) {
	served := map[string]bool{}
	for _, c := range servedClasses() {
		served[c] = true
	}
	var undeclared []string
	for _, w := range declaredWords(t) {
		class := w[:strings.Index(w, ":")]
		if !served[class] {
			undeclared = append(undeclared, w)
		}
	}
	if len(undeclared) > 0 {
		sort.Strings(undeclared)
		t.Fatalf("charly.yml claims %d provider word(s) this plugin does not serve: %s (served classes: %s)",
			len(undeclared), strings.Join(undeclared, ", "), strings.Join(servedClasses(), ", "))
	}
	t.Logf("declared words match served classes (%s)", strings.Join(servedClasses(), ", "))
}
