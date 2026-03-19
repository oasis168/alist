// Package baidu 提供百度网盘网页端接口封装，用于转存和分享功能。
// 使用 Cookie 认证（网页端），独立于 drivers/baidu_netdisk 的 OAuth 认证。
package baidu

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

const (
	baiduBaseURL = "https://pan.baidu.com"
	defaultUA    = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/114.0.0.0 Safari/537.36"
)

var (
	shareIDRegex  = regexp.MustCompile(`"shareid":(\d+?),"`)
	userIDRegex   = regexp.MustCompile(`"share_uk":"(\d+?)","`)
	fsIDRegex     = regexp.MustCompile(`"fs_id":(\d+?),"`)
	fileNameRegex = regexp.MustCompile(`"server_filename":"(.+?)","`)
	isDirRegex    = regexp.MustCompile(`"isdir":(\d+?),"`)
)

// Client 百度网盘网页端客户端
type Client struct {
	cookie   string
	bdstoken string
	hc       *http.Client
}

// NewClient 创建新客户端，cookie 为用户浏览器抓取的完整 Cookie 字符串
func NewClient(cookie string) *Client {
	return &Client{
		cookie: cookie,
		hc:     &http.Client{Timeout: 30 * time.Second},
	}
}

func (c *Client) get(rawURL string, params map[string]string) ([]byte, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil, err
	}
	q := u.Query()
	for k, v := range params {
		q.Set(k, v)
	}
	u.RawQuery = q.Encode()
	req, err := http.NewRequest(http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, err
	}
	c.setHeaders(req)
	res, err := c.hc.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	return io.ReadAll(res.Body)
}

func (c *Client) post(rawURL string, params map[string]string, data map[string]string) ([]byte, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil, err
	}
	q := u.Query()
	for k, v := range params {
		q.Set(k, v)
	}
	u.RawQuery = q.Encode()
	form := url.Values{}
	for k, v := range data {
		form.Set(k, v)
	}
	req, err := http.NewRequest(http.MethodPost, u.String(), strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	c.setHeaders(req)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	res, err := c.hc.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	return io.ReadAll(res.Body)
}

func (c *Client) setHeaders(req *http.Request) {
	req.Header.Set("Cookie", c.cookie)
	req.Header.Set("User-Agent", defaultUA)
	req.Header.Set("Referer", "https://pan.baidu.com")
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")
	req.Header.Set("Accept-Language", "zh-CN,zh;q=0.9")
}

// GetBdstoken 获取 bdstoken，所有操作的前提
func (c *Client) GetBdstoken() error {
	body, err := c.get(baiduBaseURL+"/api/gettemplatevariable", map[string]string{
		"clienttype": "0",
		"app_id":     "38824127",
		"web":        "1",
		"fields":     `["bdstoken","token","uk","isdocuser","servertime"]`,
	})
	if err != nil {
		return fmt.Errorf("get bdstoken: %w", err)
	}
	var resp struct {
		Errno  int `json:"errno"`
		Result struct {
			Bdstoken string `json:"bdstoken"`
		} `json:"result"`
	}
	if err = json.Unmarshal(body, &resp); err != nil {
		return fmt.Errorf("parse bdstoken: %w", err)
	}
	if resp.Errno != 0 {
		return fmt.Errorf("get bdstoken errno=%d", resp.Errno)
	}
	c.bdstoken = resp.Result.Bdstoken
	return nil
}

// TransferParams 转存所需参数
type TransferParams struct {
	ShareID  string
	ShareUK  string
	FsIDs    []string
	FileName string
	IsDir    bool
}

// NormalizeLink 预处理分享链接，返回 shareURL 和 passCode
func NormalizeLink(raw string) (shareURL, passCode string) {
	s := strings.ReplaceAll(raw, "share/init?surl=", "s/1")
	re := regexp.MustCompile(`[?&]pwd=|提取码*[：:]`)
	s = re.ReplaceAllString(s, " ")
	s = strings.TrimSpace(regexp.MustCompile(`\s+`).ReplaceAllString(s, " "))
	parts := strings.SplitN(s, " ", 2)
	shareURL = parts[0]
	if len(parts) > 1 {
		code := strings.TrimSpace(parts[1])
		if len(code) >= 4 {
			passCode = code[len(code)-4:]
		}
	}
	return
}

// VerifyPassCode 验证提取码，成功返回 bdclnd
func (c *Client) VerifyPassCode(shareURL, passCode string) (string, error) {
	surl := ""
	if len(shareURL) >= 48 {
		surl = shareURL[25:48]
	} else if len(shareURL) > 25 {
		surl = shareURL[25:]
	}
	body, err := c.post(baiduBaseURL+"/share/verify", map[string]string{
		"surl":       surl,
		"bdstoken":   c.bdstoken,
		"t":          fmt.Sprintf("%d", time.Now().UnixMilli()),
		"channel":    "chunlei",
		"web":        "1",
		"clienttype": "0",
	}, map[string]string{
		"pwd":       passCode,
		"vcode":     "",
		"vcode_str": "",
	})
	if err != nil {
		return "", fmt.Errorf("verify pass code: %w", err)
	}
	var resp struct {
		Errno  int    `json:"errno"`
		Randsk string `json:"randsk"`
	}
	if err = json.Unmarshal(body, &resp); err != nil {
		return "", fmt.Errorf("parse verify resp: %w", err)
	}
	if resp.Errno != 0 {
		return "", fmt.Errorf("verify pass code errno=%d", resp.Errno)
	}
	return resp.Randsk, nil
}

// UpdateCookieBDCLND 把 bdclnd 写入 cookie
func (c *Client) UpdateCookieBDCLND(bdclnd string) {
	cookies := map[string]string{}
	for _, part := range strings.Split(c.cookie, ";") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		kv := strings.SplitN(part, "=", 2)
		if len(kv) == 2 {
			cookies[strings.TrimSpace(kv[0])] = strings.TrimSpace(kv[1])
		}
	}
	cookies["BDCLND"] = bdclnd
	parts := make([]string, 0, len(cookies))
	for k, v := range cookies {
		parts = append(parts, k+"="+v)
	}
	c.cookie = strings.Join(parts, "; ")
}

// GetTransferParams 访问分享链接，解析转存所需参数
func (c *Client) GetTransferParams(shareURL string) (*TransferParams, error) {
	body, err := c.get(shareURL, nil)
	if err != nil {
		return nil, fmt.Errorf("get share page: %w", err)
	}
	html := string(body)
	shareIDs := shareIDRegex.FindStringSubmatch(html)
	userIDs := userIDRegex.FindStringSubmatch(html)
	fsIDs := fsIDRegex.FindAllStringSubmatch(html, -1)
	fileNames := fileNameRegex.FindAllStringSubmatch(html, -1)
	isDirs := isDirRegex.FindAllStringSubmatch(html, -1)
	if len(shareIDs) < 2 || len(userIDs) < 2 || len(fsIDs) == 0 {
		return nil, fmt.Errorf("parse share page failed, link may be invalid or expired")
	}
	params := &TransferParams{
		ShareID: shareIDs[1],
		ShareUK: userIDs[1],
	}
	for _, m := range fsIDs {
		params.FsIDs = append(params.FsIDs, m[1])
	}
	if len(fileNames) > 0 {
		params.FileName = fileNames[0][1]
	}
	if len(isDirs) > 0 {
		params.IsDir = isDirs[0][1] == "1"
	}
	return params, nil
}

// Transfer 执行转存
func (c *Client) Transfer(params *TransferParams, destDir string) error {
	if destDir == "" {
		destDir = "/"
	}
	if !strings.HasPrefix(destDir, "/") {
		destDir = "/" + destDir
	}
	body, err := c.post(baiduBaseURL+"/share/transfer", map[string]string{
		"shareid":    params.ShareID,
		"from":       params.ShareUK,
		"bdstoken":   c.bdstoken,
		"channel":    "chunlei",
		"web":        "1",
		"clienttype": "0",
	}, map[string]string{
		"fsidlist": "[" + strings.Join(params.FsIDs, ",") + "]",
		"path":     destDir,
	})
	if err != nil {
		return fmt.Errorf("transfer request: %w", err)
	}
	var resp struct {
		Errno int `json:"errno"`
	}
	if err = json.Unmarshal(body, &resp); err != nil {
		return fmt.Errorf("parse transfer resp: %w", err)
	}
	if resp.Errno != 0 {
		return fmt.Errorf("transfer errno=%d (see baidu error codes)", resp.Errno)
	}
	return nil
}

// CreateDir 创建目录
func (c *Client) CreateDir(dirPath string) error {
	body, err := c.post(baiduBaseURL+"/api/create", map[string]string{
		"a":        "commit",
		"bdstoken": c.bdstoken,
	}, map[string]string{
		"path":       dirPath,
		"isdir":      "1",
		"block_list": "[]",
	})
	if err != nil {
		return fmt.Errorf("create dir: %w", err)
	}
	var resp struct {
		Errno int `json:"errno"`
	}
	if err = json.Unmarshal(body, &resp); err != nil {
		return fmt.Errorf("parse create dir resp: %w", err)
	}
	if resp.Errno != 0 {
		return fmt.Errorf("create dir errno=%d", resp.Errno)
	}
	return nil
}

// DirFile 目录文件信息
type DirFile struct {
	FsID           int64  `json:"fs_id"`
	ServerFilename string `json:"server_filename"`
	Isdir          int    `json:"isdir"`
}

// ListDir 获取目录文件列表
func (c *Client) ListDir(dirPath string) ([]DirFile, error) {
	body, err := c.get(baiduBaseURL+"/api/list", map[string]string{
		"order":     "time",
		"desc":      "1",
		"showempty": "0",
		"web":       "1",
		"page":      "1",
		"num":       "1000",
		"dir":       dirPath,
		"bdstoken":  c.bdstoken,
	})
	if err != nil {
		return nil, fmt.Errorf("list dir: %w", err)
	}
	var resp struct {
		Errno int       `json:"errno"`
		List  []DirFile `json:"list"`
	}
	if err = json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("parse list dir resp: %w", err)
	}
	if resp.Errno != 0 {
		return nil, fmt.Errorf("list dir errno=%d", resp.Errno)
	}
	return resp.List, nil
}

// CreateShare 创建分享链接
// expiry: 0=永久, 1=1天, 7=7天, 30=30天
func (c *Client) CreateShare(fsID int64, expiry int, password string) (string, error) {
	body, err := c.post(baiduBaseURL+"/share/set", map[string]string{
		"channel":    "chunlei",
		"bdstoken":   c.bdstoken,
		"clienttype": "0",
		"app_id":     "250528",
		"web":        "1",
	}, map[string]string{
		"period":        fmt.Sprintf("%d", expiry),
		"pwd":           password,
		"eflag_disable": "true",
		"channel_list":  "[]",
		"schannel":      "4",
		"fid_list":      fmt.Sprintf("[%d]", fsID),
	})
	if err != nil {
		return "", fmt.Errorf("create share: %w", err)
	}
	var resp struct {
		Errno int    `json:"errno"`
		Link  string `json:"link"`
	}
	if err = json.Unmarshal(body, &resp); err != nil {
		return "", fmt.Errorf("parse create share resp: %w", err)
	}
	if resp.Errno != 0 {
		return "", fmt.Errorf("create share errno=%d", resp.Errno)
	}
	return resp.Link, nil
}
