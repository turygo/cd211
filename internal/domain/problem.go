package domain

import "unicode/utf8"

// ProblemCode is a stable, durable, operator-facing problem identifier. Codes
// are external API values: they describe what the user must understand or do,
// never an internal operation such as find_file/add_copy, and they must not
// change once released. LastErrorCode persists one ProblemCode; the LastError
// column keeps safe default English text alongside it for compatibility with
// consumers that only read the message.
type ProblemCode string

const (
	// ProblemCloudUnreachable reports a temporary CloudDrive2 transport,
	// deadline, or resource observation. The workflow retries automatically.
	ProblemCloudUnreachable ProblemCode = "cloud_unreachable"
	// ProblemCloudUnreachableTimeout is the terminal deadline form of
	// ProblemCloudUnreachable.
	ProblemCloudUnreachableTimeout ProblemCode = "cloud_unreachable_timeout"
	// ProblemCloudAuthenticationRequired reports an unauthorized CloudDrive2
	// observation. The workflow retries automatically.
	ProblemCloudAuthenticationRequired ProblemCode = "cloud_authentication_required"
	// ProblemCloudAuthenticationTimeout is the terminal deadline form of
	// ProblemCloudAuthenticationRequired.
	ProblemCloudAuthenticationTimeout ProblemCode = "cloud_authentication_timeout"
	// ProblemCloudCopyNotReady reports a copy submission that CloudDrive2 has
	// not accepted or accessed yet (not-found or rejected observation). The
	// workflow retries automatically.
	ProblemCloudCopyNotReady ProblemCode = "cloud_copy_not_ready"
	// ProblemCloudCopyNotReadyTimeout is the terminal deadline form of
	// ProblemCloudCopyNotReady.
	ProblemCloudCopyNotReadyTimeout ProblemCode = "cloud_copy_not_ready_timeout"
	// ProblemCloudFolderUnavailable reports that the 115 category folder
	// could not be found or created.
	ProblemCloudFolderUnavailable ProblemCode = "cloud_folder_unavailable"
	// ProblemCloudRequestRejected reports that CloudDrive2 refused a request
	// without implying absence or retryability.
	ProblemCloudRequestRejected ProblemCode = "cloud_request_rejected"
	// ProblemCloudContentLayoutInvalid reports a durable contradiction between
	// the torrent manifest and the completed 115 object layout.
	ProblemCloudContentLayoutInvalid ProblemCode = "cloud_content_layout_invalid"
	// ProblemCloudResponseInvalid reports a malformed, nil, or inconsistent
	// CloudDrive2 reply.
	ProblemCloudResponseInvalid ProblemCode = "cloud_response_invalid"
	// ProblemOfflineSubmissionRejected reports that 115 rejected an offline submission.
	ProblemOfflineSubmissionRejected ProblemCode = "offline_submission_rejected"
	// ProblemOfflineDownloadFailed reports that the 115 offline task itself
	// reached an error state.
	ProblemOfflineDownloadFailed ProblemCode = "offline_download_failed"
	// ProblemOfflineTimeout is the terminal offline phase deadline.
	ProblemOfflineTimeout ProblemCode = "offline_timeout"
	// ProblemCopyTaskFailed reports an explicit upstream CopyTask FAILED.
	ProblemCopyTaskFailed ProblemCode = "copy_task_failed"
	// ProblemCopyTimeout is the terminal copy phase deadline when no
	// recognized retry problem was the last observation.
	ProblemCopyTimeout ProblemCode = "copy_timeout"
	// ProblemDestinationConflict reports that another durable download
	// reserved the same destination.
	ProblemDestinationConflict ProblemCode = "destination_conflict"
	// ProblemDestinationCollision reports that content already exists at the
	// local destination name.
	ProblemDestinationCollision ProblemCode = "destination_collision"
	// ProblemLocalVerificationFailed reports unsafe or missing local content
	// after the immediate verification checks.
	ProblemLocalVerificationFailed ProblemCode = "local_verification_failed"
	// ProblemLocalVerificationTimeout is the terminal verification deadline.
	ProblemLocalVerificationTimeout ProblemCode = "local_verification_timeout"
	// ProblemLocalDeleteFailed reports that reserved local content could not
	// be deleted safely.
	ProblemLocalDeleteFailed ProblemCode = "local_delete_failed"
	// ProblemWorkflowOperationTimeout reports that a claimed workflow
	// operation exceeded its lease-bound operation deadline. The workflow
	// retries automatically.
	ProblemWorkflowOperationTimeout ProblemCode = "workflow_operation_timeout"
	// ProblemInternalWorkflowError reports an internal invariant violation or
	// an invalid local input to a remote operation.
	ProblemInternalWorkflowError ProblemCode = "internal_workflow_error"
	// ProblemLegacy marks rows migrated from an era before problem codes; the
	// stored LastError text is the only message and must be rendered as-is
	// after sanitization.
	ProblemLegacy ProblemCode = "legacy"
)

// Valid reports whether code is a member of the stable problem catalog.
func (code ProblemCode) Valid() bool {
	switch code {
	case ProblemCloudUnreachable,
		ProblemCloudUnreachableTimeout,
		ProblemCloudAuthenticationRequired,
		ProblemCloudAuthenticationTimeout,
		ProblemCloudCopyNotReady,
		ProblemCloudCopyNotReadyTimeout,
		ProblemCloudFolderUnavailable,
		ProblemCloudContentLayoutInvalid,
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
		ProblemLegacy:
		return true
	default:
		return false
	}
}

// ProblemText returns the canonical safe English text for a known problem
// code. The workflow persists this text into LastError so consumers that do
// not understand codes still receive an actionable message; the Web UI
// localizes known codes instead of rendering this fallback. ProblemLegacy has
// no canonical text: its message lives entirely in the stored LastError.
func ProblemText(code ProblemCode) string {
	switch code {
	case ProblemCloudUnreachable:
		return "CloudDrive2 is unreachable."
	case ProblemCloudUnreachableTimeout:
		return "CloudDrive2 stayed unreachable until the copy deadline. Refresh the CloudDrive2 mount and check its connection, then Retry."
	case ProblemCloudAuthenticationRequired:
		return "CloudDrive2 requires authentication."
	case ProblemCloudAuthenticationTimeout:
		return "CloudDrive2 kept rejecting the credentials until the copy deadline. Check the CloudDrive2 login credentials, then Retry."
	case ProblemCloudCopyNotReady:
		return "The 115 offline download finished, but CloudDrive2 has not accepted the copy yet. If this persists, refresh the 115 mount and verify the cloud category and NAS staging paths."
	case ProblemCloudContentLayoutInvalid:
		return "The 115 offline result does not match the torrent file layout."
	case ProblemCloudCopyNotReadyTimeout:
		return "CloudDrive2 did not accept the copy before the deadline. Refresh the 115 mount and verify the cloud category and NAS staging paths, then Retry."
	case ProblemCloudFolderUnavailable:
		return "The 115 category folder is unavailable. Check the cloud root and category configuration, then Retry."
	case ProblemCloudRequestRejected:
		return "CloudDrive2 rejected the request. Check the configuration, then Retry."
	case ProblemCloudResponseInvalid:
		return "CloudDrive2 returned an invalid response. Retry to try the operation again."
	case ProblemOfflineSubmissionRejected:
		return "115 rejected the offline download submission. Check the source, then Retry."
	case ProblemOfflineDownloadFailed:
		return "The 115 offline download failed. Retry to submit it again."
	case ProblemOfflineTimeout:
		return "The 115 offline download did not finish before the deadline. Check the source, then Retry."
	case ProblemCopyTaskFailed:
		return "The CloudDrive2 copy task failed. Retry to submit the copy again."
	case ProblemCopyTimeout:
		return "The copy did not finish before the deadline. Refresh the 115 mount and verify the cloud category and NAS staging paths, then Retry."
	case ProblemDestinationConflict:
		return "Another download reserved the same destination. Resolve the conflict, then Retry."
	case ProblemDestinationCollision:
		return "Content already exists at the destination. Remove it, then Retry."
	case ProblemLocalVerificationFailed:
		return "Local content verification failed. Check the shared staging folder, then Retry."
	case ProblemLocalVerificationTimeout:
		return "The local content did not appear before the verification deadline. Refresh the 115 mount and the staging folder, then Retry."
	case ProblemLocalDeleteFailed:
		return "Local content could not be deleted. Check the staging folder permissions, then Retry."
	case ProblemWorkflowOperationTimeout:
		return "A workflow operation exceeded its deadline. CD211 will retry automatically."
	case ProblemInternalWorkflowError:
		return "An internal workflow error occurred. Retry to continue."
	default:
		return ""
	}
}

// safeProblemCode reports whether a persisted code is structurally safe to
// store and render. Unknown codes from future versions remain readable rather
// than being rejected, so only the empty string and control-character or
// non-UTF-8 values are unsafe.
func safeProblemCode(value string) bool {
	if value == "" {
		return true
	}
	if !utf8.ValidString(value) {
		return false
	}
	for _, character := range value {
		if character < 0x20 || character == 0x7f {
			return false
		}
	}
	return true
}
