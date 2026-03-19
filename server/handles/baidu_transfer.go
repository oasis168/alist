package handles

import (
	"fmt"
	"strings"

	"github.com/alist-org/alist/v3/internal/baidu"
	"github.com/alist-org/alist/v3/server/common"
	"github.com/gin-gonic/gin"
)

// BaiduTransferReq 转存请求
type BaiduTransferReq struct {
	// Cookie: 百度网盘完整 Cookie 字符串（必填，含 BAIDUID、BDUSS 等）
	Cookie string `json:"cookie" binding:"required"`
	// Links: 分享链接列表，每个元素为 "链接 提取码" 格式（提取码可选）
	Links []string `json:"links" binding:"required,min=1"`
	// DestDir: 转存目标目录，留空则转存到根目录
	DestDir string `json:"dest_dir"`
}

// BaiduTransferItemResult 单条转存结果
type BaiduTransferItemResult struct {
	Link     string `json:"link"`
	FileName string `json:"file_name"`
	IsDir    bool   `json:"is_dir"`
	Success  bool   `json:"success"`
	Error    string `json:"error,omitempty"`
}

// BaiduTransfer 批量转存百度网盘分享链接
// POST /api/admin/baidu/transfer
func BaiduTransfer(c *gin.Context) {
	var req BaiduTransferReq
	if err := c.ShouldBindJSON(&req); err != nil {
		common.ErrorResp(c, err, 400)
		return
	}

	client := baidu.NewClient(req.Cookie)
	if err := client.GetBdstoken(); err != nil {
		common.ErrorStrResp(c, fmt.Sprintf("get bdstoken failed: %v", err), 400)
		return
	}

	// 如果目标目录不为空且不存在，自动创建
	if req.DestDir != "" {
		destDir := req.DestDir
		if !strings.HasPrefix(destDir, "/") {
			destDir = "/" + destDir
		}
		// 尝试创建（已存在时 errno=-8 或 0，不报错）
		_ = client.CreateDir(destDir)
	}

	results := make([]BaiduTransferItemResult, 0, len(req.Links))
	for _, link := range req.Links {
		link = strings.TrimSpace(link)
		if link == "" {
			continue
		}
		result := doTransfer(client, link, req.DestDir)
		results = append(results, result)
	}

	common.SuccessResp(c, results)
}

func doTransfer(client *baidu.Client, rawLink, destDir string) BaiduTransferItemResult {
	shareURL, passCode := baidu.NormalizeLink(rawLink)
	res := BaiduTransferItemResult{Link: rawLink}

	// 有提取码先验证
	if passCode != "" {
		bdclnd, err := client.VerifyPassCode(shareURL, passCode)
		if err != nil {
			res.Error = fmt.Sprintf("verify pass code: %v", err)
			return res
		}
		client.UpdateCookieBDCLND(bdclnd)
	}

	// 获取转存参数
	params, err := client.GetTransferParams(shareURL)
	if err != nil {
		res.Error = fmt.Sprintf("get transfer params: %v", err)
		return res
	}
	res.FileName = params.FileName
	res.IsDir = params.IsDir

	// 执行转存
	if err = client.Transfer(params, destDir); err != nil {
		res.Error = fmt.Sprintf("transfer: %v", err)
		return res
	}
	res.Success = true
	return res
}

// BaiduShareReq 批量分享请求
type BaiduShareReq struct {
	// Cookie: 百度网盘完整 Cookie 字符串（必填）
	Cookie string `json:"cookie" binding:"required"`
	// SourceDir: 要分享的源目录（必填），会分享该目录下所有文件和子目录
	SourceDir string `json:"source_dir" binding:"required"`
	// Expiry: 分享有效期，0=永久, 1=1天, 7=7天, 30=30天，默认0
	Expiry int `json:"expiry"`
	// Password: 提取码（4位字母数字），留空则不设提取码
	Password string `json:"password"`
}

// BaiduShareItemResult 单条分享结果
type BaiduShareItemResult struct {
	FsID     int64  `json:"fs_id"`
	Filename string `json:"filename"`
	IsDir    bool   `json:"is_dir"`
	Link     string `json:"link,omitempty"`
	Password string `json:"password,omitempty"`
	Success  bool   `json:"success"`
	Error    string `json:"error,omitempty"`
}

// BaiduShare 批量分享指定目录下的文件
// POST /api/admin/baidu/share
func BaiduShare(c *gin.Context) {
	var req BaiduShareReq
	if err := c.ShouldBindJSON(&req); err != nil {
		common.ErrorResp(c, err, 400)
		return
	}

	client := baidu.NewClient(req.Cookie)
	if err := client.GetBdstoken(); err != nil {
		common.ErrorStrResp(c, fmt.Sprintf("get bdstoken failed: %v", err), 400)
		return
	}

	sourceDir := req.SourceDir
	if !strings.HasPrefix(sourceDir, "/") {
		sourceDir = "/" + sourceDir
	}

	files, err := client.ListDir(sourceDir)
	if err != nil {
		common.ErrorStrResp(c, fmt.Sprintf("list dir failed: %v", err), 400)
		return
	}
	if len(files) == 0 {
		common.ErrorStrResp(c, "source dir is empty", 400)
		return
	}

	results := make([]BaiduShareItemResult, 0, len(files))
	for _, f := range files {
		result := BaiduShareItemResult{
			FsID:     f.FsID,
			Filename: f.ServerFilename,
			IsDir:    f.Isdir == 1,
		}
		link, err := client.CreateShare(f.FsID, req.Expiry, req.Password)
		if err != nil {
			result.Error = err.Error()
		} else {
			result.Link = link
			result.Password = req.Password
			result.Success = true
		}
		results = append(results, result)
	}

	common.SuccessResp(c, results)
}
