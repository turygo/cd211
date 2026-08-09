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
	TitleCategories   string
	CategoriesLede    string
	SectionRegistered string
	ThCloudPath       string
	ThSavePath        string
	ThAvailability    string
	UpdatedAtFormat   string // printf with RFC3339 time
	Save              string
	NoCategories      string
	SectionRegister   string
	FieldName         string
	FieldCloudPath    string
	FieldSavePath     string
	FieldAvailability string
	HintName          string
	HintCloud         string
	HintSave          string
	Enabled           string
	Disabled          string
	RegisterButton    string

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
	CloudFolder:        "Cloud folder",
	CloudSource:        "Cloud source",
	SavePathLabel:      "Save path",
	LocalContent:       "Local content",
	CategoryLabel:      "Category",
	FrozenNote:         "Paths were frozen when the download was accepted. Later category edits do not move existing data.",
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

	TitleCategories:   "Categories",
	CategoriesLede:    "Each category maps a 115 cloud folder to a local staging directory. Category changes affect future submissions only; existing downloads keep their frozen paths.",
	SectionRegistered: "Registered categories",
	ThCloudPath:       "Cloud path",
	ThSavePath:        "Local save path",
	ThAvailability:    "Availability",
	UpdatedAtFormat:   "updated %s",
	Save:              "Save",
	NoCategories:      "No categories are registered yet. Add the first one below.",
	SectionRegister:   "Register a category",
	FieldName:         "Name",
	FieldCloudPath:    "Cloud path",
	FieldSavePath:     "Local save path",
	FieldAvailability: "Availability",
	HintName:          "Trimmed and stored in lowercase.",
	HintCloud:         "Must be below the configured cloud root.",
	HintSave:          "Must be below the configured local root.",
	Enabled:           "Enabled",
	Disabled:          "Disabled",
	RegisterButton:    "Register category",

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
	SetupStepPathsTitle:              "Storage paths",
	SetupStepPathsDetail:             "Map the cloud and local roots.",
	SetupStepReviewTitle:             "Review",
	SetupStepReviewDetail:            "Check settings and finish.",
	SetupPasswordLede:                "The operator password is used to sign in to this interface and by Sonarr and Radarr.",
	SetupConfirmPassword:             "Confirm password",
	SetupSetPassword:                 "Set password and continue",
	SetupAlreadyConfigured:           "Setup has already been completed. You can sign in with the operator password.",
	CD2Address:                       "CloudDrive2 address",
	CD2Insecure:                      "Allow insecure connection (plain HTTP)",
	CD2InsecureHint:                  "Enable when CloudDrive2 does not serve TLS.",
	CloudRootLabel:                   "CloudDrive2 download folder",
	LocalRootLabel:                   "Local staging folder",
	CloudRootHint:                    "CloudDrive2 saves offline downloads here before CD211 copies them to local staging.",
	LocalRootHint:                    "CD211 stages files copied from CloudDrive2 here. Choose a folder that CD211 can access at the same path used by Sonarr and Radarr.",
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
	FinishButton:                     "Finish setup",
	OfflineTimeoutLabel:              "Offline download timeout",
	CopyTimeoutLabel:                 "Copy timeout",
	VerifyTimeoutLabel:               "Local verify timeout",
	TimeoutFormatHint:                "Go durations, for example 24h, 72h, 10m.",
	AdvancedSettings:                 "Advanced settings",

	TestPassed:           "All checks passed.",
	TestUnreachable:      "Could not reach CloudDrive2 at %s. Check the address and the network.",
	TestTLS:              "The TLS connection to CloudDrive2 failed. If it serves plain HTTP, enable the insecure option.",
	TestAuth:             "CloudDrive2 rejected the username or password.",
	TestOther:            "The CloudDrive2 connection test failed.",
	AddressRequired:      "The CloudDrive2 address is required.",
	AddressInvalid:       "The CloudDrive2 address must be host:port with a port from 1 to 65535.",
	UsernameRequired:     "The CloudDrive2 username is required.",
	CD2PasswordRequired:  "The CloudDrive2 password is required.",
	CloudRootInvalid:     "The cloud root must be an absolute clean path such as /cloud.",
	LocalRootInvalid:     "The local root must be an absolute clean path such as /data/downloads.",
	CloudRootUnverified:  "The cloud root directory could not be verified.",
	CloudRootNotDir:      "The cloud root path is not a directory.",
	LocalRootNotWritable: "The local root is not writable or does not exist.",
	TimeoutInvalid:       "Timeouts must be positive durations such as 24h or 10m.",
	ActivationFailed:     "The settings were saved, but activating them failed. Restart the service to apply them.",

	NavSettings:             "Settings",
	TitleSettings:           "Settings",
	SettingsLede:            "Connection and path settings are tested before they are saved and take effect immediately.",
	SettingsSectionCD2:      "CloudDrive2 connection",
	SettingsSectionPaths:    "Paths",
	SettingsSectionTimeouts: "Timeouts",
	CD2PasswordKeep:         "Leave empty to keep the stored password.",
	SettingsSaveButton:      "Save settings",
	SettingsSaved:           "Settings saved and applied.",
	SettingsApplyFailed:     "The settings were saved, but applying them failed. They will take effect after a restart.",
	SettingsFrozenPathsNote: "Path changes never affect downloads that have already been accepted; those keep the paths recorded at accept time.",
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
	SectionFiles:       "文件",
	CloudFolder:        "云端目录",
	CloudSource:        "云端源路径",
	SavePathLabel:      "保存路径",
	LocalContent:       "本地内容",
	CategoryLabel:      "分类",
	FrozenNote:         "任务创建后，相关路径即固定不变；之后修改分类不会移动已有数据。",
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

	TitleCategories:   "分类管理",
	CategoriesLede:    "每个分类都将 115 云端目录映射到本地暂存目录。分类修改仅影响后续提交的任务，已有任务仍使用原路径。",
	SectionRegistered: "已有分类",
	ThCloudPath:       "云端路径",
	ThSavePath:        "本地保存路径",
	ThAvailability:    "启用状态",
	UpdatedAtFormat:   "更新于 %s",
	Save:              "保存",
	NoCategories:      "暂无分类，请在下方添加。",
	SectionRegister:   "添加分类",
	FieldName:         "名称",
	FieldCloudPath:    "云端路径",
	FieldSavePath:     "本地保存路径",
	FieldAvailability: "启用状态",
	HintName:          "自动去除首尾空格并转为小写。",
	HintCloud:         "必须位于已配置的云端根目录下。",
	HintSave:          "必须位于已配置的本地根目录下。",
	Enabled:           "启用",
	Disabled:          "停用",
	RegisterButton:    "添加分类",

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
	SetupStepPathsTitle:              "存储路径",
	SetupStepPathsDetail:             "设置云端与本地目录",
	SetupStepReviewTitle:             "确认配置",
	SetupStepReviewDetail:            "检查无误后完成设置",
	SetupPasswordLede:                "操作员密码用于登录本界面，Sonarr 和 Radarr 连接 CD211 时也使用此密码。",
	SetupConfirmPassword:             "确认密码",
	SetupSetPassword:                 "设置密码并继续",
	SetupAlreadyConfigured:           "初始设置已完成，可以使用操作员密码登录。",
	CD2Address:                       "CloudDrive2 地址",
	CD2Insecure:                      "允许不安全连接（明文 HTTP）",
	CD2InsecureHint:                  "如果 CloudDrive2 使用明文 HTTP，请启用此选项。",
	CloudRootLabel:                   "CloudDrive2 下载目录",
	LocalRootLabel:                   "本地暂存目录",
	CloudRootHint:                    "CloudDrive2 会先将离线下载的内容保存到此目录，再由 CD211 复制到本地暂存目录。",
	LocalRootHint:                    "CD211 会将从 CloudDrive2 复制的内容暂存于此；请选择 CD211 可访问的目录，并在 Sonarr 和 Radarr 中使用相同的路径。",
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
	FinishButton:                     "完成设置",
	OfflineTimeoutLabel:              "离线下载超时",
	CopyTimeoutLabel:                 "复制超时",
	VerifyTimeoutLabel:               "本地校验超时",
	TimeoutFormatHint:                "请输入时长，例如 24h（24 小时）或 10m（10 分钟）。",
	AdvancedSettings:                 "高级设置",

	TestPassed:           "所有检查均已通过。",
	TestUnreachable:      "无法连接地址 %s 上的 CloudDrive2，请检查地址和网络。",
	TestTLS:              "CloudDrive2 的 TLS 连接失败。如果 CloudDrive2 使用明文 HTTP，请启用“允许不安全连接”选项。",
	TestAuth:             "CloudDrive2 拒绝了登录请求，请检查用户名和密码。",
	TestOther:            "CloudDrive2 连接测试失败。",
	AddressRequired:      "请填写 CloudDrive2 地址。",
	AddressInvalid:       "CloudDrive2 地址格式应为“主机名:端口”，端口范围为 1–65535。",
	UsernameRequired:     "请填写 CloudDrive2 用户名。",
	CD2PasswordRequired:  "请填写 CloudDrive2 密码。",
	CloudRootInvalid:     "云端根目录必须是规范的绝对路径，例如 /cloud。",
	LocalRootInvalid:     "本地根目录必须是规范的绝对路径，例如 /data/downloads。",
	CloudRootUnverified:  "无法确认云端根目录是否可用。",
	CloudRootNotDir:      "指定的云端根目录不是目录。",
	LocalRootNotWritable: "本地根目录不存在或不可写。",
	TimeoutInvalid:       "超时时间必须大于 0，例如 24h 或 10m。",
	ActivationFailed:     "设置已保存，但未能生效。请重启服务以应用设置。",

	NavSettings:             "设置",
	TitleSettings:           "设置",
	SettingsLede:            "保存前会先测试连接和路径设置，保存后立即生效。",
	SettingsSectionCD2:      "CloudDrive2 连接",
	SettingsSectionPaths:    "路径",
	SettingsSectionTimeouts: "超时",
	CD2PasswordKeep:         "留空则保留当前密码。",
	SettingsSaveButton:      "保存设置",
	SettingsSaved:           "设置已保存并生效。",
	SettingsApplyFailed:     "设置已保存，但未能立即生效；重启服务后将自动生效。",
	SettingsFrozenPathsNote: "修改路径不会影响已有任务，这些任务将继续使用创建时记录的路径。",
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
