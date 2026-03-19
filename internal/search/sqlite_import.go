package search

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path"
	"strings"
	"time"

	_ "github.com/mattn/go-sqlite3"
	"github.com/alist-org/alist/v3/internal/model"
	"github.com/alist-org/alist/v3/pkg/utils"
	log "github.com/sirupsen/logrus"
)

const sqliteImportBatchSize = 1000

// ImportFromSQLite 从百度网盘导出的 SQLite .db 文件批量导入搜索索引。
// dbPath: 临时文件路径，导入完成后自动删除。
// mountPrefix: alist 中对应的挂载路径前缀（如 "/百度网盘"），拼接到 parent_path 前。
func ImportFromSQLite(ctx context.Context, dbPath string, mountPrefix string) error {
	defer func() {
		if err := os.Remove(dbPath); err != nil {
			log.Warnf("[sqlite_import] remove temp db file failed: %v", err)
		}
	}()

	db, err := sql.Open("sqlite3", dbPath+"?mode=ro")
	if err != nil {
		return fmt.Errorf("open sqlite db: %w", err)
	}
	defer db.Close()

	// 获取总数
	var total int64
	if err = db.QueryRowContext(ctx, "SELECT count(id) FROM cache_file").Scan(&total); err != nil {
		return fmt.Errorf("count cache_file: %w", err)
	}
	log.Infof("[sqlite_import] total records: %d, mount_prefix: %s", total, mountPrefix)

	mountPrefix = strings.TrimRight(mountPrefix, "/")

	var (
		offset   int64 = 0
		imported int64 = 0
	)

	WriteProgress(&model.IndexProgress{ObjCount: 0, IsDone: false})

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		rows, err := db.QueryContext(ctx,
			"SELECT parent_path, server_filename, isdir, file_size FROM cache_file ORDER BY id LIMIT ? OFFSET ?",
			sqliteImportBatchSize, offset,
		)
		if err != nil {
			return fmt.Errorf("query cache_file at offset %d: %w", offset, err)
		}

		var objs []ObjWithParent
		for rows.Next() {
			var (
				parentPath     string
				serverFilename string
				isdir          int
				fileSize       int64
			)
			if err = rows.Scan(&parentPath, &serverFilename, &isdir, &fileSize); err != nil {
				rows.Close()
				return fmt.Errorf("scan row: %w", err)
			}
			parentPath = strings.TrimRight(parentPath, "/")
			fullParent := mountPrefix + parentPath
			if fullParent == "" {
				fullParent = "/"
			}
			objs = append(objs, ObjWithParent{
				Parent: fullParent,
				Obj: &sqliteObj{
					name:  serverFilename,
					isDir: isdir == 1,
					size:  fileSize,
					path:  path.Join(fullParent, serverFilename),
				},
			})
		}
		rows.Close()

		if len(objs) == 0 {
			break
		}

		if err = BatchIndex(ctx, objs); err != nil {
			return fmt.Errorf("batch index at offset %d: %w", offset, err)
		}

		imported += int64(len(objs))
		offset += sqliteImportBatchSize
		log.Infof("[sqlite_import] imported %d / %d", imported, total)
		WriteProgress(&model.IndexProgress{ObjCount: uint64(imported), IsDone: false})

		if int64(len(objs)) < sqliteImportBatchSize {
			break
		}
	}

	now := time.Now()
	log.Infof("[sqlite_import] done, total imported: %d", imported)
	WriteProgress(&model.IndexProgress{
		ObjCount:     uint64(imported),
		IsDone:       true,
		LastDoneTime: &now,
	})
	return nil
}

// sqliteObj 实现 model.Obj 接口，用于从 SQLite 读取的文件记录
type sqliteObj struct {
	name  string
	isDir bool
	size  int64
	path  string
}

func (o *sqliteObj) GetName() string           { return o.name }
func (o *sqliteObj) GetSize() int64            { return o.size }
func (o *sqliteObj) IsDir() bool               { return o.isDir }
func (o *sqliteObj) ModTime() time.Time        { return time.Time{} }
func (o *sqliteObj) CreateTime() time.Time     { return time.Time{} }
func (o *sqliteObj) GetHash() utils.HashInfo   { return utils.HashInfo{} }
func (o *sqliteObj) GetID() string             { return "" }
func (o *sqliteObj) GetPath() string           { return o.path }
