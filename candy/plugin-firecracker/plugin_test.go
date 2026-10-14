package firecracker

import (
	"context"
	"strings"
	"testing"

	"github.com/opencharly/sdk/kit"
	pb "github.com/opencharly/spec/proto"
)

// TestProviderServesBothClasses gates the defect that made EVERY check of this plugin silently run the
// command path. The SDK hands the provider to its server and asks whether it also implements
// kit.CheckVerbProvider: when it does, `firecracker:` steps are routed to RunVerb; when it does NOT,
// they fall through to Invoke (the command class) and the step reports `firecracker: command exited 1`
// — with no check, no probe and no message. One value must therefore satisfy both interfaces.
func TestProviderServesBothClasses(t *testing.T) {
	p := NewProvider()

	kv, ok := p.(kit.CheckVerbProvider)
	if !ok {
		t.Fatal("the provider the SDK is handed does not implement kit.CheckVerbProvider: every firecracker: check step would fall through to the command path")
	}

	if kv.Reserved() != "firecracker" {
		t.Errorf("Reserved() = %q, want firecracker", kv.Reserved())
	}

	var _ kit.CheckVerbProvider = NewCheckVerb()
}

// TestInvokeDispatchesByClass is the gate for the defect that made every check of this plugin
// meaningless: Invoke never looked at req.GetClass(), so a VERB invocation ran the COMMAND effect, the
// CLI exited non-zero, and the step reported `firecracker: command exited 1` -- with the check verb
// never called. A verb invocation must answer as a verb even when it cannot run (here: no venue
// context), so the reply is a typed result carrying the verb's own message.
func TestInvokeDispatchesByClass(t *testing.T) {
	p := NewProvider()

	req := &pb.InvokeRequest{
		Class:      classVerb,
		ParamsJson: []byte(`{"plugin":"firecracker","plugin_input":{"method":"vmm"}}`),
	}

	rep, err := p.Invoke(context.Background(), req)
	if err != nil {
		if strings.Contains(err.Error(), "command exited") {
			t.Fatalf("a verb invocation ran the COMMAND effect: %v", err)
		}

		t.Fatalf("a verb invocation failed before reaching the verb: %v", err)
	}

	if len(rep.GetResultJson()) == 0 {
		t.Fatal("a verb invocation returned no typed result")
	}

	if !strings.Contains(string(rep.GetResultJson()), "firecracker") {
		t.Errorf("the verb message did not survive: %s", string(rep.GetResultJson()))
	}

	// And the command class still routes to the CLI path: an unknown class is refused by name.
	if _, err := p.Invoke(context.Background(), &pb.InvokeRequest{Class: "nonsense"}); err == nil {
		t.Error("an unknown class was accepted")
	}
}
