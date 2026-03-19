package handles

import (
	"context"

	"github.com/alist-org/alist/v3/internal/search"
	"github.com/alist-org/alist/v3/server/common"
	"github.com/gin-gonic/gin"
	log "github.com/sirupsen/logrus"
)

// IncrementalIndexReq 增量索引请求参数
type IncrementalIndexReq struct {
	// AccessToken: 百度网盘 access_token，必填
	AccessToken string `json:"access_token" binding:"required"`
	// MountPrefix: alist 中对应的挂载路径，如 /百度网盘，可为空（默认 /）
	MountPrefix string `json:"mount_prefix"`
	// Since: 增量起始 Unix 时间戳（秒），0 表示取 7 天前
	Since int64 `json:"since"`
}

// IncrementalIndex 触发百度网盘增量索引。
// 只拉取 since 时间之后修改的文件，追加写入索引，不清空旧数据。
func IncrementalIndex(c *gin.Context) {
	if search.Running() {
		common.ErrorStrResp(c, "index is running, please wait", 400)
		return
	}

	var req IncrementalIndexReq
	if err := c.ShouldBindJSON(&req); err != nil {
		common.ErrorResp(c, err, 400)
		return
	}

	if req.MountPrefix == "" {
		req.MountPrefix = "/"
	}

	go func() {
		if err := search.IncrementalBuild(
			context.Background(),
			req.AccessToken,
			req.MountPrefix,
			req.Since,
		); err != nil {
			log.Errorf("[incremental] build failed: %v", err)
		}
	}()

	common.SuccessResp(c, gin.H{
		"message": "incremental index started, use GET /api/admin/index/progress to track progress",
	})
}
