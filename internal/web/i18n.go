package web

import "net/http"

// Lang selects the operator interface language.
type Lang string

const (
	// LangEN is the default interface language.
	LangEN Lang = "en"
	// LangZH is the Simplified Chinese interface language.
	LangZH Lang = "zh"
)

// langCookie stores the operator's display-language preference. It carries no
// authority, so it is readable by scripts and set through an idempotent GET.
const langCookie = "LANG"

// StateLabels contains the operator-visible labels for durable download states.
type StateLabels struct {
	Accepted          string
	Stopped           string
	SubmittingOffline string
	WaitingOffline    string
	SubmittingCopy    string
	WaitingCopy       string
	VerifyingLocal    string
	Completed         string
	Failed            string
	CancelRequested   string
	Cancelled         string
	DeleteRequested   string
	Deleted           string
}

// Strings is the full set of operator-visible interface text for one language.
// Templates and view builders read translated text exclusively from here.
type Strings struct {
	// Shared chrome
	NavDownloads  string
	NavCategories string
	SignOut       string
	SwitchLang    string // label of the OTHER language, used on the toggle link

	// Login
	TitleSignIn string
	LoginHint   string
	Username    string
	Password    string
	SignIn      string
	LoginFailed string

	// Downloads list
	TitleDownloads   string
	ShownFormat      string // printf with row count
	CloudOnline      string
	CloudUnavailable string
	FilterView       string
	FilterCategory   string
	Apply            string
	AllCategories    string
	ViewActive       string
	ViewCompleted    string
	ViewFailed       string
	ViewCancelled    string
	ViewAll          string
	ThName           string
	ThState          string
	ThProgress       string
	ThCategory       string
	ThUpdated        string
	AgoFormat        string // printf with age like "5m"
	EmptyTitle       string
	EmptyBody        string
	States           StateLabels

	// Route stages
	Stage115           string
	StageCopy          string
	StageVerify        string
	StatusPending      string
	StatusQueued       string
	StatusActiveSuffix string
	StatusSourceReady  string
	StatusCopyPresent  string
	StatusChecking     string
	StatusVerified     string

	// Detail
	SectionPaths       string
	SectionChronology  string
	SectionActions     string
	SectionFiles       string
	CloudFolder        string
	CloudSource        string
	SavePathLabel      string
	LocalContent       string
	CategoryLabel      string
	FrozenNote         string
	ProgressLabel      string
	ProgressSummary    string // printf: overall, offline, copy
	ProjectedState     string
	Created            string
	Updated            string
	PhaseStarted       string
	NextRun            string
	CompletedLabel     string
	Attempts           string
	RecordedError      string
	ActionStart        string
	ActionRetry        string
	ActionCancel       string
	ActionRemove       string
	ActionRemoveFiles  string
	ConfirmCancel      string
	ConfirmRemove      string
	ConfirmRemoveFiles string
	RemovalNote        string
	ThIndex            string
	ThPath             string
	ThSize             string
	NoFiles            string

	// Categories
	TitleCategories         string
	CategoriesLede          string
	SectionRegistered       string
	ThCloudPath             string
	ThSavePath              string
	ThAvailability          string
	UpdatedAtFormat         string // printf with RFC3339 time
	Save                    string
	NoCategories            string
	SectionRegister         string
	FieldName               string
	FieldCloudPath          string
	FieldSavePath           string
	FieldAvailability       string
	HintName                string
	HintCloud               string
	HintSave                string
	Enabled                 string
	Disabled                string
	RegisterButton          string
	CategoryRootLede        string
	CategoryOnboardingTitle string
	CategoryOnboardingBody  string
	FullPathLabel           string
	CategoryPathDetached    string
	CategorySubpathInvalid  string
	CategoryPrepareFailed   string
	CategoryRemapFailed     string

	// Values produced by view builders
	NotRecorded   string
	NotScheduled  string
	NotCompleted  string
	Uncategorized string
	RedactedError string

	// Password page
	NavPassword          string
	TitlePassword        string
	PasswordLede         string
	CurrentPassword      string
	NewPassword          string
	ConfirmPassword      string
	ChangeButton         string
	PasswordChanged      string
	PasswordTooShort     string
	PasswordMismatch     string
	PasswordWrongCurrent string

	// Setup wizard
	SetupTitle                       string
	SetupStepFormat                  string // printf with the current step number
	SetupStepPasswordTitle           string
	SetupStepPasswordDetail          string
	SetupStepCD2Title                string
	SetupStepCD2Detail               string
	SetupStepPathsTitle              string
	SetupStepPathsDetail             string
	SetupStepReviewTitle             string
	SetupStepReviewDetail            string
	SetupPasswordLede                string
	SetupConfirmPassword             string
	SetupSetPassword                 string
	SetupAlreadyConfigured           string
	CD2Address                       string
	CD2Insecure                      string
	CD2InsecureHint                  string
	CloudRootLabel                   string
	LocalRootLabel                   string
	CloudRootHint                    string
	LocalRootHint                    string
	CloudDirectoryUp                 string
	CloudDirectoryCurrent            string
	CloudDirectorySelect             string
	CloudDirectoryNoneSelected       string
	CloudDirectoryCreateLabel        string
	CloudDirectoryCreatePlaceholder  string
	CloudDirectoryCreateButton       string
	CloudDirectoryLoading            string
	CloudDirectoryEmpty              string
	CloudDirectoryListFailed         string
	CloudDirectoryCreateFailed       string
	CloudDirectoryPathInvalid        string
	CloudDirectoryNameInvalid        string
	CloudDirectoryConnectionRequired string
	LocalDirectoryListFailed         string
	LocalDirectoryCreateFailed       string
	LocalDirectoryPathInvalid        string
	SetupSessionExpired              string
	TestButton                       string
	ContinueButton                   string
	FinishButton                     string
	OfflineTimeoutLabel              string
	CopyTimeoutLabel                 string
	VerifyTimeoutLabel               string
	TimeoutFormatHint                string
	AdvancedSettings                 string
	PathFlowTitle                    string
	PathFlowCopy                     string
	PathFlowSharedRequirement        string

	// Setup & settings validation and test results
	TestPassed           string
	TestUnreachable      string // printf with the CloudDrive2 address
	TestTLS              string
	TestAuth             string
	TestOther            string
	AddressRequired      string
	AddressInvalid       string
	UsernameRequired     string
	CD2PasswordRequired  string
	CloudRootInvalid     string
	LocalRootInvalid     string
	CloudRootUnverified  string
	CloudRootNotDir      string
	LocalRootNotWritable string
	TimeoutInvalid       string
	ActivationFailed     string

	// Settings page
	NavSettings             string
	TitleSettings           string
	SettingsLede            string
	SettingsSectionCD2      string
	SettingsSectionPaths    string
	SettingsSectionTimeouts string
	CD2PasswordKeep         string
	SettingsSaveButton      string
	SettingsSaved           string
	SettingsApplyFailed     string
	SettingsFrozenPathsNote string
	SettingsRemapTitle      string
	SettingsRemapHint       string

	// Webhook endpoints
	NavWebhooks           string
	TitleWebhooks         string
	WebhooksLede          string
	SectionEndpoints      string
	NoEndpoints           string
	AddEndpoint           string
	EditEndpoint          string
	CancelButton          string
	EndpointName          string
	FieldURL              string
	HintURL               string
	FieldBearerToken      string
	BearerKeepHint        string
	ClearBearerToken      string
	SubscriptionsLabel    string
	SubscriptionCompleted string
	SubscriptionFailed    string
	CreateEndpointButton  string
	EndpointSaved         string
	URLQueryRedacted      string
	EndpointNameInvalid   string
	EndpointURLInvalid    string
	SubscriptionRequired  string
	BearerTooLong         string
	RotateSecret          string
	RotateSecretConfirm   string
	DeleteEndpoint        string
	DeleteEndpointConfirm string
	SendTest              string
	SendTestConfirm       string
	DisableEndpoint       string
	EnableEndpoint        string
	SecretSetMasked       string
	SecretTitle           string
	SecretLede            string
	SecretLabel           string
	SecretDone            string
	TestEnqueued          string

	// Webhook delivery history
	TitleDeliveries    string
	DeliveriesLede     string
	FilterEndpoint     string
	FilterEvent        string
	FilterStatus       string
	AllEndpoints       string
	AllEvents          string
	AllStatuses        string
	ThEndpoint         string
	ThEvent            string
	ThStatus           string
	ThHTTPStatus       string
	ThLastError        string
	ThNextAttempt      string
	ThDelivered        string
	NoDeliveries       string
	Replay             string
	ReplayConfirm      string
	ReplayEnqueued     string
	NextPage           string
	EventTest          string
	DeliveryPending    string
	DeliveryDelivering string
	DeliverySucceeded  string
	DeliveryDead       string
}

var stringsEN = Strings{
	NavDownloads:  "Downloads",
	NavCategories: "Categories",
	SignOut:       "Sign out",
	SwitchLang:    "中文",

	TitleSignIn: "Sign in",
	LoginHint:   "Sign in with the operator password set during initial setup.",
	Username:    "Username",
	Password:    "Password",
	SignIn:      "Sign in",
	LoginFailed: "The username or password did not match.",

	TitleDownloads:   "Downloads",
	ShownFormat:      "%d shown",
	CloudOnline:      "CloudDrive2 online",
	CloudUnavailable: "CloudDrive2 unavailable",
	FilterView:       "View",
	FilterCategory:   "Category",
	Apply:            "Apply",
	AllCategories:    "All categories",
	ViewActive:       "Active",
	ViewCompleted:    "Completed",
	ViewFailed:       "Failed",
	ViewCancelled:    "Cancelled",
	ViewAll:          "All records",
	ThName:           "Name",
	ThState:          "State",
	ThProgress:       "Progress",
	ThCategory:       "Category",
	ThUpdated:        "Updated",
	AgoFormat:        "%s ago",
	EmptyTitle:       "No downloads match this filter.",
	EmptyBody:        "Pick another view or category, or submit a release from Sonarr or Radarr.",
	States: StateLabels{
		Accepted:          "Accepted",
		Stopped:           "Stopped",
		SubmittingOffline: "Submitting offline download",
		WaitingOffline:    "Waiting for offline download",
		SubmittingCopy:    "Submitting copy",
		WaitingCopy:       "Waiting for copy",
		VerifyingLocal:    "Verifying local files",
		Completed:         "Completed",
		Failed:            "Failed",
		CancelRequested:   "Cancelling",
		Cancelled:         "Cancelled",
		DeleteRequested:   "Deleting",
		Deleted:           "Deleted",
	},

	Stage115:           "115 OFFLINE",
	StageCopy:          "NAS COPY",
	StageVerify:        "LOCAL VERIFY",
	StatusPending:      "Pending",
	StatusQueued:       "Queued",
	StatusActiveSuffix: " · Active",
	StatusSourceReady:  "Source ready",
	StatusCopyPresent:  "Copy present",
	StatusChecking:     "Checking",
	StatusVerified:     "Verified",

	SectionPaths:       "Paths",
	SectionChronology:  "Chronology",
	SectionActions:     "Actions",
	SectionFiles:       "Files",
	CloudFolder:        "115 category folder",
	CloudSource:        "115 download result",
	SavePathLabel:      "Shared staging folder",
	LocalContent:       "Verified content path",
	CategoryLabel:      "Category",
	FrozenNote:         "These paths were frozen when the download was accepted. Later root or category changes do not move existing data.",
	ProgressLabel:      "Progress",
	ProgressSummary:    "%s overall · offline %s · copy %s",
	ProjectedState:     "Projected state",
	Created:            "Created",
	Updated:            "Updated",
	PhaseStarted:       "Phase started",
	NextRun:            "Next run",
	CompletedLabel:     "Completed",
	Attempts:           "Attempts",
	RecordedError:      "Recorded error",
	ActionStart:        "Start",
	ActionRetry:        "Retry",
	ActionCancel:       "Cancel",
	ActionRemove:       "Remove record",
	ActionRemoveFiles:  "Remove + local files",
	ConfirmCancel:      "Cancel active work for this transfer?",
	ConfirmRemove:      "Remove this record? The 115 cloud copy is retained.",
	ConfirmRemoveFiles: "Remove this record and delete its local files? The 115 cloud copy is retained.",
	RemovalNote:        "Removal never deletes the 115 cloud copy.",
	ThIndex:            "#",
	ThPath:             "Relative path",
	ThSize:             "Size",
	NoFiles:            "No file entries were recorded for this transfer.",

	TitleCategories:         "Categories",
	CategoriesLede:          "A category chooses one child folder below each configured root. New downloads go to 115 first, then CloudDrive2 copies them to shared staging for Sonarr or Radarr to import.",
	SectionRegistered:       "Registered categories",
	ThCloudPath:             "115 category subfolder",
	ThSavePath:              "Shared staging subfolder",
	ThAvailability:          "Availability",
	UpdatedAtFormat:         "updated %s",
	Save:                    "Save",
	NoCategories:            "No categories yet. Add one matching the category configured in Sonarr or Radarr.",
	SectionRegister:         "Add a category",
	FieldName:               "Category name",
	FieldCloudPath:          "115 category subfolder",
	FieldSavePath:           "Shared staging subfolder",
	FieldAvailability:       "Availability",
	HintName:                "Use the same value in Sonarr or Radarr. Stored in lowercase.",
	HintCloud:               "Relative to the 115 offline download root. Do not start with /.",
	HintSave:                "Relative to the shared staging root. Do not start with /.",
	Enabled:                 "Enabled",
	Disabled:                "Disabled",
	RegisterButton:          "Add category",
	CategoryRootLede:        "Every category path is built from the configured root plus the subfolder below.",
	CategoryOnboardingTitle: "Last step: configure a Sonarr or Radarr category",
	CategoryOnboardingBody:  "Use the same category name in CD211 and the qBittorrent download client entry. Common examples are movies for Radarr and tv for Sonarr.",
	FullPathLabel:           "Full path",
	CategoryPathDetached:    "This category is outside the configured roots. Choose both subfolders before enabling it.",
	CategorySubpathInvalid:  "Enter clean relative subfolders without a leading slash, backslash, or parent-directory segment.",
	CategoryPrepareFailed:   "The shared staging subfolder could not be prepared. Check the root path and permissions.",
	CategoryRemapFailed:     "The roots were not changed because one or more category paths could not be safely remapped.",

	NotRecorded:   "Not recorded",
	NotScheduled:  "Not scheduled",
	NotCompleted:  "Not completed",
	Uncategorized: "Uncategorized",
	RedactedError: "Protected upstream details were redacted.",

	NavPassword:          "Change password",
	TitlePassword:        "Change password",
	PasswordLede:         "The username stays admin. Sonarr and Radarr sign in with the same password, so update their qBittorrent download client entry after changing it.",
	CurrentPassword:      "Current password",
	NewPassword:          "New password",
	ConfirmPassword:      "Confirm new password",
	ChangeButton:         "Change password",
	PasswordChanged:      "Password changed. Update the qBittorrent password configured in Sonarr and Radarr as well.",
	PasswordTooShort:     "The new password must be at least 8 characters long.",
	PasswordMismatch:     "The two new password entries do not match.",
	PasswordWrongCurrent: "The current password is incorrect.",

	SetupTitle:                       "Initial setup",
	SetupStepFormat:                  "Step %d of 4",
	SetupStepPasswordTitle:           "Operator password",
	SetupStepPasswordDetail:          "Secure the interface and API.",
	SetupStepCD2Title:                "CloudDrive2",
	SetupStepCD2Detail:               "Connect and verify the service.",
	SetupStepPathsTitle:              "File flow",
	SetupStepPathsDetail:             "Choose the 115 and shared staging roots.",
	SetupStepReviewTitle:             "Review",
	SetupStepReviewDetail:            "Check the complete file flow.",
	SetupPasswordLede:                "The operator password is used to sign in to this interface and by Sonarr and Radarr.",
	SetupConfirmPassword:             "Confirm password",
	SetupSetPassword:                 "Set password and continue",
	SetupAlreadyConfigured:           "Setup has already been completed. You can sign in with the operator password.",
	CD2Address:                       "CloudDrive2 address",
	CD2Insecure:                      "Allow insecure connection (plain HTTP)",
	CD2InsecureHint:                  "Enable when CloudDrive2 does not serve TLS.",
	CloudRootLabel:                   "115 offline download root",
	LocalRootLabel:                   "Shared staging root",
	CloudRootHint:                    "115 saves offline downloads in category subfolders below this root.",
	LocalRootHint:                    "CloudDrive2 writes here, CD211 verifies here, and Sonarr or Radarr imports from here. This is staging, not the final media library.",
	CloudDirectoryUp:                 "Up one level",
	CloudDirectoryCurrent:            "Browsing",
	CloudDirectorySelect:             "Use this folder",
	CloudDirectoryNoneSelected:       "No folder selected",
	CloudDirectoryCreateLabel:        "Create a folder here",
	CloudDirectoryCreatePlaceholder:  "Folder name",
	CloudDirectoryCreateButton:       "Create",
	CloudDirectoryLoading:            "Loading folders…",
	CloudDirectoryEmpty:              "No subfolders here.",
	CloudDirectoryListFailed:         "Could not load CloudDrive2 folders. Check the connection and try again.",
	CloudDirectoryCreateFailed:       "Could not create the folder in CloudDrive2.",
	CloudDirectoryPathInvalid:        "The CloudDrive2 folder path is invalid.",
	CloudDirectoryNameInvalid:        "Enter a folder name without slashes.",
	CloudDirectoryConnectionRequired: "Complete the CloudDrive2 connection step before choosing a download folder.",
	LocalDirectoryListFailed:         "Could not read local folders. Check that CD211 has access to this path.",
	LocalDirectoryCreateFailed:       "Could not create the local folder. Check the parent folder permissions.",
	LocalDirectoryPathInvalid:        "The local folder path is invalid.",
	SetupSessionExpired:              "The setup session expired. Return to setup and sign in again.",
	TestButton:                       "Test connection",
	ContinueButton:                   "Continue",
	FinishButton:                     "Finish setup and configure categories",
	OfflineTimeoutLabel:              "Offline download timeout",
	CopyTimeoutLabel:                 "Copy timeout",
	VerifyTimeoutLabel:               "Local verify timeout",
	TimeoutFormatHint:                "Go durations, for example 24h, 72h, 10m.",
	AdvancedSettings:                 "Advanced settings",
	PathFlowTitle:                    "Files pass through two locations",
	PathFlowCopy:                     "CloudDrive2 copies completed downloads",
	PathFlowSharedRequirement:        "CloudDrive2, CD211, Sonarr, and Radarr must all use the same absolute path for shared staging.",

	TestPassed:           "All checks passed.",
	TestUnreachable:      "Could not reach CloudDrive2 at %s. Check the address and the network.",
	TestTLS:              "The TLS connection to CloudDrive2 failed. If it serves plain HTTP, enable the insecure option.",
	TestAuth:             "CloudDrive2 rejected the username or password.",
	TestOther:            "The CloudDrive2 connection test failed.",
	AddressRequired:      "The CloudDrive2 address is required.",
	AddressInvalid:       "The CloudDrive2 address must be host:port with a port from 1 to 65535.",
	UsernameRequired:     "The CloudDrive2 username is required.",
	CD2PasswordRequired:  "The CloudDrive2 password is required.",
	CloudRootInvalid:     "The 115 offline download root must be an absolute clean path such as /115open/CD211.",
	LocalRootInvalid:     "The shared staging root must be an absolute clean path such as /downloads.",
	CloudRootUnverified:  "The cloud root directory could not be verified.",
	CloudRootNotDir:      "The cloud root path is not a directory.",
	LocalRootNotWritable: "The local root is not writable or does not exist.",
	TimeoutInvalid:       "Timeouts must be positive durations such as 24h or 10m.",
	ActivationFailed:     "The settings were saved, but activating them failed. Restart the service to apply them.",

	NavSettings:             "Settings",
	TitleSettings:           "Settings",
	SettingsLede:            "Connections and roots are tested before saving. Root changes remap category subfolders for future downloads.",
	SettingsSectionCD2:      "CloudDrive2 connection",
	SettingsSectionPaths:    "Roots and file flow",
	SettingsSectionTimeouts: "Timeouts",
	CD2PasswordKeep:         "Leave empty to keep the stored password.",
	SettingsSaveButton:      "Save settings",
	SettingsSaved:           "Settings saved and applied.",
	SettingsApplyFailed:     "The settings were saved, but applying them failed. They will take effect after a restart.",
	SettingsFrozenPathsNote: "Existing downloads keep their recorded paths and files are never moved. Only future downloads use remapped category paths.",
	SettingsRemapTitle:      "Category path changes",
	SettingsRemapHint:       "Changing either root keeps every category subfolder and previews its new full path below.",

	NavWebhooks:           "Webhooks",
	TitleWebhooks:         "Webhook endpoints",
	WebhooksLede:          "Deliver download completion and failure events to your own services with HMAC-signed HTTP requests.",
	SectionEndpoints:      "Endpoints",
	NoEndpoints:           "No endpoints yet. Add one to start receiving download events.",
	AddEndpoint:           "Add endpoint",
	EditEndpoint:          "Edit endpoint",
	CancelButton:          "Cancel",
	EndpointName:          "Name",
	FieldURL:              "Endpoint URL",
	HintURL:               "Absolute http or https URL without a username, password, or fragment.",
	FieldBearerToken:      "Bearer token",
	BearerKeepHint:        "Leave empty to keep the current token.",
	ClearBearerToken:      "Clear bearer token",
	SubscriptionsLabel:    "Events to deliver",
	SubscriptionCompleted: "Download completed",
	SubscriptionFailed:    "Download failed",
	CreateEndpointButton:  "Create endpoint",
	EndpointSaved:         "Endpoint updated.",
	URLQueryRedacted:      "Query strings are never shown; they are stored for delivery and displayed as ?….",
	EndpointNameInvalid:   "The name is required and must be 1–64 characters without control characters.",
	EndpointURLInvalid:    "The URL must be an absolute http or https URL without a username, password, or fragment.",
	SubscriptionRequired:  "Choose at least one event to deliver.",
	BearerTooLong:         "The bearer token must be at most 4096 bytes.",
	RotateSecret:          "Rotate secret",
	RotateSecretConfirm:   "Rotate the signing secret? The old secret stops working immediately.",
	DeleteEndpoint:        "Delete",
	DeleteEndpointConfirm: "Delete this endpoint? Pending deliveries are cancelled; history is retained.",
	SendTest:              "Send test",
	SendTestConfirm:       "Send a test delivery to this endpoint?",
	DisableEndpoint:       "Disable",
	EnableEndpoint:        "Enable",
	SecretSetMasked:       "A signing secret is set. Rotate it to generate a new one.",
	SecretTitle:           "Webhook signing secret",
	SecretLede:            "Copy this secret now. It is shown only once and cannot be recovered later.",
	SecretLabel:           "HMAC-SHA256 signing secret",
	SecretDone:            "Done",
	TestEnqueued:          "Test delivery enqueued.",

	TitleDeliveries:    "Webhook deliveries",
	DeliveriesLede:     "Delivery history for every webhook event, including retries and dead letters.",
	FilterEndpoint:     "Endpoint",
	FilterEvent:        "Event",
	FilterStatus:       "Status",
	AllEndpoints:       "All endpoints",
	AllEvents:          "All events",
	AllStatuses:        "All statuses",
	ThEndpoint:         "Endpoint",
	ThEvent:            "Event",
	ThStatus:           "Status",
	ThHTTPStatus:       "HTTP",
	ThLastError:        "Last error",
	ThNextAttempt:      "Next attempt",
	ThDelivered:        "Delivered",
	NoDeliveries:       "No deliveries match these filters.",
	Replay:             "Replay",
	ReplayConfirm:      "Replay this dead-letter delivery? It will be retried for up to 24 hours.",
	ReplayEnqueued:     "Delivery reopened for replay.",
	NextPage:           "Next",
	EventTest:          "Test",
	DeliveryPending:    "Pending",
	DeliveryDelivering: "Delivering",
	DeliverySucceeded:  "Succeeded",
	DeliveryDead:       "Dead letter",
}

var stringsZH = Strings{
	NavDownloads:  "下载任务",
	NavCategories: "分类管理",
	SignOut:       "退出登录",
	SwitchLang:    "English",

	TitleSignIn: "登录",
	LoginHint:   "请使用初始设置时设定的操作员密码登录。",
	Username:    "用户名",
	Password:    "密码",
	SignIn:      "登录",
	LoginFailed: "用户名或密码不正确。",

	TitleDownloads:   "下载任务",
	ShownFormat:      "共 %d 条",
	CloudOnline:      "CloudDrive2 在线",
	CloudUnavailable: "CloudDrive2 不可用",
	FilterView:       "视图",
	FilterCategory:   "分类",
	Apply:            "应用",
	AllCategories:    "全部分类",
	ViewActive:       "进行中",
	ViewCompleted:    "已完成",
	ViewFailed:       "已失败",
	ViewCancelled:    "已取消",
	ViewAll:          "全部记录",
	ThName:           "名称",
	ThState:          "状态",
	ThProgress:       "进度",
	ThCategory:       "分类",
	ThUpdated:        "更新时间",
	AgoFormat:        "%s 前",
	EmptyTitle:       "没有符合筛选条件的下载任务。",
	EmptyBody:        "可尝试切换视图或分类，也可以从 Sonarr 或 Radarr 提交新任务。",
	States: StateLabels{
		Accepted:          "待处理",
		Stopped:           "已停止",
		SubmittingOffline: "正在提交离线下载",
		WaitingOffline:    "等待离线下载",
		SubmittingCopy:    "正在提交复制任务",
		WaitingCopy:       "等待复制完成",
		VerifyingLocal:    "正在本地校验",
		Completed:         "已完成",
		Failed:            "已失败",
		CancelRequested:   "正在取消",
		Cancelled:         "已取消",
		DeleteRequested:   "正在删除",
		Deleted:           "已删除",
	},

	Stage115:           "115 离线下载",
	StageCopy:          "复制到 NAS",
	StageVerify:        "本地校验",
	StatusPending:      "等待中",
	StatusQueued:       "排队中",
	StatusActiveSuffix: " · 进行中",
	StatusSourceReady:  "源已就绪",
	StatusCopyPresent:  "复制完成",
	StatusChecking:     "校验中",
	StatusVerified:     "已校验",

	SectionPaths:       "路径",
	SectionChronology:  "时间线",
	SectionActions:     "操作",
	CloudFolder:        "115 分类目录",
	CloudSource:        "115 下载结果",
	SavePathLabel:      "共享暂存目录",
	LocalContent:       "已校验内容路径",
	CategoryLabel:      "分类",
	FrozenNote:         "任务提交后，这些路径不会再变；之后修改根目录或分类也不会移动已有数据。",
	ProgressLabel:      "进度",
	ProgressSummary:    "总进度 %s · 离线 %s · 复制 %s",
	ProjectedState:     "qBittorrent 状态",
	Created:            "创建时间",
	Updated:            "更新时间",
	PhaseStarted:       "阶段开始时间",
	NextRun:            "下次调度",
	CompletedLabel:     "完成时间",
	Attempts:           "尝试次数",
	RecordedError:      "错误信息",
	ActionStart:        "开始",
	ActionRetry:        "重试",
	ActionCancel:       "取消",
	ActionRemove:       "删除记录",
	ActionRemoveFiles:  "删除记录和本地文件",
	ConfirmCancel:      "确定要取消这个任务吗？",
	ConfirmRemove:      "确定要删除这条记录吗？115 网盘中的副本会保留。",
	ConfirmRemoveFiles: "确定要删除这条记录及其本地文件吗？115 网盘中的副本会保留。",
	RemovalNote:        "删除记录或本地文件不会影响 115 网盘中的副本。",
	ThIndex:            "#",
	ThPath:             "相对路径",
	ThSize:             "大小",
	NoFiles:            "该任务暂无文件记录。",

	TitleCategories:         "分类管理",
	CategoriesLede:          "每个分类只需指定两个根目录下的子目录。任务先下载到 115，再由 CloudDrive2 复制到共享暂存目录，供 Sonarr 或 Radarr 导入。",
	SectionRegistered:       "已有分类",
	ThCloudPath:             "115 分类子目录",
	ThSavePath:              "共享暂存子目录",
	ThAvailability:          "启用状态",
	UpdatedAtFormat:         "更新于 %s",
	Save:                    "保存",
	NoCategories:            "尚未配置分类。请添加一个与 Sonarr 或 Radarr 设置一致的分类。",
	SectionRegister:         "添加分类",
	FieldName:               "分类名称",
	FieldCloudPath:          "115 分类子目录",
	FieldSavePath:           "共享暂存子目录",
	FieldAvailability:       "启用状态",
	HintName:                "请与 Sonarr 或 Radarr 中的分类保持一致；保存时转为小写。",
	HintCloud:               "相对于 115 离线下载根目录，请勿以 / 开头。",
	HintSave:                "相对于共享暂存根目录，请勿以 / 开头。",
	Enabled:                 "启用",
	Disabled:                "停用",
	RegisterButton:          "添加分类",
	CategoryRootLede:        "每条分类路径都由已配置的根目录与下方填写的子目录拼接而成。",
	CategoryOnboardingTitle: "最后一步：配置 Sonarr 或 Radarr 分类",
	CategoryOnboardingBody:  "CD211 与 qBittorrent 下载客户端配置必须使用相同分类名。Radarr 通常使用 movies，Sonarr 通常使用 tv。",
	FullPathLabel:           "完整路径",
	CategoryPathDetached:    "此分类已脱离当前根目录，请重新填写两个子目录后再启用。",
	CategorySubpathInvalid:  "请输入有效的相对子目录路径；路径不能以斜杠开头，也不能包含反斜杠或上级目录。",
	CategoryPrepareFailed:   "无法准备共享暂存子目录，请检查根目录和目录权限。",
	CategoryRemapFailed:     "一个或多个分类路径无法安全映射，因此没有修改根目录。",

	NotRecorded:   "未记录",
	NotScheduled:  "未调度",
	NotCompleted:  "未完成",
	Uncategorized: "未分类",
	RedactedError: "上游错误包含敏感信息，已脱敏。",

	NavPassword:          "修改密码",
	TitlePassword:        "修改密码",
	PasswordLede:         "用户名固定为 admin。Sonarr 和 Radarr 连接 CD211 时也使用此密码；修改密码后，请同步更新两者的 qBittorrent 下载客户端配置。",
	CurrentPassword:      "当前密码",
	NewPassword:          "新密码",
	ConfirmPassword:      "确认新密码",
	ChangeButton:         "修改密码",
	PasswordChanged:      "密码已修改。请同步更新 Sonarr 和 Radarr 中配置的 qBittorrent 密码。",
	PasswordTooShort:     "新密码长度至少为 8 个字符。",
	PasswordMismatch:     "两次输入的新密码不一致。",
	PasswordWrongCurrent: "当前密码不正确。",

	SetupTitle:                       "初始设置",
	SetupStepFormat:                  "第 %d 步，共 4 步",
	SetupStepPasswordTitle:           "操作员密码",
	SetupStepPasswordDetail:          "保护管理界面与 API",
	SetupStepCD2Title:                "云盘连接",
	SetupStepCD2Detail:               "配置并测试 CloudDrive2 连接",
	SetupStepPathsTitle:              "文件流向",
	SetupStepPathsDetail:             "选择 115 与共享暂存根目录",
	SetupStepReviewTitle:             "确认配置",
	SetupStepReviewDetail:            "检查完整文件流向",
	SetupPasswordLede:                "操作员密码用于登录本界面，Sonarr 和 Radarr 连接 CD211 时也使用此密码。",
	SetupConfirmPassword:             "确认密码",
	SetupSetPassword:                 "设置密码并继续",
	SetupAlreadyConfigured:           "初始设置已完成，可以使用操作员密码登录。",
	CD2Address:                       "CloudDrive2 地址",
	CD2Insecure:                      "允许不安全连接（明文 HTTP）",
	CD2InsecureHint:                  "如果 CloudDrive2 使用明文 HTTP，请启用此选项。",
	CloudRootLabel:                   "115 离线下载根目录",
	LocalRootLabel:                   "共享暂存根目录",
	CloudRootHint:                    "115 会将离线任务下载到此根目录下对应的分类子目录。",
	LocalRootHint:                    "CloudDrive2 写入、CD211 校验、Sonarr 或 Radarr 从这里导入。这里是中转区，不是最终媒体库。",
	CloudDirectoryUp:                 "返回上一级",
	CloudDirectoryCurrent:            "正在浏览",
	CloudDirectorySelect:             "使用此目录",
	CloudDirectoryNoneSelected:       "尚未选择目录",
	CloudDirectoryCreateLabel:        "在此新建目录",
	CloudDirectoryCreatePlaceholder:  "目录名称",
	CloudDirectoryCreateButton:       "新建",
	CloudDirectoryLoading:            "正在加载目录…",
	CloudDirectoryEmpty:              "此处没有子目录。",
	CloudDirectoryListFailed:         "无法读取 CloudDrive2 目录，请检查连接后重试。",
	CloudDirectoryCreateFailed:       "无法在 CloudDrive2 中新建目录。",
	CloudDirectoryPathInvalid:        "CloudDrive2 目录路径无效。",
	CloudDirectoryNameInvalid:        "请输入不含斜杠的目录名称。",
	CloudDirectoryConnectionRequired: "请先完成 CloudDrive2 连接设置，再选择下载目录。",
	LocalDirectoryListFailed:         "无法读取本地目录，请确认 CD211 有权访问此路径。",
	LocalDirectoryCreateFailed:       "无法新建本地目录，请检查上级目录的权限。",
	LocalDirectoryPathInvalid:        "本地目录路径无效。",
	SetupSessionExpired:              "设置会话已过期，请返回初始设置页面并重新登录。",
	TestButton:                       "测试连接",
	ContinueButton:                   "继续",
	FinishButton:                     "完成设置并配置分类",
	OfflineTimeoutLabel:              "离线下载超时",
	CopyTimeoutLabel:                 "复制超时",
	VerifyTimeoutLabel:               "本地校验超时",
	TimeoutFormatHint:                "请输入时长，例如 24h（24 小时）或 10m（10 分钟）。",
	AdvancedSettings:                 "高级设置",
	PathFlowTitle:                    "文件会依次经过两个位置",
	PathFlowCopy:                     "下载完成后由 CloudDrive2 复制到共享暂存目录",
	PathFlowSharedRequirement:        "CloudDrive2、CD211、Sonarr 和 Radarr 必须使用同一个绝对路径访问共享暂存目录。",

	TestPassed:           "所有检查均已通过。",
	TestUnreachable:      "无法连接地址 %s 上的 CloudDrive2，请检查地址和网络。",
	TestTLS:              "CloudDrive2 的 TLS 连接失败。如果 CloudDrive2 使用明文 HTTP，请启用“允许不安全连接”选项。",
	TestAuth:             "CloudDrive2 拒绝了登录请求，请检查用户名和密码。",
	TestOther:            "CloudDrive2 连接测试失败。",
	AddressRequired:      "请填写 CloudDrive2 地址。",
	AddressInvalid:       "CloudDrive2 地址格式应为“主机名:端口”，端口范围为 1–65535。",
	UsernameRequired:     "请填写 CloudDrive2 用户名。",
	CD2PasswordRequired:  "请填写 CloudDrive2 密码。",
	CloudRootInvalid:     "115 离线下载根目录必须是规范的绝对路径，例如 /115open/CD211。",
	LocalRootInvalid:     "共享暂存根目录必须是规范的绝对路径，例如 /downloads。",
	CloudRootUnverified:  "无法确认云端根目录是否可用。",
	CloudRootNotDir:      "指定的云端根目录不是目录。",
	LocalRootNotWritable: "本地根目录不存在或不可写。",
	TimeoutInvalid:       "超时时间必须大于 0，例如 24h 或 10m。",
	ActivationFailed:     "设置已保存，但未能生效。请重启服务以应用设置。",

	NavSettings:             "设置",
	TitleSettings:           "设置",
	SettingsLede:            "保存前会测试连接和根目录。修改根目录时，分类子目录会自动映射到新位置并用于后续任务。",
	SettingsSectionCD2:      "CloudDrive2 连接",
	SettingsSectionPaths:    "根目录与文件流向",
	SettingsSectionTimeouts: "超时",
	CD2PasswordKeep:         "留空则保留当前密码。",
	SettingsSaveButton:      "保存设置",
	SettingsSaved:           "设置已保存并生效。",
	SettingsApplyFailed:     "设置已保存，但未能立即生效；重启服务后将自动生效。",
	SettingsFrozenPathsNote: "已有任务仍使用提交时记录的路径，文件不会移动；重新映射后的分类路径仅用于后续任务。",
	SettingsRemapTitle:      "分类路径变化",
	SettingsRemapHint:       "修改任一根目录时会保留每个分类的子目录；下方会预览新的完整路径。",

	NavWebhooks:           "Webhook 管理",
	TitleWebhooks:         "Webhook 端点",
	WebhooksLede:          "通过带 HMAC 签名的 HTTP 请求，将下载完成和失败事件推送到自有服务。",
	SectionEndpoints:      "端点列表",
	NoEndpoints:           "还没有端点。添加一个即可开始接收下载事件。",
	AddEndpoint:           "添加端点",
	EditEndpoint:          "编辑端点",
	CancelButton:          "取消",
	EndpointName:          "名称",
	FieldURL:              "端点地址",
	HintURL:               "请填写绝对 HTTP 或 HTTPS URL，且不能包含用户名、密码或片段标识符（fragment）。",
	FieldBearerToken:      "Bearer 令牌",
	BearerKeepHint:        "留空则保留当前令牌。",
	ClearBearerToken:      "清除 Bearer 令牌",
	SubscriptionsLabel:    "要推送的事件",
	SubscriptionCompleted: "下载完成",
	SubscriptionFailed:    "下载失败",
	CreateEndpointButton:  "创建端点",
	EndpointSaved:         "端点已更新。",
	URLQueryRedacted:      "查询字符串仅用于投递；界面一律显示为“?…”，不会展示原始内容。",
	EndpointNameInvalid:   "名称不能为空，长度须为 1–64 个字符且不能包含控制字符。",
	EndpointURLInvalid:    "地址必须是绝对 HTTP 或 HTTPS URL，且不能包含用户名、密码或片段标识符。",
	SubscriptionRequired:  "请至少选择一种要推送的事件。",
	BearerTooLong:         "Bearer 令牌不能超过 4096 字节。",
	RotateSecret:          "轮换密钥",
	RotateSecretConfirm:   "确定轮换签名密钥吗？旧密钥将立即失效。",
	DeleteEndpoint:        "删除",
	DeleteEndpointConfirm: "确定删除此端点吗？删除后将取消待投递的请求，但保留历史记录。",
	SendTest:              "发送测试投递",
	SendTestConfirm:       "确定要向此端点发送一次测试投递吗？",
	DisableEndpoint:       "停用",
	EnableEndpoint:        "启用",
	SecretSetMasked:       "已设置签名密钥。需要更换时请点击“轮换密钥”。",
	SecretTitle:           "Webhook 签名密钥",
	SecretLede:            "请立即复制并妥善保存此密钥。此密钥仅显示一次，之后无法查看。",
	SecretLabel:           "HMAC-SHA256 签名密钥",
	SecretDone:            "完成",
	TestEnqueued:          "测试投递已加入队列。",

	TitleDeliveries:    "Webhook 投递记录",
	DeliveriesLede:     "查看每个 Webhook 事件的投递历史，包括重试与死信记录。",
	FilterEndpoint:     "端点",
	FilterEvent:        "事件",
	FilterStatus:       "状态",
	AllEndpoints:       "全部端点",
	AllEvents:          "全部事件",
	AllStatuses:        "全部状态",
	ThEndpoint:         "端点",
	ThEvent:            "事件",
	ThStatus:           "状态",
	ThHTTPStatus:       "HTTP",
	ThLastError:        "最新错误",
	ThNextAttempt:      "下次投递",
	ThDelivered:        "投递时间",
	NoDeliveries:       "没有符合筛选条件的投递记录。",
	Replay:             "重新投递",
	ReplayConfirm:      "确定重新投递这条死信记录吗？系统随后会持续重试，重试期最长为 24 小时。",
	ReplayEnqueued:     "投递已重新排队。",
	NextPage:           "下一页",
	EventTest:          "测试",
	DeliveryPending:    "待投递",
	DeliveryDelivering: "投递中",
	DeliverySucceeded:  "已投递",
	DeliveryDead:       "死信",
}

// tr returns the string table for lang, defaulting to English.
func tr(lang Lang) *Strings {
	if lang == LangZH {
		return &stringsZH
	}
	return &stringsEN
}

// otherLang returns the language the toggle link switches to.
func otherLang(lang Lang) Lang {
	if lang == LangZH {
		return LangEN
	}
	return LangZH
}

// requestLang reads the display-language preference; unknown values fall back
// to English.
func requestLang(r *http.Request) Lang {
	cookie, err := r.Cookie(langCookie)
	if err == nil && Lang(cookie.Value) == LangZH {
		return LangZH
	}
	return LangEN
}
