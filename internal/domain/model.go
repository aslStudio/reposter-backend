package domain

import "time"

type Platform string

const (
	Telegram Platform = "telegram"
	X        Platform = "x"
	Threads  Platform = "threads"
)

type PublishMode string

const (
	ManualApproval PublishMode = "manual_approval"
	Automatic      PublishMode = "automatic"
)

type DraftStatus string

const (
	PendingApproval DraftStatus = "pending_approval"
	ReadyToPublish  DraftStatus = "ready_to_publish"
	Publishing      DraftStatus = "publishing"
	Published       DraftStatus = "published"
	PublishFailed   DraftStatus = "publish_failed"
)

// Source is a public account or channel monitored for new posts.
type Source struct {
	ID       string
	Platform Platform
	URL      string
}

// Destination belongs to a customer and is connected for publishing.
type Destination struct {
	ID             string
	CustomerID     string
	Platform       Platform
	ExternalID     string
	StylePrompt    string
	Language       string
	Mode           PublishMode
	DailyPostLimit int
}

// Route connects one source to one destination. A source may serve many routes.
type Route struct {
	ID            string
	SourceID      string
	DestinationID string
}

type SourcePost struct {
	ID          string
	SourceID    string
	URL         string
	Text        string
	MediaURLs   []string
	PublishedAt time.Time
}

type Draft struct {
	ID             string
	RouteID        string
	SourcePostID   string
	DestinationID  string
	Text           string
	MediaURLs      []string
	Status         DraftStatus
	CreatedAt      time.Time
	PublishedAt    *time.Time
	ExternalPostID string
}

// PlanLimits are enforced per customer by the application layer.
type PlanLimits struct {
	MaxDestinations              int
	MaxSourcesPerDestination     int
	MaxPostsPerDestinationPerDay int
}
