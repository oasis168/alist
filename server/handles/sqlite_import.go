package handles

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/alist-org/alist/v3/internal/conf"
	"github.com/alist-org/alist/v3/internal/model"
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

// ListServerDbFiles 列出服务器固定目录下的 .db 文件
// GET /api/admin/index/list_db_files
func ListServerDbFiles(c *gin.Context) {
	dir := c.Query("dir")
	if dir == "" {
		dir = "/opt/alist/db"
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		common.ErrorStrResp(c, fmt.Sprintf("读取目录失败: %v", err), 400)
		return
	}
	type FileInfo struct {
		Name string `json:"name"`
		Path string `json:"path"`
		Size int64  `json:"size"`
	}
	files := make([]FileInfo, 0)
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(strings.ToLower(e.Name()), ".db") {
			info, _ := e.Info()
			files = append(files, FileInfo{
				Name: e.Name(),
				Path: filepath.Join(dir, e.Name()),
				Size: info.Size(),
			})
		}
	}
	common.SuccessResp(c, files)
}

// ImportServerDbFile 从服务器本地路径导入 .db 文件
// POST /api/admin/index/import_sqlite_server
func ImportServerDbFile(c *gin.Context) {
	var req struct {
		FilePath    string `json:"file_path" binding:"required"`
		MountPrefix string `json:"mount_prefix" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		common.ErrorResp(c, err, 400)
		return
	}
	// 安全检查：只允许读取 .db 文件
	if !strings.HasSuffix(strings.ToLower(req.FilePath), ".db") {
		common.ErrorStrResp(c, "只允许导入 .db 文件", 400)
		return
	}
	go func() {
		if err := search.ImportFromSQLite(context.Background(), req.FilePath, req.MountPrefix); err != nil {
			search.WriteProgress(&model.IndexProgress{
				Error: err.Error(),
			})
		}
	}()
	common.SuccessResp(c, "导入任务已启动，请查看进度")
}
