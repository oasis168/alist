package handles

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	neturl "net/url"
	"path"
	"strings"
	"time"

	"github.com/alist-org/alist/v3/internal/baidu"
	"github.com/alist-org/alist/v3/internal/conf"
	"github.com/alist-org/alist/v3/internal/db"
	internaldriver "github.com/alist-org/alist/v3/internal/driver"
	"github.com/alist-org/alist/v3/internal/op"
	"github.com/alist-org/alist/v3/internal/search"
	"github.com/alist-org/alist/v3/server/common"
	"github.com/gin-gonic/gin"
)

var baiduHTTPClient = &http.Client{Timeout: 15 * time.Second}

func resolveBaiduPathFromRequest(rawPath string) (internaldriver.Driver, string, string, error) {
	rawPath = strings.TrimSpace(rawPath)
	if rawPath == "" {
		return nil, "", "", fmt.Errorf("path is empty")
	}
	storageDriver, actualPath, err := op.GetStorageAndActualPath(rawPath)
	if err != nil {
		return nil, "", "", err
	}
	mountPath := storageDriver.GetStorage().MountPath
	baiduPath := path.Clean(actualPath)
	if baiduPath == "." || baiduPath == "" {
		baiduPath = "/"
	}
	if !strings.HasPrefix(baiduPath, "/") {
		baiduPath = "/" + baiduPath
	}
	return storageDriver, mountPath, baiduPath, nil
}

func getBaiduShareClient() (*baidu.Client, error) {
	cookieItem, err := op.GetSettingItemByKey(conf.BaiduTransferCookie)
	if err != nil || cookieItem.Value == "" {
		return nil, fmt.Errorf("请先在设置 > Baidu 中配置 baidu_transfer_cookie")
	}
	client := baidu.NewClient(cookieItem.Value)
	if err := client.GetBdstoken(); err != nil {
		return nil, fmt.Errorf("获取bdstoken失败: %w", err)
	}
	return client, nil
}

// getFsIDByPath 优先从搜索索引查 fs_id，找不到再降级调百度 API
// filePath: 百度网盘内路径（已去掉挂载前缀），fullPath: alist 完整路径（含挂载前缀）
func getFsIDByPath(accessToken, filePath string, fullPath ...string) (int64, error) {
	dir := path.Dir(filePath)
	name := path.Base(filePath)

	// 1. 先用完整路径（含挂载前缀）查索引，适配导入时带挂载前缀的情况
	if len(fullPath) > 0 && fullPath[0] != "" {
		fullDir := path.Dir(fullPath[0])
		if fsID, err := search.GetFsIDByPath(context.Background(), fullDir, name); err == nil && fsID > 0 {
			return fsID, nil
		}
		if fsID, err := db.GetFsIDByPath(fullDir, name); err == nil && fsID > 0 {
			return fsID, nil
		}
	}

	// 2. 再用 baiduPath（去掉挂载前缀）查索引
	if fsID, err := search.GetFsIDByPath(context.Background(), dir, name); err == nil && fsID > 0 {
		return fsID, nil
	}

	// 3. 再查本地 SQLite（database 模式备用）
	if fsID, err := db.GetFsIDByPath(dir, name); err == nil && fsID > 0 {
		return fsID, nil
	}

	// 4. 降级：调百度 API
	params := neturl.Values{}
	params.Set("method", "search")
	params.Set("access_token", accessToken)
	params.Set("key", name)
	params.Set("dir", dir)
	params.Set("recursion", "0")
	params.Set("num", "20")
	apiURL := "https://pan.baidu.com/rest/2.0/xpan/file?" + params.Encode()
	req, _ := http.NewRequest(http.MethodGet, apiURL, nil)
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36")
	req.Header.Set("Referer", "https://pan.baidu.com/")
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
		if f.Path == filePath {
			return f.FsID, nil
		}
	}
	return 0, fmt.Errorf("file not found: %s", filePath)
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
	if decoded, err := neturl.PathUnescape(req.Path); err == nil {
		req.Path = decoded
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

	_, mountPrefix, baiduPath, err := resolveBaiduPathFromRequest(req.Path)
	if err != nil {
		common.ErrorStrResp(c, fmt.Sprintf("解析百度路径失败: %v", err), 400)
		return
	}

	// 源账号生成临时分享链接（1天，带随机提取码），直接用 path-based 接口，无需查询 fs_id
	shareCookie, scErr := getBaiduStorageCookie(mountPrefix)
	if scErr != nil {
		shareCookie = cookieItem.Value // 存储无 cookie 时降级用目标账号 cookie
	}
	shareClient := baidu.NewClient(shareCookie)
	shareLink, sharePwd, err := shareClient.CreateShareByPaths([]string{baiduPath}, 1, "")
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
	// 分享链接带密码时，需先验证提取码获取 bdclnd，再访问分享页面
	if sharePwd != "" {
		bdclnd, verifyErr := targetClient.VerifyPassCode(shareURL, sharePwd)
		if verifyErr != nil {
			common.ErrorStrResp(c, fmt.Sprintf("验证提取码失败: %v", verifyErr), 400)
			return
		}
		targetClient.UpdateCookieBDCLND(bdclnd)
	}
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

func getBaiduStorageCookie(mountPrefix string) (string, error) {
	storageDriver, err := op.GetStorageByMountPath(strings.TrimRight(mountPrefix, "/"))
	if err != nil {
		return "", fmt.Errorf("storage not found: %s: %w", mountPrefix, err)
	}
	b, err := json.Marshal(storageDriver.GetAddition())
	if err != nil {
		return "", err
	}
	var addition struct {
		Cookie string `json:"cookie"`
	}
	if err = json.Unmarshal(b, &addition); err != nil {
		return "", err
	}
	if addition.Cookie == "" {
		return "", fmt.Errorf("存储未配置 Cookie")
	}
	return addition.Cookie, nil
}

// BaiduFileShare 给搜索到的百度网盘文件生成分享链接
// 优先用挂载存储自身的 cookie 分享，支持多网盘挂载各用各的账号
// POST /api/admin/baidu/share_file
func BaiduFileShare(c *gin.Context) {
	var req BaiduFileShareReq
	if err := c.ShouldBindJSON(&req); err != nil {
		common.ErrorResp(c, err, 400)
		return
	}
	if decoded, err := neturl.PathUnescape(req.Path); err == nil {
		req.Path = decoded
	}
	if req.Period == 0 {
		req.Period = 7
	}

	_, mountPrefix, baiduPath, err := resolveBaiduPathFromRequest(req.Path)
	if err != nil {
		common.ErrorStrResp(c, fmt.Sprintf("解析百度路径失败: %v", err), 400)
		return
	}

	// 优先用挂载存储自身的 cookie 分享（支持多网盘，各用各账号的 cookie）
	// 注意：/share/pset 只需要 BDUSS cookie，不需要 bdstoken，所以不调用 GetBdstoken
	cookie, cookieErr := getBaiduStorageCookie(mountPrefix)
	if cookieErr == nil {
		client := baidu.NewClient(cookie)
		link, pwd, shareErr := client.CreateShareByPaths([]string{baiduPath}, req.Period, "")
		if shareErr == nil {
			common.SuccessResp(c, gin.H{"link": link, "pwd": pwd, "path": baiduPath})
			return
		}
		cookieErr = shareErr
	}

	// 存储没有配置 cookie 或分享失败，降级用全局 baidu_transfer_cookie
	globalCookieItem, globalErr := op.GetSettingItemByKey(conf.BaiduTransferCookie)
	if globalErr != nil || globalCookieItem.Value == "" {
		common.ErrorStrResp(c, fmt.Sprintf("分享失败: %v", cookieErr), 400)
		return
	}
	globalClient := baidu.NewClient(globalCookieItem.Value)
	link, pwd, err := globalClient.CreateShareByPaths([]string{baiduPath}, req.Period, "")
	if err != nil {
		common.ErrorStrResp(c, fmt.Sprintf("分享失败: %v", err), 400)
		return
	}
	common.SuccessResp(c, gin.H{"link": link, "pwd": pwd, "path": baiduPath})
}
