package handles

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"path"
	"strings"
	"time"

	"github.com/alist-org/alist/v3/internal/baidu"
	"github.com/alist-org/alist/v3/internal/conf"
	"github.com/alist-org/alist/v3/internal/op"
	"github.com/alist-org/alist/v3/server/common"
	"github.com/gin-gonic/gin"
)

var baiduHTTPClient = &http.Client{Timeout: 15 * time.Second}

// getFsIDByPath 用 access_token 搜索文件，返回 fs_id
func getFsIDByPath(accessToken, filePath string) (int64, error) {
	dir := path.Dir(filePath)
	name := path.Base(filePath)
	url := fmt.Sprintf(
		"https://pan.baidu.com/rest/2.0/xpan/file?method=search&access_token=%s&key=%s&dir=%s&recursion=0&num=20",
		accessToken, name, dir,
	)
	req, _ := http.NewRequest(http.MethodGet, url, nil)
	req.Header.Set("User-Agent", "pan.baidu.com")
	resp, err := baiduHTTPClient.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	var result struct {
		Errno int `json:"errno"`
		List  []struct {
			FsID int64  `json:"fs_id"`
			Path string `json:"path"`
			Name string `json:"server_filename"`
		} `json:"list"`
	}
	if err = json.Unmarshal(body, &result); err != nil {
		return 0, fmt.Errorf("parse search resp: %w", err)
	}
	if result.Errno != 0 {
		return 0, fmt.Errorf("search api errno=%d", result.Errno)
	}
	if len(result.List) == 0 {
		return 0, fmt.Errorf("file not found: %s", filePath)
	}
	for _, f := range result.List {
		if f.Path == filePath || f.Name == name {
			return f.FsID, nil
		}
	}
	return result.List[0].FsID, nil
}

// getBaiduAccessToken 从挂载存储里提取 access_token
func getBaiduAccessToken(mountPrefix string) (string, error) {
	prefix := strings.TrimRight(mountPrefix, "/")
	storageDriver, err := op.GetStorageByMountPath(prefix)
	if err != nil {
		return "", fmt.Errorf("storage not found: %s: %w", prefix, err)
	}
	b, err := json.Marshal(storageDriver.GetAddition())
	if err != nil {
		return "", err
	}
	var addition struct {
		AccessToken string `json:"AccessToken"`
	}
	if err = json.Unmarshal(b, &addition); err != nil {
		return "", err
	}
	if addition.AccessToken == "" {
		return "", fmt.Errorf("access_token is empty, not a BaiduNetdisk storage")
	}
	return addition.AccessToken, nil
}

// shareByAccessToken 用 access_token 生成分享链接
func shareByAccessToken(accessToken string, fsID int64, period int) (string, error) {
	url := fmt.Sprintf(
		"https://pan.baidu.com/rest/2.0/xpan/share?method=set&access_token=%s",
		accessToken,
	)
	body := fmt.Sprintf("fid_list=[%d]&period=%d&pwd=&eflag_disable=true&channel_list=[]&schannel=4", fsID, period)
	req, _ := http.NewRequest(http.MethodPost, url, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("User-Agent", "pan.baidu.com")
	resp, err := baiduHTTPClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	var result struct {
		Errno    int    `json:"errno"`
		Link     string `json:"link"`
		Shorturl string `json:"shorturl"`
	}
	if err = json.Unmarshal(respBody, &result); err != nil {
		return "", fmt.Errorf("parse share resp: %w", err)
	}
	if result.Errno != 0 {
		return "", fmt.Errorf("share errno=%d", result.Errno)
	}
	if result.Link != "" {
		return result.Link, nil
	}
	return result.Shorturl, nil
}

// BaiduFileTransferReq 文件转存请求
type BaiduFileTransferReq struct {
	Path string `json:"path" binding:"required"`
	Dest string `json:"dest"`
}

// BaiduFileTransfer 将搜索到的百度网盘文件转存到另一个账号
// 流程：源账号生成临时分享链接 -> 目标账号转存
// POST /api/admin/baidu/transfer_file
func BaiduFileTransfer(c *gin.Context) {
	var req BaiduFileTransferReq
	if err := c.ShouldBindJSON(&req); err != nil {
		common.ErrorResp(c, err, 400)
		return
	}

	// 读取目标账号 Cookie
	cookieItem, err := op.GetSettingItemByKey(conf.BaiduTransferCookie)
	if err != nil || cookieItem.Value == "" {
		common.ErrorStrResp(c, "请先在设置 > Baidu 中配置 baidu_transfer_cookie", 400)
		return
	}

	// 目标路径
	destDir := req.Dest
	if destDir == "" {
		if destItem, err := op.GetSettingItemByKey(conf.BaiduTransferDest); err == nil && destItem.Value != "" {
			destDir = destItem.Value
		} else {
			destDir = "/我的资源"
		}
	}

	// 解析挂载路径和百度网盘内路径
	trimmed := strings.TrimPrefix(req.Path, "/")
	parts := strings.SplitN(trimmed, "/", 2)
	if len(parts) < 2 {
		common.ErrorStrResp(c, "path 格式错误，需要 /挂载路径/文件路径", 400)
		return
	}
	mountPrefix := "/" + parts[0]
	baiduPath := "/" + parts[1]

	// 获取源账号 access_token
	accessToken, err := getBaiduAccessToken(mountPrefix)
	if err != nil {
		common.ErrorStrResp(c, fmt.Sprintf("获取access_token失败: %v", err), 400)
		return
	}

	// 查询 fs_id
	fsID, err := getFsIDByPath(accessToken, baiduPath)
	if err != nil {
		common.ErrorStrResp(c, fmt.Sprintf("查询fs_id失败: %v", err), 400)
		return
	}

	// 源账号生成临时分享链接（1天，无需提取码）
	shareLink, err := shareByAccessToken(accessToken, fsID, 1)
	if err != nil {
		common.ErrorStrResp(c, fmt.Sprintf("生成临时分享链接失败: %v", err), 400)
		return
	}

	// 目标账号执行转存
	targetClient := baidu.NewClient(cookieItem.Value)
	if err = targetClient.GetBdstoken(); err != nil {
		common.ErrorStrResp(c, fmt.Sprintf("目标账号获取bdstoken失败: %v", err), 400)
		return
	}
	_ = targetClient.CreateDir(destDir)

	shareURL, _ := baidu.NormalizeLink(shareLink)
	params, err := targetClient.GetTransferParams(shareURL)
	if err != nil {
		common.ErrorStrResp(c, fmt.Sprintf("解析分享链接失败: %v", err), 400)
		return
	}
	if err = targetClient.Transfer(params, destDir); err != nil {
		common.ErrorStrResp(c, fmt.Sprintf("转存失败: %v", err), 400)
		return
	}

	common.SuccessResp(c, gin.H{"message": "转存成功", "dest": destDir})
}

// BaiduFileShareReq 文件分享请求
type BaiduFileShareReq struct {
	Path   string `json:"path" binding:"required"`
	Period int    `json:"period"`
}

// BaiduFileShare 给搜索到的百度网盘文件生成分享链接
// POST /api/admin/baidu/share_file
func BaiduFileShare(c *gin.Context) {
	var req BaiduFileShareReq
	if err := c.ShouldBindJSON(&req); err != nil {
		common.ErrorResp(c, err, 400)
		return
	}
	if req.Period == 0 {
		req.Period = 7
	}

	trimmed := strings.TrimPrefix(req.Path, "/")
	parts := strings.SplitN(trimmed, "/", 2)
	if len(parts) < 2 {
		common.ErrorStrResp(c, "path 格式错误，需要 /挂载路径/文件路径", 400)
		return
	}
	mountPrefix := "/" + parts[0]
	baiduPath := "/" + parts[1]

	accessToken, err := getBaiduAccessToken(mountPrefix)
	if err != nil {
		common.ErrorStrResp(c, fmt.Sprintf("获取access_token失败: %v", err), 400)
		return
	}

	fsID, err := getFsIDByPath(accessToken, baiduPath)
	if err != nil {
		common.ErrorStrResp(c, fmt.Sprintf("查询fs_id失败: %v", err), 400)
		return
	}

	link, err := shareByAccessToken(accessToken, fsID, req.Period)
	if err != nil {
		common.ErrorStrResp(c, fmt.Sprintf("分享失败: %v", err), 400)
		return
	}

	common.SuccessResp(c, gin.H{"link": link})
}
