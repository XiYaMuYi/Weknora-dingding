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
	requestTarget := path
	if len(query) > 0 {
		encodedQuery := query.Encode()
		full += "?" + encodedQuery
		requestTarget += "?" + encodedQuery
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

	logger.Infof(ctx, "[DingTalk] %s %s", method, requestTarget)
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
		detail := formatDingTalkAPIErrorDetail(apiErr, raw)
		logger.Warnf(ctx, "[DingTalk] %s %s failed: status=%d body=%s", method, requestTarget, resp.StatusCode, truncate(string(raw), 500))
		if detail != "" {
			return fmt.Errorf("%s", humanizeDingTalkAPIError(path, detail, resp.StatusCode))
		}
		return fmt.Errorf("%s", humanizeDingTalkAPIError(path, truncate(string(raw), 200), resp.StatusCode))
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
	var all []spaceItem
	next := ""
	for {
		q := url.Values{}
		q.Set("unionId", c.cfg.OperatorUnionID)
		q.Set("spaceType", "org")
		q.Set("maxResults", "50")
		if next != "" {
			q.Set("nextToken", next)
		}
		var out listSpacesResponse
		if err := c.doAPI(ctx, http.MethodGet, "/v1.0/drive/spaces", q, nil, &out); err != nil {
			return nil, fmt.Errorf("list spaces: %w", err)
		}
		all = append(all, out.normalizedSpaces()...)
		if out.NextToken == "" {
			break
		}
		next = out.NextToken
	}
	return all, nil
}

func (c *Client) ListDentries(ctx context.Context, spaceID, parentID, cursor string) ([]dentryItem, string, error) {
	q := url.Values{}
	q.Set("unionId", c.cfg.OperatorUnionID)
	if parentID != "" {
		q.Set("parentId", parentID)
	} else {
		q.Set("parentId", "0")
	}
	if cursor != "" {
		q.Set("nextToken", cursor)
	}
	var out listDentriesResponse
	if err := c.doAPI(ctx, http.MethodGet, "/v1.0/storage/spaces/"+url.PathEscape(spaceID)+"/dentries", q, nil, &out); err != nil {
		return nil, "", err
	}
	return out.normalizedDentries(), out.normalizedNextToken(), nil
}

func (c *Client) ListWikiWorkspaces(ctx context.Context) ([]wikiWorkspaceItem, error) {
	var all []wikiWorkspaceItem
	next := ""
	for {
		q := url.Values{}
		q.Set("operatorId", c.cfg.OperatorUnionID)
		q.Set("maxResults", "50")
		if next != "" {
			q.Set("nextToken", next)
		}
		var out listWikiWorkspacesResponse
		if err := c.doAPI(ctx, http.MethodGet, "/v2.0/wiki/workspaces", q, nil, &out); err != nil {
			return nil, fmt.Errorf("list wiki workspaces: %w", err)
		}
		all = append(all, out.normalizedWorkspaces()...)
		if out.NextToken == "" {
			break
		}
		next = out.NextToken
	}
	return all, nil
}

func (c *Client) ListWikiNodes(ctx context.Context, parentNodeID, cursor string) ([]wikiNodeItem, string, error) {
	q := url.Values{}
	q.Set("operatorId", c.cfg.OperatorUnionID)
	q.Set("parentNodeId", parentNodeID)
	q.Set("maxResults", "50")
	if cursor != "" {
		q.Set("nextToken", cursor)
	}
	var out listWikiNodesResponse
	if err := c.doAPI(ctx, http.MethodGet, "/v2.0/wiki/nodes", q, nil, &out); err != nil {
		return nil, "", fmt.Errorf("list wiki nodes: %w", err)
	}
	return out.normalizedNodes(), out.NextToken, nil
}

func (c *Client) GetWikiNode(ctx context.Context, nodeID string) (*wikiNodeItem, error) {
	q := url.Values{}
	q.Set("operatorId", c.cfg.OperatorUnionID)
	var out struct {
		Node wikiNodeItem `json:"node"`
	}
	if err := c.doAPI(ctx, http.MethodGet, "/v2.0/wiki/nodes/"+url.PathEscape(nodeID), q, nil, &out); err != nil {
		return nil, err
	}
	if out.Node.NodeID == "" {
		out.Node.NodeID = nodeID
	}
	return &out.Node, nil
}

func (c *Client) ResolveDentryIDByUUID(ctx context.Context, dentryUUID string) (*dentryIDByUUIDResponse, error) {
	q := url.Values{}
	q.Set("operatorId", c.cfg.OperatorUnionID)
	path := "/v2.0/doc/dentries/" + url.PathEscape(dentryUUID) + "/queryDentryId"
	var out dentryIDByUUIDResponse
	if err := c.doAPI(ctx, http.MethodGet, path, q, nil, &out); err != nil {
		return nil, err
	}
	if out.SpaceID == "" || out.DentryID == "" {
		return nil, fmt.Errorf("钉钉未返回可下载文件的 spaceId/dentryId")
	}
	if out.DentryUUID == "" {
		out.DentryUUID = dentryUUID
	}
	return &out, nil
}

// GetDentry returns metadata for a single dentry (used to walk parent chain in the resource picker).
func (c *Client) GetDentry(ctx context.Context, spaceID, dentryID string) (*dentryItem, error) {
	path := fmt.Sprintf("/v1.0/storage/spaces/%s/dentries/%s",
		url.PathEscape(spaceID), url.PathEscape(dentryID))
	q := url.Values{}
	q.Set("unionId", c.cfg.OperatorUnionID)
	var out dentryItem
	if err := c.doAPI(ctx, http.MethodGet, path, q, nil, &out); err != nil {
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

	if isDingTalkNativeOnlineDocExtension(extension) {
		return c.downloadViaExportFallback(ctx, token, spaceID, dentryID, name, extension, nil)
	}

	// Try the current download-info API first, then GET URL with returned headers.
	path := fmt.Sprintf("/v1.0/storage/spaces/%s/dentries/%s/downloadInfos",
		url.PathEscape(spaceID), url.PathEscape(dentryID))
	data, err := c.downloadStorageFile(ctx, token, path+"/query", name, extension)
	if err == nil {
		return data, pickFileName(name, extension, "docx"), nil
	}

	// Backward compatibility for older/private deployments that still expose
	// /downloadInfos without the /query suffix.
	var legacyErr error
	if data, legacyErr = c.downloadStorageFile(ctx, token, path, name, extension); legacyErr == nil {
		return data, pickFileName(name, extension, "docx"), nil
	}

	return nil, "", fmt.Errorf("读取钉钉上传文件下载信息失败：%v（兼容旧接口也失败：%v）", err, legacyErr)
}

func (c *Client) downloadStorageFile(ctx context.Context, token, path, name, extension string) ([]byte, error) {
	q := url.Values{}
	q.Set("unionId", c.cfg.OperatorUnionID)
	reqBody := map[string]interface{}{
		"option": map[string]interface{}{
			"preferIntranet": false,
		},
	}
	var downloadResp downloadInfoResponse
	if err := c.doAPI(ctx, http.MethodPost, path, q, reqBody, &downloadResp); err != nil {
		return nil, err
	}
	downloadURL, headers := firstDownloadURLAndHeaders(downloadResp)
	if downloadURL == "" {
		return nil, fmt.Errorf("钉钉未返回文件下载地址")
	}

	if !c.isAllowedDownloadHost(downloadURL) {
		return nil, fmt.Errorf("download url host not allowed")
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, downloadURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("x-acs-dingtalk-access-token", token)
	for k, v := range headers {
		if strings.TrimSpace(k) != "" && strings.TrimSpace(v) != "" {
			req.Header.Set(k, v)
		}
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("下载钉钉文件失败：HTTP %d %s", resp.StatusCode, truncate(string(body), 200))
	}
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	return data, nil
}

func firstDownloadURLAndHeaders(resp downloadInfoResponse) (string, map[string]string) {
	if resp.DownloadURL != "" {
		return resp.DownloadURL, resp.Headers
	}
	if len(resp.ResourceURLs) > 0 {
		return resp.ResourceURLs[0], resp.Headers
	}
	if len(resp.HeaderSignatureInfo.ResourceURLs) > 0 {
		return resp.HeaderSignatureInfo.ResourceURLs[0], resp.HeaderSignatureInfo.Headers
	}
	if len(resp.HeaderSignatureInfo.InternalResourceURLs) > 0 {
		return resp.HeaderSignatureInfo.InternalResourceURLs[0], resp.HeaderSignatureInfo.Headers
	}
	return "", nil
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
	fileName := markdownFileName(name)
	return []byte(contentResp.Content), fileName, nil
}

func (c *Client) DownloadWikiDocContent(ctx context.Context, docKey, name, extension string) ([]byte, string, error) {
	q := url.Values{}
	q.Set("operatorId", c.cfg.OperatorUnionID)
	path := fmt.Sprintf("/v1.0/doc/suites/documents/%s/blocks", url.PathEscape(docKey))
	var out map[string]interface{}
	if err := c.doAPI(ctx, http.MethodGet, path, q, nil, &out); err != nil {
		return nil, "", err
	}
	content := strings.TrimSpace(extractDingTalkBlockText(out))
	if content == "" {
		return nil, "", fmt.Errorf("读取知识库文档正文失败：钉钉返回了文档结构，但没有可同步的正文内容，请确认该文档不是空文档或暂不支持的类型")
	}
	fileName := markdownFileName(name)
	return []byte(content), fileName, nil
}

func (c *Client) DownloadWikiSpreadsheetContent(ctx context.Context, workbookID, name string) ([]byte, string, error) {
	sheets, err := c.ListWorkbookSheets(ctx, workbookID)
	if err != nil {
		return nil, "", err
	}
	if len(sheets) == 0 {
		return nil, "", fmt.Errorf("读取知识库表格失败：钉钉返回的工作簿没有工作表")
	}

	var b strings.Builder
	title := strings.TrimSpace(name)
	if title == "" {
		title = "钉钉表格"
	}
	b.WriteString("# ")
	b.WriteString(title)
	b.WriteString("\n\n")

	hasContent := false
	for _, sheet := range sheets {
		sheetID := sheet.identifier()
		if sheetID == "" {
			continue
		}
		var rows [][]string
		for _, rangeAddress := range spreadsheetRangeAddresses(sheet.RowCount, sheet.ColumnCount) {
			rangeRows, err := c.GetWorkbookRange(ctx, workbookID, sheetID, rangeAddress)
			if err != nil {
				return nil, "", fmt.Errorf("读取知识库表格 %s 失败：%w", sheet.displayName(), err)
			}
			rows = append(rows, rangeRows...)
		}
		rows = trimEmptySpreadsheetRows(rows)
		if len(rows) == 0 {
			continue
		}
		hasContent = true
		b.WriteString("## ")
		b.WriteString(sheet.displayName())
		b.WriteString("\n\n")
		b.WriteString(markdownTable(rows))
		b.WriteString("\n\n")
	}

	if !hasContent {
		return nil, "", fmt.Errorf("读取知识库表格失败：表格没有可同步的单元格内容")
	}
	return []byte(strings.TrimSpace(b.String()) + "\n"), markdownFileName(name), nil
}

func (c *Client) ListWorkbookSheets(ctx context.Context, workbookID string) ([]workbookSheetItem, error) {
	q := url.Values{}
	q.Set("operatorId", c.cfg.OperatorUnionID)
	path := fmt.Sprintf("/v1.0/doc/workbooks/%s/sheets", url.PathEscape(workbookID))
	var out listWorkbookSheetsResponse
	if err := c.doAPI(ctx, http.MethodGet, path, q, nil, &out); err != nil {
		return nil, err
	}
	if len(out.Value) > 0 {
		return out.Value, nil
	}
	if len(out.Sheets) > 0 {
		return out.Sheets, nil
	}
	return out.Items, nil
}

func (c *Client) GetWorkbookRange(ctx context.Context, workbookID, sheetID, rangeAddress string) ([][]string, error) {
	q := url.Values{}
	q.Set("operatorId", c.cfg.OperatorUnionID)
	path := fmt.Sprintf("/v1.0/doc/workbooks/%s/sheets/%s/ranges/%s",
		url.PathEscape(workbookID), url.PathEscape(sheetID), url.PathEscape(rangeAddress))
	var out map[string]interface{}
	if err := c.doAPI(ctx, http.MethodGet, path, q, nil, &out); err != nil {
		return nil, err
	}
	return extractSpreadsheetRows(out), nil
}

func extractDingTalkBlockText(v interface{}) string {
	var parts []string
	var walk func(interface{}, string)
	walk = func(cur interface{}, key string) {
		switch val := cur.(type) {
		case map[string]interface{}:
			for k, child := range val {
				walk(child, k)
			}
		case []interface{}:
			for _, child := range val {
				walk(child, key)
			}
		case string:
			switch strings.ToLower(key) {
			case "text", "content", "plaintext", "plain_text":
				if s := strings.TrimSpace(val); s != "" {
					parts = append(parts, s)
				}
			}
		}
	}
	walk(v, "")
	return strings.Join(parts, "\n")
}

func (s workbookSheetItem) identifier() string {
	switch {
	case strings.TrimSpace(s.SheetID) != "":
		return strings.TrimSpace(s.SheetID)
	case strings.TrimSpace(s.ID) != "":
		return strings.TrimSpace(s.ID)
	case strings.TrimSpace(s.Name) != "":
		return strings.TrimSpace(s.Name)
	default:
		return strings.TrimSpace(s.Title)
	}
}

func (s workbookSheetItem) displayName() string {
	switch {
	case strings.TrimSpace(s.Name) != "":
		return strings.TrimSpace(s.Name)
	case strings.TrimSpace(s.Title) != "":
		return strings.TrimSpace(s.Title)
	case strings.TrimSpace(s.SheetID) != "":
		return strings.TrimSpace(s.SheetID)
	default:
		return "工作表"
	}
}

const dingtalkSpreadsheetMaxCellsPerRange = 30000

func spreadsheetRangeAddresses(rows, cols int) []string {
	if rows <= 0 || rows > 1000 {
		rows = 1000
	}
	if cols <= 0 || cols > 50 {
		cols = 50
	}
	rowsPerRange := dingtalkSpreadsheetMaxCellsPerRange / cols
	if rowsPerRange <= 0 {
		rowsPerRange = 1
	}
	ranges := make([]string, 0, (rows+rowsPerRange-1)/rowsPerRange)
	for start := 1; start <= rows; start += rowsPerRange {
		end := start + rowsPerRange - 1
		if end > rows {
			end = rows
		}
		ranges = append(ranges, fmt.Sprintf("A%d:%s%d", start, spreadsheetColumnName(cols), end))
	}
	return ranges
}

func spreadsheetColumnName(n int) string {
	if n <= 0 {
		return "A"
	}
	var out []byte
	for n > 0 {
		n--
		out = append([]byte{byte('A' + n%26)}, out...)
		n /= 26
	}
	return string(out)
}

func extractSpreadsheetRows(v interface{}) [][]string {
	rows := findSpreadsheetRows(v)
	if len(rows) == 0 {
		return nil
	}
	out := make([][]string, 0, len(rows))
	for _, row := range rows {
		cells := make([]string, 0, len(row))
		for _, cell := range row {
			cells = append(cells, spreadsheetCellString(cell))
		}
		out = append(out, cells)
	}
	return out
}

func findSpreadsheetRows(v interface{}) [][]interface{} {
	switch val := v.(type) {
	case map[string]interface{}:
		for _, key := range []string{"displayValues", "values", "formattedValues"} {
			if rows := rowsFromInterface(val[key]); len(rows) > 0 {
				return rows
			}
		}
		for _, child := range val {
			if rows := findSpreadsheetRows(child); len(rows) > 0 {
				return rows
			}
		}
	case []interface{}:
		if rows := rowsFromInterface(val); len(rows) > 0 {
			return rows
		}
		for _, child := range val {
			if rows := findSpreadsheetRows(child); len(rows) > 0 {
				return rows
			}
		}
	}
	return nil
}

func rowsFromInterface(v interface{}) [][]interface{} {
	rawRows, ok := v.([]interface{})
	if !ok || len(rawRows) == 0 {
		return nil
	}
	rows := make([][]interface{}, 0, len(rawRows))
	for _, rawRow := range rawRows {
		row, ok := rawRow.([]interface{})
		if !ok {
			return nil
		}
		rows = append(rows, row)
	}
	return rows
}

func spreadsheetCellString(v interface{}) string {
	switch val := v.(type) {
	case nil:
		return ""
	case string:
		return strings.TrimSpace(val)
	case float64:
		return strings.TrimRight(strings.TrimRight(fmt.Sprintf("%.10f", val), "0"), ".")
	case bool:
		if val {
			return "TRUE"
		}
		return "FALSE"
	case map[string]interface{}:
		for _, key := range []string{"displayValue", "formattedValue", "text", "value"} {
			if s := spreadsheetCellString(val[key]); s != "" {
				return s
			}
		}
	}
	return strings.TrimSpace(fmt.Sprint(v))
}

func trimEmptySpreadsheetRows(rows [][]string) [][]string {
	out := rows[:0]
	for _, row := range rows {
		for len(row) > 0 && strings.TrimSpace(row[len(row)-1]) == "" {
			row = row[:len(row)-1]
		}
		empty := true
		for _, cell := range row {
			if strings.TrimSpace(cell) != "" {
				empty = false
				break
			}
		}
		if !empty {
			out = append(out, row)
		}
	}
	return out
}

func markdownTable(rows [][]string) string {
	if len(rows) == 0 {
		return ""
	}
	width := 0
	for _, row := range rows {
		if len(row) > width {
			width = len(row)
		}
	}
	if width == 0 {
		return ""
	}
	normalized := make([][]string, len(rows))
	for i, row := range rows {
		normalized[i] = make([]string, width)
		copy(normalized[i], row)
	}
	var b strings.Builder
	writeMarkdownRow(&b, normalized[0])
	separator := make([]string, width)
	for i := range separator {
		separator[i] = "---"
	}
	writeMarkdownRow(&b, separator)
	for _, row := range normalized[1:] {
		writeMarkdownRow(&b, row)
	}
	return strings.TrimRight(b.String(), "\n")
}

func writeMarkdownRow(b *strings.Builder, row []string) {
	b.WriteString("|")
	for _, cell := range row {
		b.WriteString(" ")
		b.WriteString(escapeMarkdownTableCell(cell))
		b.WriteString(" |")
	}
	b.WriteString("\n")
}

func escapeMarkdownTableCell(s string) string {
	s = strings.ReplaceAll(s, "\n", "<br>")
	s = strings.ReplaceAll(s, "|", "\\|")
	return s
}

func humanizeDingTalkAPIError(path, msg string, status int) string {
	action := "调用钉钉接口失败"
	switch {
	case strings.Contains(path, "/oauth2/accessToken"):
		action = "获取钉钉访问令牌失败"
	case strings.Contains(path, "/drive/spaces"):
		action = "读取钉钉云盘空间失败"
	case strings.Contains(path, "/storage/spaces") && strings.Contains(path, "/dentries"):
		action = "读取钉钉云盘文件失败"
	case strings.Contains(path, "/wiki/workspaces"):
		action = "读取钉钉知识库列表失败"
	case strings.Contains(path, "/wiki/nodes"):
		action = "读取钉钉知识库目录失败"
	case strings.Contains(path, "/doc/dentries") && strings.Contains(path, "/queryDentryId"):
		action = "解析钉钉知识库上传文件下载标识失败"
	case strings.Contains(path, "/doc/suites/documents") && strings.Contains(path, "/blocks"):
		action = "读取钉钉知识库文档正文失败"
	case strings.Contains(path, "/doc/workbooks") && strings.Contains(path, "/sheets") && strings.Contains(path, "/ranges"):
		action = "读取钉钉知识库表格内容失败"
	case strings.Contains(path, "/doc/workbooks") && strings.Contains(path, "/sheets"):
		action = "读取钉钉知识库表格工作表失败"
	}

	hint := strings.TrimSpace(msg)
	switch status {
	case http.StatusUnauthorized:
		hint = "AppKey 或 AppSecret 无效，或应用访问令牌已失效"
	case http.StatusForbidden:
		if strings.Contains(path, "/doc/dentries") && strings.Contains(path, "/queryDentryId") {
			hint = withDingTalkOriginalDetail("当前应用缺少知识库文档读取权限，或操作人无权访问该文件。请在钉钉开放平台开通 Document.WorkspaceDocument.Read，并确认该成员能访问目标知识库文件", msg)
		} else if strings.Contains(path, "/downloadInfos") {
			hint = withDingTalkOriginalDetail("当前应用缺少企业存储文件下载信息读权限，或操作人无权下载该文件。请在钉钉开放平台开通 Storage.DownloadInfo.Read，并确认该成员能访问目标知识库文件", msg)
		} else if strings.Contains(path, "/doc/workbooks") {
			hint = withDingTalkOriginalDetail("当前应用或操作人 UnionID 没有读取钉钉表格的权限。请在钉钉开放平台为应用开通钉钉表格/工作簿读取权限，并确认该成员能访问这个知识库表格", msg)
		} else {
			hint = withDingTalkOriginalDetail("当前应用或操作人 UnionID 没有访问权限，请在钉钉开放平台开通对应的云盘、知识库和文档读取权限，并确认该成员能访问目标内容", msg)
		}
	case http.StatusNotFound:
		hint = "钉钉没有找到这个接口或资源。请确认选择的类型正确，并且知识库 ID 使用 workspaceId，云盘 ID 使用 spaceId"
	case http.StatusBadRequest:
		if hint == "" {
			hint = "请求参数不完整或格式不正确，请检查 AppKey、AppSecret、UnionID 和空间/知识库 ID"
		}
		if strings.Contains(path, "/doc/workbooks") && strings.Contains(strings.ToLower(hint), "doc key") {
			hint = "钉钉认为这个节点不是可读取的表格，或应用缺少表格读取权限。请确认同步范围里选择的是钉钉表格节点，并在开放平台开通钉钉表格/知识库读取权限"
		}
	}
	if hint == "" {
		hint = fmt.Sprintf("钉钉返回 HTTP %d", status)
	}
	return fmt.Sprintf("%s：%s（接口：%s，HTTP %d）", action, hint, path, status)
}

func formatDingTalkAPIErrorDetail(apiErr apiErrorBody, raw []byte) string {
	code := strings.TrimSpace(apiErr.Code)
	message := strings.TrimSpace(apiErr.Message)
	switch {
	case code != "" && message != "":
		return fmt.Sprintf("code=%s message=%s", code, message)
	case code != "":
		return "code=" + code
	case message != "":
		return message
	default:
		return truncate(string(raw), 200)
	}
}

func withDingTalkOriginalDetail(hint, detail string) string {
	detail = strings.TrimSpace(detail)
	if detail == "" || strings.Contains(hint, detail) {
		return hint
	}
	return hint + "。钉钉原始返回：" + detail
}

func (r listSpacesResponse) normalizedSpaces() []spaceItem {
	if len(r.Spaces) > 0 {
		return r.Spaces
	}
	return r.Items
}

func (r listDentriesResponse) normalizedDentries() []dentryItem {
	dentries := r.Dentries
	if len(dentries) == 0 {
		dentries = r.Items
	}
	for i := range dentries {
		if dentries[i].ID == "" {
			switch {
			case dentries[i].DentryID != "":
				dentries[i].ID = dentries[i].DentryID
			case dentries[i].DentryUUID != "":
				dentries[i].ID = dentries[i].DentryUUID
			}
		}
	}
	return dentries
}

func (r listDentriesResponse) normalizedNextToken() string {
	if r.NextToken != "" {
		return r.NextToken
	}
	return r.NextCursor
}

func (r listWikiWorkspacesResponse) normalizedWorkspaces() []wikiWorkspaceItem {
	if len(r.Workspaces) > 0 {
		return r.Workspaces
	}
	return r.Items
}

func (r listWikiNodesResponse) normalizedNodes() []wikiNodeItem {
	if len(r.Nodes) > 0 {
		return r.Nodes
	}
	return r.Items
}

func (c *Client) isAllowedDownloadHost(rawURL string) bool {
	u, err := url.Parse(rawURL)
	if err != nil || u.Host == "" {
		return false
	}
	host := strings.ToLower(u.Host)
	if base, err := url.Parse(c.cfg.GetAPIBase()); err == nil && strings.EqualFold(u.Host, base.Host) {
		return true
	}
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
