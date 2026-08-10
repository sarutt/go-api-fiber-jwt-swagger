package main

import "time"

// EpisodeStatus is the stage an episode currently sits at in the production
// pipeline. Episodes only ever move between stages through the transition
// table in pipeline_state.go.
type EpisodeStatus string

const (
	StatusIdeaBacklog       EpisodeStatus = "IDEA_BACKLOG"
	StatusScriptDraft       EpisodeStatus = "SCRIPT_DRAFT"
	StatusScriptApproved    EpisodeStatus = "SCRIPT_APPROVED"
	StatusVOGenerated       EpisodeStatus = "VO_GENERATED"
	StatusMusicGenerated    EpisodeStatus = "MUSIC_GENERATED"
	StatusAnimationRendered EpisodeStatus = "ANIMATION_RENDERED"
	StatusAssembled         EpisodeStatus = "ASSEMBLED"
	StatusQAReview          EpisodeStatus = "QA_REVIEW"
	StatusPlatformAdapted   EpisodeStatus = "PLATFORM_ADAPTED"
	StatusScheduled         EpisodeStatus = "SCHEDULED"
	StatusPublished         EpisodeStatus = "PUBLISHED"
	StatusAnalyzed          EpisodeStatus = "ANALYZED"
	StatusCancelled         EpisodeStatus = "CANCELLED"
	// StatusFailed parks an episode a worker could not finish, so a job that
	// keeps failing stops being retried and waits for a person instead.
	StatusFailed EpisodeStatus = "FAILED"
)

// maxAttempts is how many times a stage is retried before the episode is
// parked in FAILED. Without a cap a permanently failing job cycles forever,
// quietly spending generation budget on every attempt.
const maxAttempts = 3

// ControlScopeAll pauses the whole pipeline rather than a single stage.
const ControlScopeAll = "ALL"

// Gate names for the two mandatory human review points.
const (
	Gate1Script  = "GATE_1_SCRIPT"
	Gate2Release = "GATE_2_RELEASE"
)

// Approval decisions.
const (
	DecisionApproved = "APPROVED"
	DecisionRejected = "REJECTED"
)

// Curriculum themes, rotating on a four week cycle.
const (
	ThemeCounting = "COUNTING"
	ThemeLetters  = "LETTERS"
	ThemeEmotions = "EMOTIONS"
	ThemeKindness = "KINDNESS"
)

// AssetKind identifies what a produced file actually is.
type AssetKind string

const (
	AssetScript      AssetKind = "SCRIPT"
	AssetVoiceOver   AssetKind = "VOICEOVER"
	AssetMusic       AssetKind = "MUSIC"
	AssetAnimation   AssetKind = "ANIMATION"
	AssetMasterVideo AssetKind = "MASTER_VIDEO"
	AssetShortsCut   AssetKind = "SHORTS_CUT"
	AssetTikTokCut   AssetKind = "TIKTOK_CUT"
	AssetThumbnail   AssetKind = "THUMBNAIL"
)

// Episode is one show episode moving through the production pipeline.
type Episode struct {
	ID                uint          `json:"id" gorm:"primaryKey"`
	Code              string        `json:"code" gorm:"uniqueIndex;size:32"`
	Title             string        `json:"title" gorm:"size:200"`
	Synopsis          string        `json:"synopsis"`
	Status            EpisodeStatus `json:"status" gorm:"index;size:32"`
	CurriculumTopicID *uint         `json:"curriculum_topic_id"`
	ScriptText        string        `json:"script_text"`
	SongTitle         string        `json:"song_title" gorm:"size:200"`
	SongLyrics        string        `json:"song_lyrics"`
	DurationSeconds   int           `json:"duration_seconds"`
	// MadeForKids drives the YouTube selfDeclaredMadeForKids flag. It defaults
	// to true because every episode of this show is children's content, and
	// getting this wrong is a COPPA problem rather than a cosmetic one.
	MadeForKids    bool       `json:"made_for_kids" gorm:"default:true"`
	AIDisclosure   bool       `json:"ai_disclosure" gorm:"default:true"`
	ScheduledAt    *time.Time `json:"scheduled_at"`
	PublishedAt    *time.Time `json:"published_at"`
	YouTubeVideoID string     `json:"youtube_video_id" gorm:"size:64"`
	TikTokVideoID  string     `json:"tiktok_video_id" gorm:"size:64"`
	// ClaimedBy is the worker currently holding this episode, and ClaimedAt
	// is when it took it. The claim is a lease: it expires so a worker that
	// dies mid-job does not strand the episode. Both are cleared whenever the
	// episode changes stage.
	ClaimedBy string     `json:"claimed_by" gorm:"index;size:120"`
	ClaimedAt *time.Time `json:"claimed_at"`
	// Attempts counts how many times the current stage has been tried. It
	// resets whenever the episode moves on, so it measures this stage rather
	// than the episode's whole history.
	Attempts   int           `json:"attempts"`
	LastError  string        `json:"last_error"`
	FailedFrom EpisodeStatus `json:"failed_from" gorm:"size:32"`
	CreatedAt  time.Time     `json:"created_at"`
	UpdatedAt  time.Time     `json:"updated_at"`
}

// PipelineControl is the admin's stop switch. One row per scope: the whole
// pipeline, or a single stage.
type PipelineControl struct {
	ID        uint       `json:"id" gorm:"primaryKey"`
	Scope     string     `json:"scope" gorm:"uniqueIndex;size:32"`
	Paused    bool       `json:"paused"`
	Reason    string     `json:"reason"`
	PausedBy  string     `json:"paused_by" gorm:"size:120"`
	PausedAt  *time.Time `json:"paused_at"`
	UpdatedAt time.Time  `json:"updated_at"`
}

// Character is a member of the fixed cast. The reference sheet and voice
// profile are deliberately stored once and reused by every episode so the
// cast looks and sounds the same across the whole series.
type Character struct {
	ID                uint      `json:"id" gorm:"primaryKey"`
	Name              string    `json:"name" gorm:"uniqueIndex;size:80"`
	Species           string    `json:"species" gorm:"size:80"`
	Role              string    `json:"role" gorm:"size:120"`
	ColorHex          string    `json:"color_hex" gorm:"size:16"`
	SignatureItem     string    `json:"signature_item" gorm:"size:120"`
	Teaches           string    `json:"teaches" gorm:"size:120"`
	VoiceProfileID    string    `json:"voice_profile_id" gorm:"size:120"`
	ReferenceSheetURL string    `json:"reference_sheet_url"`
	Active            bool      `json:"active" gorm:"default:true"`
	CreatedAt         time.Time `json:"created_at"`
	UpdatedAt         time.Time `json:"updated_at"`
}

// Location is a recurring set. Backgrounds are built once and reused.
type Location struct {
	ID                 uint      `json:"id" gorm:"primaryKey"`
	Name               string    `json:"name" gorm:"uniqueIndex;size:80"`
	Description        string    `json:"description"`
	UsedFor            string    `json:"used_for" gorm:"size:120"`
	BackgroundAssetURL string    `json:"background_asset_url"`
	CreatedAt          time.Time `json:"created_at"`
	UpdatedAt          time.Time `json:"updated_at"`
}

// CurriculumTopic is a planned teaching subject waiting in the backlog for
// the content idea agent to pick up.
type CurriculumTopic struct {
	ID              uint      `json:"id" gorm:"primaryKey"`
	Theme           string    `json:"theme" gorm:"index;size:32"`
	Title           string    `json:"title" gorm:"size:200"`
	LearningGoal    string    `json:"learning_goal"`
	WeekInCycle     int       `json:"week_in_cycle"`
	DifficultyLevel int       `json:"difficulty_level" gorm:"default:1"`
	Used            bool      `json:"used" gorm:"index;default:false"`
	CreatedAt       time.Time `json:"created_at"`
	UpdatedAt       time.Time `json:"updated_at"`
}

// Asset is a file produced for an episode by one of the generation agents.
type Asset struct {
	ID              uint      `json:"id" gorm:"primaryKey"`
	EpisodeID       uint      `json:"episode_id" gorm:"index"`
	Kind            AssetKind `json:"kind" gorm:"index;size:32"`
	Platform        string    `json:"platform" gorm:"size:32"`
	URI             string    `json:"uri"`
	GeneratedBy     string    `json:"generated_by" gorm:"size:80"`
	DurationSeconds int       `json:"duration_seconds"`
	CreatedAt       time.Time `json:"created_at"`
}

// ApprovalLog records a human decision at one of the two mandatory gates.
type ApprovalLog struct {
	ID         uint          `json:"id" gorm:"primaryKey"`
	EpisodeID  uint          `json:"episode_id" gorm:"index"`
	Gate       string        `json:"gate" gorm:"index;size:32"`
	Decision   string        `json:"decision" gorm:"size:16"`
	Reviewer   string        `json:"reviewer" gorm:"size:120"`
	Notes      string        `json:"notes"`
	FromStatus EpisodeStatus `json:"from_status" gorm:"size:32"`
	ToStatus   EpisodeStatus `json:"to_status" gorm:"size:32"`
	CreatedAt  time.Time     `json:"created_at"`
}

// EpisodeEvent is the audit trail of every stage change, whoever caused it.
type EpisodeEvent struct {
	ID         uint          `json:"id" gorm:"primaryKey"`
	EpisodeID  uint          `json:"episode_id" gorm:"index"`
	FromStatus EpisodeStatus `json:"from_status" gorm:"size:32"`
	ToStatus   EpisodeStatus `json:"to_status" gorm:"size:32"`
	Actor      string        `json:"actor" gorm:"size:120"`
	Note       string        `json:"note"`
	CreatedAt  time.Time     `json:"created_at"`
}

// TransitionRequest is the body an agent posts to move an episode forward.
// WorkerID must match the current claim when the episode is claimed.
type TransitionRequest struct {
	ToStatus EpisodeStatus `json:"to_status"`
	Actor    string        `json:"actor"`
	WorkerID string        `json:"worker_id"`
	Note     string        `json:"note"`
}

// ClaimRequest is the body a worker posts to take the next job at a stage.
type ClaimRequest struct {
	Status   EpisodeStatus `json:"status"`
	WorkerID string        `json:"worker_id"`
}

// WorkerRequest identifies the worker releasing or extending a claim.
type WorkerRequest struct {
	WorkerID string `json:"worker_id"`
}

// FailRequest is how a worker reports that it could not finish its job.
type FailRequest struct {
	WorkerID string `json:"worker_id"`
	Error    string `json:"error"`
}

// PauseRequest is the admin stopping production.
type PauseRequest struct {
	Scope  string `json:"scope"`
	Reason string `json:"reason"`
}

// StageCount is how many episodes sit at one stage.
type StageCount struct {
	Status EpisodeStatus `json:"status"`
	Count  int64         `json:"count"`
	Paused bool          `json:"paused"`
}

// PipelineOverview is the admin's single view of the whole operation: what is
// running, what is stuck, what is waiting on them, and what has broken.
type PipelineOverview struct {
	Paused         bool         `json:"paused"`
	PauseReason    string       `json:"pause_reason,omitempty"`
	PausedStages   []string     `json:"paused_stages"`
	Stages         []StageCount `json:"stages"`
	InFlight       int64        `json:"in_flight"`
	ClaimedNow     int64        `json:"claimed_now"`
	AwaitingReview int64        `json:"awaiting_review"`
	Failed         int64        `json:"failed"`
	PublishedTotal int64        `json:"published_total"`
	Stuck          []Episode    `json:"stuck"`
}

// ApprovalRequest is the body a human reviewer posts at a gate. The reviewer
// is not part of the body: it comes from the authenticated identity so a
// decision cannot be attributed to someone who did not make it.
type ApprovalRequest struct {
	Gate     string `json:"gate"`
	Decision string `json:"decision"`
	Notes    string `json:"notes"`
}

// StageInfo describes one pipeline stage for the discovery endpoint, so an
// agent can look up which stage it is responsible for.
type StageInfo struct {
	Status       EpisodeStatus   `json:"status"`
	Layer        string          `json:"layer"`
	OwnerAgent   string          `json:"owner_agent"`
	NextStatuses []EpisodeStatus `json:"next_statuses"`
	RequiresGate string          `json:"requires_gate,omitempty"`
}

// ErrorResponse is the standard error body for the pipeline endpoints.
type ErrorResponse struct {
	Error   string `json:"error"`
	Message string `json:"message,omitempty"`
}
