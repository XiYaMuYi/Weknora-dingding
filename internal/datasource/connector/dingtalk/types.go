// Package dingtalk implements the DingTalk (钉钉) data source connector for WeKnora.
//
// It syncs online documents (Doc) from DingTalk document storage into knowledge bases.
//
// DingTalk Open API references (verify against your tenant's console permissions):
//   - Access token: https://open.dingtalk.com/document/orgapp/obtain-orgapp-token
//   - Storage / document: https://open.dingtalk.com/document/orgapp/document-overview
package dingtalk

import "time"

// Config holds DingTalk connector credentials (enterprise internal app).
type Config struct {
	// AppKey is the application's AppKey (Client ID).
	AppKey string `json:"app_key"`
	// AppSecret is the application's AppSecret (Client Secret).
	AppSecret string `json:"app_secret"`
	// CorpID is the enterprise CorpId (optional for some token flows; recommended for validation).
	CorpID string `json:"corp_id,omitempty"`
	// BaseURL overrides the Open API host (for tests or regional endpoints).
	BaseURL string `json:"base_url,omitempty"`
	// LegacyBaseURL overrides oapi.dingtalk.com (gettoken fallback).
	LegacyBaseURL string `json:"legacy_base_url,omitempty"`
}

const (
	defaultAPIBase    = "https://api.dingtalk.com"
	defaultLegacyBase = "https://oapi.dingtalk.com"
)

func (c *Config) GetAPIBase() string {
	if c.BaseURL != "" {
		return c.BaseURL
	}
	return defaultAPIBase
}

func (c *Config) GetLegacyBase() string {
	if c.LegacyBaseURL != "" {
		return c.LegacyBaseURL
	}
	return defaultLegacyBase
}

// Settings from DataSourceConfig.Settings (non-secret).
type Settings struct {
	// IncludeSubfolders when true, Fetch* walks folder subtrees under selected resources.
	IncludeSubfolders bool `json:"include_subfolders"`
	// ExportFormat preferred export: "markdown" (when API supports) or "docx".
	ExportFormat string `json:"export_format"`
}

func parseSettings(m map[string]interface{}) Settings {
	var s Settings
	if m == nil {
		s.IncludeSubfolders = true
		return s
	}
	if v, ok := m["include_subfolders"].(bool); ok {
		s.IncludeSubfolders = v
	} else {
		s.IncludeSubfolders = true
	}
	if v, ok := m["export_format"].(string); ok {
		s.ExportFormat = v
	}
	return s
}

// --- API DTOs ---

type legacyTokenResponse struct {
	ErrCode     int    `json:"errcode"`
	ErrMsg      string `json:"errmsg"`
	AccessToken string `json:"access_token"`
	ExpiresIn   int    `json:"expires_in"`
}

type oauthTokenResponse struct {
	AccessToken string `json:"accessToken"`
	ExpireIn    int    `json:"expireIn"`
}

type apiErrorBody struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// spaceItem is a DingTalk document space (team/personal drive root).
type spaceItem struct {
	SpaceID   string `json:"spaceId"`
	SpaceName string `json:"spaceName"`
	SpaceType string `json:"spaceType"`
}

type listSpacesResponse struct {
	Spaces []spaceItem `json:"spaces"`
}

// dentryItem is a file or folder in a space.
type dentryItem struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Type       string `json:"type"` // FILE | FOLDER
	Extension  string `json:"extension"`
	Size       int64  `json:"size"`
	UpdatedTime string `json:"updatedTime"` // ISO8601 or millis string per API version
	ParentID   string `json:"parentId"`
	SpaceID    string `json:"spaceId"`
}

type listDentriesResponse struct {
	Dentries   []dentryItem `json:"dentries"`
	NextCursor string       `json:"nextCursor"`
}

// dingtalkCursor stores incremental sync state.
type dingtalkCursor struct {
	LastSyncTime time.Time            `json:"last_sync_time"`
	DocRevisions map[string]string    `json:"doc_revisions"` // external_id -> revision key
}