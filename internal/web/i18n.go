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
}

var stringsEN = Strings{
	NavDownloads:  "Downloads",
	NavCategories: "Categories",
	SignOut:       "Sign out",
	SwitchLang:    "中文",

	TitleSignIn: "Sign in",
	LoginHint:   "Initial credentials are admin / adminadmin. The password can be changed after signing in.",
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
}

var stringsZH = Strings{
	NavDownloads:  "下载任务",
	NavCategories: "分类管理",
	SignOut:       "退出登录",
	SwitchLang:    "English",

	TitleSignIn: "登录",
	LoginHint:   "初始用户名为 admin，密码为 adminadmin；登录后可修改密码。",
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
