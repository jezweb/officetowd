package sync

import "testing"

// mkJobs builds a job slice with the given counts of each delete op (plus a
// few noops as filler), so we can assert the valve holds the right ones.
func mkJobs(deleteRemote, deleteLocal, noops int) []job {
	jobs := make([]job, 0, deleteRemote+deleteLocal+noops)
	for i := 0; i < deleteRemote; i++ {
		jobs = append(jobs, job{action: action{Op: opDeleteRemote}})
	}
	for i := 0; i < deleteLocal; i++ {
		jobs = append(jobs, job{action: action{Op: opDeleteLocal}})
	}
	for i := 0; i < noops; i++ {
		jobs = append(jobs, job{action: action{Op: opNoop}})
	}
	return jobs
}

func countOps(jobs []job) (dr, dl int) {
	for i := range jobs {
		switch jobs[i].action.Op {
		case opDeleteRemote:
			dr++
		case opDeleteLocal:
			dl++
		}
	}
	return
}

func TestSafetyCeiling(t *testing.T) {
	cases := map[int]int{0: 10, 5: 10, 20: 10, 21: 10, 22: 11, 100: 50}
	for in, want := range cases {
		if got := safetyCeiling(in); got != want {
			t.Errorf("safetyCeiling(%d) = %d, want %d", in, got, want)
		}
	}
}

func TestValveHoldsLocalWipe(t *testing.T) {
	// Remote listing came back empty while 40 local files still exist → all 40
	// marked opDeleteLocal. Must be held (40 > ceiling(40)=20).
	jobs := mkJobs(0, 40, 5)
	heldRemote, heldLocal := enforceDeleteSafetyValve(jobs, 0, 40)
	if heldRemote != 0 || heldLocal != 40 {
		t.Fatalf("held = (%d,%d), want (0,40)", heldRemote, heldLocal)
	}
	dr, dl := countOps(jobs)
	if dl != 0 {
		t.Errorf("expected all opDeleteLocal neutralised, %d remain", dl)
	}
	if dr != 0 {
		t.Errorf("unexpected opDeleteRemote: %d", dr)
	}
}

func TestValveHoldsRemoteWipe(t *testing.T) {
	// Local folder vanished while 40 remote objects exist → all 40 marked
	// opDeleteRemote. Must be held (40 > ceiling(40)=20).
	jobs := mkJobs(40, 0, 5)
	heldRemote, heldLocal := enforceDeleteSafetyValve(jobs, 40, 0)
	if heldRemote != 40 || heldLocal != 0 {
		t.Fatalf("held = (%d,%d), want (40,0)", heldRemote, heldLocal)
	}
	if dr, _ := countOps(jobs); dr != 0 {
		t.Errorf("expected all opDeleteRemote neutralised, %d remain", dr)
	}
}

func TestValveAllowsSmallDeletes(t *testing.T) {
	// A handful of genuine deletes on a healthy cortex must pass through.
	// 8 local deletes with 100 local files (ceiling 50) and 8 < 10 floor.
	jobs := mkJobs(3, 8, 50)
	heldRemote, heldLocal := enforceDeleteSafetyValve(jobs, 100, 100)
	if heldRemote != 0 || heldLocal != 0 {
		t.Fatalf("held = (%d,%d), want (0,0) — small deletes should pass", heldRemote, heldLocal)
	}
	dr, dl := countOps(jobs)
	if dr != 3 || dl != 8 {
		t.Errorf("ops changed: got (%d,%d) want (3,8)", dr, dl)
	}
}

func TestValveIndependentDirections(t *testing.T) {
	// Local wipe triggers but a small number of remote deletes still proceed.
	jobs := mkJobs(2, 30, 0)
	heldRemote, heldLocal := enforceDeleteSafetyValve(jobs, 100, 30)
	if heldRemote != 0 {
		t.Errorf("remote deletes should pass (2 < ceiling), held %d", heldRemote)
	}
	if heldLocal != 30 {
		t.Errorf("local wipe should be held, held %d want 30", heldLocal)
	}
	if dr, _ := countOps(jobs); dr != 2 {
		t.Errorf("remote deletes neutralised unexpectedly: %d remain want 2", dr)
	}
}
