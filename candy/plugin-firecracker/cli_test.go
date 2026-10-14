package firecracker

import "testing"

// TestRunStatusCLI_RequiresAName gates the subcommand plumbing at its cheapest honest boundary: the
// argument contract, which needs no lease and no VMM. The lease-backed behaviour of status/stop is
// covered where the lease is (TestLease_*), and the orchestration between them is exercised live by
// the R10 bed — stated here rather than implied.
func TestRunStatusCLI_RequiresAName(t *testing.T) {
	if got := runStatusCLI(cliArgs{}); got != 2 {
		t.Errorf("status with no --name exited %d, want 2 (usage)", got)
	}

	if got := runStopCLI(cliArgs{}); got != 2 {
		t.Errorf("stop with no --name exited %d, want 2 (usage)", got)
	}
}

// TestRunStopCLI_UnknownLeaseIsReportedWithAFailureExit: a lease that does not exist is an error, never
// a silent success.
func TestRunStopCLI_UnknownLeaseIsReportedWithAFailureExit(t *testing.T) {
	if got := runStopCLI(cliArgs{name: "charly-test-lease-that-does-not-exist"}); got != 1 {
		t.Errorf("stop for an unknown lease exited %d, want 1", got)
	}
}
