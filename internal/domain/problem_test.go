package domain

import "testing"

func TestProblemCatalogIsCompleteAndActionable(t *testing.T) {
	catalog := []ProblemCode{
		ProblemCloudUnreachable,
		ProblemCloudUnreachableTimeout,
		ProblemCloudAuthenticationRequired,
		ProblemCloudAuthenticationTimeout,
		ProblemCloudCopyNotReady,
		ProblemCloudCopyNotReadyTimeout,
		ProblemCloudFolderUnavailable,
		ProblemCloudRequestRejected,
		ProblemCloudResponseInvalid,
		ProblemOfflineSubmissionRejected,
		ProblemOfflineDownloadFailed,
		ProblemOfflineTimeout,
		ProblemCopyTaskFailed,
		ProblemCopyTimeout,
		ProblemDestinationConflict,
		ProblemDestinationCollision,
		ProblemLocalVerificationFailed,
		ProblemLocalVerificationTimeout,
		ProblemLocalDeleteFailed,
		ProblemWorkflowOperationTimeout,
		ProblemInternalWorkflowError,
		ProblemLegacy,
	}
	for _, code := range catalog {
		if !code.Valid() {
			t.Errorf("catalog code %q must be valid", code)
		}
		if code != ProblemLegacy && ProblemText(code) == "" {
			t.Errorf("catalog code %q has no actionable English text", code)
		}
	}
	if ProblemText(ProblemLegacy) != "" {
		t.Error("legacy code must keep its message entirely in LastError")
	}
	if ProblemCode("").Valid() || ProblemCode("find_file").Valid() || ProblemCode("permanent").Valid() {
		t.Error("non-catalog codes must not validate")
	}
}

func TestProblemCodeSafety(t *testing.T) {
	for _, value := range []string{"", "cloud_unreachable", "legacy", "future_code"} {
		if !safeProblemCode(value) {
			t.Errorf("safeProblemCode(%q) = false, want true", value)
		}
	}
	for _, value := range []string{"bad\ncode", "bad\x00code", string([]byte{0xff})} {
		if safeProblemCode(value) {
			t.Errorf("safeProblemCode(%q) = true, want false", value)
		}
	}
}
