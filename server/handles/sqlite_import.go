package handles

import (
	"context"
	"os"
	"path/filepath"

	"github.com/alist-org/alist/v3/internal/conf"
	"github.com/alist-org/alist/v3/internal/search"
	"github.com/alist-org/alist/v3/server/common"
	"github.com/gin-gonic/gin"
	log "github.com/sirupsen/logrus"
)

// ImportSQLiteIndex 接收上传的百度网盘 SQLite .db 文件，异步导入到搜索索引。
// 请求格式：multipart/form-data
//   - file: .db 文件
//   - mount_prefix: alist 中对应的挂载路径，如 /百度网盘（可为空，默认 /）
func ImportSQLiteIndex(c *gin.Context) {
	if search.Running() {
		common.ErrorStrResp(c, "index is running, please wait", 400)
		return
	}

	mountPrefix := c.PostForm("mount_prefix")
	if mountPrefix == "" {
		mountPrefix = "/"
	}

	fh, err := c.FormFile("file")
	if err != nil {
		common.ErrorResp(c, err, 400)
		return
	}

	// 保存到临时目录
	tmpPath := filepath.Join(conf.Conf.TempDir, "sqlite_import_"+fh.Filename)
	if err = c.SaveUploadedFile(fh, tmpPath); err != nil {
		common.ErrorResp(c, err, 500)
		return
	}

	// 检查文件是否存在
	if _, err = os.Stat(tmpPath); err != nil {
		common.ErrorResp(c, err, 500)
		return
	}

	go func() {
		if err := search.ImportFromSQLite(context.Background(), tmpPath, mountPrefix); err != nil {
			log.Errorf("[sqlite_import] import failed: %v", err)
		}
	}()

	common.SuccessResp(c, gin.H{
		"message": "import started, use GET /api/admin/index/progress to track progress",
	})
}
