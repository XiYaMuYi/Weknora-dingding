package dingtalk

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/Tencent/WeKnora/internal/logger"
)

// Client calls DingTalk Open APIs for document storage.
type Client struct {
	cfg        *Config
	httpClient *http.Client

	tokenMu    sync.Mutex
	tokenCache string
	tokenExpAt time.Time
}

func NewClient(cfg *Config) *Client {
	return &Client{
		cfg:        cfg,
		httpClient: &http.Client{Timeout: 45 * time.Second},
	}
}

func (c *Client) Ping(ctx context.Context) error {
	_, err := c.getAccessToken(ctx)
	return err
}

func (c *Client) getAccessToken(ctx context.Context) (string, error) {
	c.tokenMu.Lock()
	defer c.tokenMu.Unlock()

	if c.tokenCache != "" && time.Now().Before(c.tokenExpAt) {
		return c.tokenCache, nil
	}

	// Prefer api.dingtalk.com OAuth2 client_credentials when available.
	token, ttl, err := c.fetchOAuthToken(ctx)
	if err != nil {
		logger.Warnf(ctx, "[DingTalk] oauth token failed, trying legacy gettoken: %v", err)
		token, ttl, err = c.fetchLegacyToken(ctx)
		if err != nil {
			return "", err
		}
	}

	c.tokenCache = token
	if ttl > 5*time.Minute {
		ttl -= 5 * time.Minute
	} else if ttl <= 0 {
		ttl = 30 * time.Minute
	}
	c.tokenExpAt = time.Now().Add(ttl)
	return c.tokenCache, nil
}

func (c *Client) fetchOAuthToken(ctx context.Context) (string, time.Duration, error) {
	u := c.cfg.GetAPIBase() + "/v1.0/oauth2/accessToken"
	body, _ := json.Marshal(map[string]string{
		"appKey":    c.cfg.AppKey,
		"appSecret": c.cfg.AppSecret,
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, bytes.NewReader(body))
	if err != nil {
		return "", 0, err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", 0, fmt.Errorf("oauth token request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusBadRequest {
		return "", 0, fmt.Errorf("oauth endpoint unavailable: status=%d", resp.StatusCode)
	}

	var out oauthTokenResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", 0, fmt.Errorf("decode oauth token: %w", err)
	}
	if out.AccessToken == "" {
		return "", 0, fmt.Errorf("empty oauth access token")
	}
	sec := out.ExpireIn
	if sec <= 0 {
		sec = 7200
	}
	return out.AccessToken, time.Duration(sec) * time.Second, nil
}

func (c *Client) fetchLegacyToken(ctx context.Context) (string, time.Duration, error) {
	q := url.Values{}
	q.Set("appkey", c.cfg.AppKey)
	q.Set("appsecret", c.cfg.AppSecret)
	u := c.cfg.GetLegacyBase() + "/gettoken?" + q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return "", 0, err
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", 0, fmt.Errorf("legacy gettoken: %w", err)
	}
	defer resp.Body.Close()

	var out legacyTokenResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", 0, fmt.Errorf("decode legacy token: %w", err)
	}
	if out.ErrCode != 0 {
		return "", 0, fmt.Errorf("dingtalk gettoken errcode=%d errmsg=%s", out.ErrCode, out.ErrMsg)
	}
	sec := out.ExpiresIn
	if sec <= 0 {
		sec = 7200
	}
	return out.AccessToken, time.Duration(sec) * time.Second, nil
}

func (c *Client) doAPI(ctx context.Context, method, path string, query url.Values, body interface{}, result interface{}) error {
	token, err := c.getAccessToken(ctx)
	if err != nil {
		return err
	}

	full := c.cfg.GetAPIBase() + path
	if len(query) > 0 {
		full += "?" + query.Encode()
	}

	var bodyReader io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		bodyReader = bytes.NewReader(b)
	}

	req, err := http.NewRequestWithContext(ctx, method, full, bodyReader)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-acs-dingtalk-access-token", token)

	logger.Infof(ctx, "[DingTalk] %s %s", method, path)
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("request: %w", err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if resp.StatusCode >= 400 {
		var apiErr apiErrorBody
		_ = json.Unmarshal(raw, &apiErr)
		if apiErr.Message != "" {
			return fmt.Errorf("dingtalk api %s: %s (http %d)", path, apiErr.Message, resp.StatusCode)
		}
		return fmt.Errorf("dingtalk api %s: http %d body=%s", path, resp.StatusCode, truncate(string(raw), 200))
	}
	if result == nil {
		return nil
	}
	if err := json.Unmarshal(raw, result); err != nil {
		return fmt.Errorf("decode response: %w", err)
	}
	return nil
}

func (c *Client) ListSpaces(ctx context.Context) ([]spaceItem, error) {
	var out listSpacesResponse
	// Union ID may be required on some tenants; empty body lists spaces visible to the app.
	if err := c.doAPI(ctx, http.MethodGet, "/v1.0/storage/spaces", nil, nil, &out); err != nil {
		return nil, fmt.Errorf("list spaces: %w", err)
	}
	return out.Spaces, nil
}

func (c *Client) ListDentries(ctx context.Context, spaceID, parentID, cursor string) ([]dentryItem, string, error) {
	q := url.Values{}
	q.Set("spaceId", spaceID)
	if parentID != "" {
		q.Set("parentId", parentID)
	}
	if cursor != "" {
		q.Set("nextCursor", cursor)
	}
	var out listDentriesResponse
	if err := c.doAPI(ctx, http.MethodGet, "/v1.0/storage/spaces/"+url.PathEscape(spaceID)+"/dentries", q, nil, &out); err != nil {
		return nil, "", err
	}
	return out.Dentries, out.NextCursor, nil
}

// GetDentry returns metadata for a single dentry (used to walk parent chain in the resource picker).
func (c *Client) GetDentry(ctx context.Context, spaceID, dentryID string) (*dentryItem, error) {
	path := fmt.Sprintf("/v1.0/storage/spaces/%s/dentries/%s",
		url.PathEscape(spaceID), url.PathEscape(dentryID))
	var out dentryItem
	if err := c.doAPI(ctx, http.MethodGet, path, nil, nil, &out); err != nil {
		return nil, err
	}
	if out.ID == "" {
		out.ID = dentryID
	}
	if out.SpaceID == "" {
		out.SpaceID = spaceID
	}
	return &out, nil
}

// DownloadDocContent exports or downloads document bytes for an online doc dentry.
// Implementation uses storage download when available; tests can stub via httptest.
func (c *Client) DownloadDocContent(ctx context.Context, spaceID, dentryID, name, extension string) ([]byte, string, error) {
	token, err := c.getAccessToken(ctx)
	if err != nil {
		return nil, "", err
	}

	// Try POST download info then GET URL — pattern used by DingTalk storage APIs.
	path := fmt.Sprintf("/v1.0/storage/spaces/%s/dentries/%s/downloadInfos",
		url.PathEscape(spaceID), url.PathEscape(dentryID))
	reqBody := map[string]interface{}{}
	var downloadResp struct {
		DownloadURL string `json:"downloadUrl"`
	}
	if err := c.doAPI(ctx, http.MethodPost, path, nil, reqBody, &downloadResp); err != nil {
		return c.downloadViaExportFallback(ctx, token, spaceID, dentryID, name, extension, err)
	}
	if downloadResp.DownloadURL == "" {
		return c.downloadViaExportFallback(ctx, token, spaceID, dentryID, name, extension, fmt.Errorf("empty downloadUrl"))
	}

	if !isAllowedDownloadHost(downloadResp.DownloadURL) {
		return nil, "", fmt.Errorf("download url host not allowed")
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, downloadResp.DownloadURL, nil)
	if err != nil {
		return nil, "", err
	}
	req.Header.Set("x-acs-dingtalk-access-token", token)
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, "", err
	}
	fileName := pickFileName(name, extension, "docx")
	return data, fileName, nil
}

func (c *Client) downloadViaExportFallback(ctx context.Context, token, spaceID, dentryID, name, extension string, prev error) ([]byte, string, error) {
	// Minimal fallback: raw content endpoint used in some DingTalk doc versions.
	path := fmt.Sprintf("/v1.0/doc/documents/%s/content", url.PathEscape(dentryID))
	var contentResp struct {
		Content string `json:"content"`
	}
	if err := c.doAPI(ctx, http.MethodGet, path, nil, nil, &contentResp); err != nil {
		return nil, "", fmt.Errorf("download doc: %w (also: %v)", err, prev)
	}
	if contentResp.Content == "" {
		return nil, "", fmt.Errorf("empty doc content: %v", prev)
	}
	fileName := pickFileName(name, extension, "md")
	return []byte(contentResp.Content), fileName, nil
}

func isAllowedDownloadHost(rawURL string) bool {
	u, err := url.Parse(rawURL)
	if err != nil || u.Host == "" {
		return false
	}
	host := strings.ToLower(u.Host)
	allowed := []string{"dingtalk.com", "aliyuncs.com", "alicdn.com"}
	for _, suffix := range allowed {
		if host == suffix || strings.HasSuffix(host, "."+suffix) {
			return true
		}
	}
	return false
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}