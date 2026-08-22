package guard

import "time"

type Plan struct {
	ReleaseID           string            `json:"release_id"`
	Version             string            `json:"version"`
	CandidateFile       string            `json:"candidate_file,omitempty"`
	Repository          string            `json:"repository,omitempty"`
	BaseRef             string            `json:"base_ref,omitempty"`
	TargetRef           string            `json:"target_ref,omitempty"`
	ExpectedFiles       []string          `json:"expected_files,omitempty"`
	Checks              []Check           `json:"checks"`
	DriftFile           string            `json:"drift_file,omitempty"`
	ExpectedDrifts      []string          `json:"expected_drifts,omitempty"`
	ExpectedChangesFile string            `json:"expected_changes_file,omitempty"`
	Fleet               *FleetPolicy      `json:"fleet,omitempty"`
	Observation         *ObservationPlan  `json:"observation,omitempty"`
	Metrics             []MetricPolicy    `json:"metrics,omitempty"`
	RecoveryChecks      []Check           `json:"recovery_checks"`
	Rollback            []string          `json:"rollback"`
	Metadata            map[string]string `json:"metadata,omitempty"`
}

type FleetPolicy struct {
	CenterURL        string            `json:"center_url"`
	TokenEnv         string            `json:"token_env,omitempty"`
	Nodes            []string          `json:"nodes"`
	VersionLabel     string            `json:"version_label,omitempty"`
	ExpectedVersions map[string]string `json:"expected_versions,omitempty"`
	MaxAgeSeconds    int               `json:"max_age_seconds,omitempty"`
}

type ObservationPlan struct {
	Seconds           int `json:"seconds"`
	PollSeconds       int `json:"poll_seconds,omitempty"`
	MaxCriticalAlerts int `json:"max_critical_alerts,omitempty"`
}

type MetricPolicy struct {
	Name                 string            `json:"name"`
	Metric               string            `json:"metric"`
	Node                 string            `json:"node,omitempty"`
	Source               string            `json:"source,omitempty"`
	Labels               map[string]string `json:"labels,omitempty"`
	Aggregate            string            `json:"aggregate,omitempty"`
	SeriesReduce         string            `json:"series_reduce,omitempty"`
	BaselineSeconds      int               `json:"baseline_seconds,omitempty"`
	Direction            string            `json:"direction,omitempty"`
	MaxRegressionPercent *float64          `json:"max_regression_percent,omitempty"`
	MaxValue             *float64          `json:"max_value,omitempty"`
	MinValue             *float64          `json:"min_value,omitempty"`
	Required             *bool             `json:"required,omitempty"`
}

type Check struct {
	Name              string            `json:"name"`
	Type              string            `json:"type"`
	Command           string            `json:"command,omitempty"`
	URL               string            `json:"url,omitempty"`
	Method            string            `json:"method,omitempty"`
	Headers           map[string]string `json:"headers,omitempty"`
	Path              string            `json:"path,omitempty"`
	Ref               string            `json:"ref,omitempty"`
	BeforePath        string            `json:"before_path,omitempty"`
	AfterPath         string            `json:"after_path,omitempty"`
	AllowedChanges    []string          `json:"allowed_changes,omitempty"`
	RequiredKeys      []string          `json:"required_keys,omitempty"`
	WantStatus        int               `json:"want_status,omitempty"`
	Contains          string            `json:"contains,omitempty"`
	SHA256            string            `json:"sha256,omitempty"`
	JSONPath          string            `json:"json_path,omitempty"`
	Equals            any               `json:"equals,omitempty"`
	Driver            string            `json:"driver,omitempty"`
	DSNEnv            string            `json:"dsn_env,omitempty"`
	Query             string            `json:"query,omitempty"`
	IncludeSQLPreview bool              `json:"include_sql_preview,omitempty"`
	TimeoutSecs       int               `json:"timeout_seconds,omitempty"`
	WorkingDir        string            `json:"working_directory,omitempty"`
	Required          *bool             `json:"required,omitempty"`
}

type Result struct {
	Name       string         `json:"name"`
	Type       string         `json:"type"`
	Phase      string         `json:"phase"`
	Status     string         `json:"status"`
	Required   bool           `json:"required"`
	Summary    string         `json:"summary"`
	DurationMS int64          `json:"duration_ms"`
	Evidence   map[string]any `json:"evidence,omitempty"`
}

type GitManifest struct {
	Repository       string   `json:"repository"`
	BaseRef          string   `json:"base_ref"`
	TargetRef        string   `json:"target_ref"`
	BaseCommit       string   `json:"base_commit"`
	TargetCommit     string   `json:"target_commit"`
	Commits          []string `json:"commits"`
	ChangedFiles     []string `json:"changed_files"`
	MigrationFiles   []string `json:"migration_files"`
	Configuration    []string `json:"configuration_files"`
	UnexpectedFiles  []string `json:"unexpected_sensitive_files"`
	WorkingTreeClean bool     `json:"working_tree_clean"`
	DirtyFiles       []string `json:"dirty_files,omitempty"`
}

type CandidateSummary struct {
	Spec                 string                   `json:"spec"`
	RequirementID        string                   `json:"requirement_id,omitempty"`
	RequirementTitle     string                   `json:"requirement_title,omitempty"`
	CriteriaTotal        int                      `json:"criteria_total"`
	CriteriaSatisfied    int                      `json:"criteria_satisfied"`
	CriteriaWithEvidence int                      `json:"criteria_with_evidence"`
	TasksTotal           int                      `json:"tasks_total"`
	TasksDone            int                      `json:"tasks_done"`
	SourcesTotal         int                      `json:"sources_total"`
	SourcesClean         int                      `json:"sources_clean"`
	Sources              []CandidateSourceSummary `json:"sources,omitempty"`
	Ready                bool                     `json:"ready"`
}

type CandidateSourceSummary struct {
	TaskID         string `json:"task_id"`
	RepositoryPath string `json:"repository_path"`
	Branch         string `json:"branch"`
	HeadCommit     string `json:"head_commit"`
	Clean          bool   `json:"clean"`
}

type FleetEvidence struct {
	CheckedAt      string         `json:"checked_at"`
	Nodes          []NodeEvidence `json:"nodes"`
	CriticalAlerts int            `json:"critical_alerts"`
}

type MetricWindow struct {
	StartMS int64   `json:"start_ms"`
	EndMS   int64   `json:"end_ms"`
	Samples int     `json:"samples"`
	Series  int     `json:"series"`
	Value   float64 `json:"value"`
	Minimum float64 `json:"minimum"`
	Maximum float64 `json:"maximum"`
	Last    float64 `json:"last"`
}

type MetricEvidence struct {
	Name              string        `json:"name"`
	Metric            string        `json:"metric"`
	Node              string        `json:"node,omitempty"`
	Aggregate         string        `json:"aggregate"`
	SeriesReduce      string        `json:"series_reduce"`
	Direction         string        `json:"direction"`
	Before            *MetricWindow `json:"before,omitempty"`
	After             *MetricWindow `json:"after,omitempty"`
	RegressionPercent float64       `json:"regression_percent,omitempty"`
	Pass              bool          `json:"pass"`
	Summary           string        `json:"summary"`
}

type ExpectedChangeDocument struct {
	Spec        string            `json:"spec"`
	Kind        string            `json:"kind"`
	ReleaseID   string            `json:"release_id"`
	Version     string            `json:"version"`
	GeneratedAt string            `json:"generated_at"`
	Changes     []ExpectedChange  `json:"changes"`
	Metadata    map[string]string `json:"metadata,omitempty"`
}

type ExpectedChange struct {
	ID                 string   `json:"id"`
	Source             string   `json:"source"`
	Action             string   `json:"action"`
	ResourceID         string   `json:"resource_id"`
	ResourceType       string   `json:"resource_type,omitempty"`
	NodeID             string   `json:"node_id,omitempty"`
	Fields             []string `json:"fields,omitempty"`
	Fingerprint        string   `json:"fingerprint,omitempty"`
	Summary            string   `json:"summary"`
	EvidenceIDs        []string `json:"evidence_ids,omitempty"`
	VerificationChecks []string `json:"verification_checks,omitempty"`
	MetricPolicies     []string `json:"metric_policies,omitempty"`
	AffectedNodes      []string `json:"affected_nodes,omitempty"`
	Required           *bool    `json:"required,omitempty"`
}

type ObservedChange struct {
	ID             string   `json:"id"`
	Source         string   `json:"source"`
	Action         string   `json:"action"`
	ResourceID     string   `json:"resource_id"`
	ResourceType   string   `json:"resource_type,omitempty"`
	NodeID         string   `json:"node_id,omitempty"`
	Fields         []string `json:"fields,omitempty"`
	Fingerprint    string   `json:"fingerprint,omitempty"`
	Severity       string   `json:"severity,omitempty"`
	Summary        string   `json:"summary,omitempty"`
	Classification string   `json:"classification,omitempty"`
	ReleaseID      string   `json:"release_id,omitempty"`
}

type ChangeSourceEvidence struct {
	Source         string `json:"source"`
	ArtifactSHA256 string `json:"artifact_sha256,omitempty"`
	CheckedAt      string `json:"checked_at"`
	Items          int    `json:"items"`
}

type ChangeCorrelation struct {
	ExpectedID  string   `json:"expected_id"`
	Status      string   `json:"status"`
	Required    bool     `json:"required"`
	ObservedID  string   `json:"observed_id,omitempty"`
	EvidenceIDs []string `json:"evidence_ids,omitempty"`
	Reasons     []string `json:"reasons,omitempty"`
}

type ChangeCoverage struct {
	Spec            string                 `json:"spec"`
	DocumentSHA256  string                 `json:"document_sha256"`
	Declared        []ExpectedChange       `json:"declared"`
	Observed        []ObservedChange       `json:"observed"`
	Sources         []ChangeSourceEvidence `json:"sources"`
	Correlations    []ChangeCorrelation    `json:"correlations"`
	Unexpected      []ObservedChange       `json:"unexpected"`
	ExpectedTotal   int                    `json:"expected_total"`
	MatchedTotal    int                    `json:"matched_total"`
	MissingRequired int                    `json:"missing_required"`
	MissingOptional int                    `json:"missing_optional"`
	UnexpectedTotal int                    `json:"unexpected_total"`
}

type Guidance struct {
	Code       string   `json:"code"`
	Priority   string   `json:"priority"`
	Title      string   `json:"title"`
	Summary    string   `json:"summary"`
	RelatedIDs []string `json:"related_ids,omitempty"`
}
type NodeEvidence struct {
	NodeID          string `json:"node_id"`
	Health          string `json:"health"`
	ObservedAt      string `json:"observed_at"`
	ActualVersion   string `json:"actual_version"`
	ExpectedVersion string `json:"expected_version"`
	Match           bool   `json:"match"`
}

type Manifest struct {
	ReleaseID   string            `json:"release_id"`
	Version     string            `json:"version"`
	CreatedAt   time.Time         `json:"created_at"`
	Metadata    map[string]string `json:"metadata,omitempty"`
	Candidate   *CandidateSummary `json:"candidate,omitempty"`
	Git         *GitManifest      `json:"git,omitempty"`
	FleetBefore *FleetEvidence    `json:"fleet_before,omitempty"`
	FleetAfter  *FleetEvidence    `json:"fleet_after,omitempty"`
	Metrics     []MetricEvidence  `json:"metrics,omitempty"`
	Changes     *ChangeCoverage   `json:"changes,omitempty"`
}

type Report struct {
	SchemaVersion  string            `json:"schema_version"`
	ReleaseID      string            `json:"release_id"`
	Version        string            `json:"version"`
	Decision       string            `json:"decision"`
	DecisionReason string            `json:"decision_reason"`
	GeneratedAt    time.Time         `json:"generated_at"`
	PlanSHA256     string            `json:"plan_sha256"`
	Manifest       Manifest          `json:"manifest"`
	Results        []Result          `json:"results"`
	Rollback       []string          `json:"rollback"`
	Observation    *ObservationState `json:"observation,omitempty"`
	Guidance       []Guidance        `json:"guidance,omitempty"`
}

type ObservationState struct {
	Status     string          `json:"status"`
	StartedAt  time.Time       `json:"started_at"`
	DeadlineAt time.Time       `json:"deadline_at"`
	Samples    []FleetEvidence `json:"samples"`
}

type Approval struct {
	SchemaVersion string    `json:"schema_version"`
	ReleaseID     string    `json:"release_id"`
	ReportSHA256  string    `json:"report_sha256"`
	Decision      string    `json:"decision"`
	ApprovedBy    string    `json:"approved_by"`
	ApprovedAt    time.Time `json:"approved_at"`
	Note          string    `json:"note,omitempty"`
}
