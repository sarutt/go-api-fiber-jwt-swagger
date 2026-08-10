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
)

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
	CreatedAt      time.Time  `json:"created_at"`
	UpdatedAt      time.Time  `json:"updated_at"`
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
type TransitionRequest struct {
	ToStatus EpisodeStatus `json:"to_status"`
	Actor    string        `json:"actor"`
	Note     string        `json:"note"`
}

// ApprovalRequest is the body a human reviewer posts at a gate.
type ApprovalRequest struct {
	Gate     string `json:"gate"`
	Decision string `json:"decision"`
	Reviewer string `json:"reviewer"`
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
