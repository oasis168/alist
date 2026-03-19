package search

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"path"
	"strings"
	"time"

	log "github.com/sirupsen/logrus"
	"github.com/alist-org/alist/v3/pkg/utils"
)

const (
	incrementalBatchSize = 10000
	incrementalCooldown  = 30 * time.Minute // 两次增量之间的最小间隔
)

// IncrementalResp 百度网盘 listall 接口响应
type baiduListAllResp struct {
	Errno   int              `json:"errno"`
	HasMore int              `json:"has_more"`
	List    []baiduFileEntry `json:"list"`
}

type baiduFileEntry struct {
	Path           string `json:"path"`
	ServerFilename string `json:"server_filename"`
	Isdir          int    `json:"isdir"`
	Size           int64  `json:"size"`
	ServerMtime    int64  `json:"server_mtime"`
}

// IncrementalBuild 基于 mtime 增量拉取百度网盘文件，追加写入搜索索引。
// accessToken: 百度网盘 access_token
// mountPrefix: alist 挂载路径前缀（如 "/百度网盘"）
// since: 增量起始时间戳（Unix 秒），传 0 则取 7 天前
func IncrementalBuild(ctx context.Context, accessToken string, mountPrefix string, since int64) error {
	if since == 0 {
		since = time.Now().Unix() - 7*86400
	}

	mountPrefix = strings.TrimRight(mountPrefix, "/")

	var (
		start    int = 0
		total    int = 0
		imported int = 0
	)

	log.Infof("[incremental] start, access_token present: %v, mount_prefix: %s, since: %d",
		len(accessToken) > 0, mountPrefix, since)

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		url := fmt.Sprintf(
			"https://pan.baidu.com/rest/2.0/xpan/multimedia?method=listall&path=%%2F&access_token=%s&order=time&desc=1&recursion=1&mtime=%d&start=%d&limit=%d",
			accessToken, since, start, incrementalBatchSize,
		)

		resp, err := httpGet(ctx, url)
		if err != nil {
			return fmt.Errorf("listall request failed at start=%d: %w", start, err)
		}

		if resp.Errno != 0 {
			return fmt.Errorf("listall errno=%d", resp.Errno)
		}

		if len(resp.List) == 0 {
			break
		}

		var objs []ObjWithParent
		for _, f := range resp.List {
			parentPath := path.Dir(f.Path)
			fullParent := mountPrefix + parentPath
			if fullParent == "" {
				fullParent = "/"
			}
			objs = append(objs, ObjWithParent{
				Parent: fullParent,
				Obj: &sqliteObj{
					name:  f.ServerFilename,
					isDir: f.Isdir == 1,
					size:  f.Size,
					path:  mountPrefix + f.Path,
				},
			})
		}

		if err = BatchIndex(ctx, objs); err != nil {
			return fmt.Errorf("batch index at start=%d: %w", start, err)
		}

		imported += len(objs)
		total += len(resp.List)
		start += incrementalBatchSize
		log.Infof("[incremental] imported %d records so far", imported)

		if resp.HasMore == 0 {
			break
		}
	}

	now := time.Now()
	log.Infof("[incremental] done, imported: %d", imported)

	// 更新进度（叠加已有 ObjCount）
	progress, err := Progress()
	if err == nil {
		progress.ObjCount += uint64(imported)
		progress.IsDone = true
		progress.LastDoneTime = &now
		WriteProgress(progress)
	}

	return nil
}

// httpGet 发起简单 GET 请求并解析 baiduListAllResp
func httpGet(ctx context.Context, url string) (*baiduListAllResp, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "pan.baidu.com")

	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()

	body, err := io.ReadAll(res.Body)
	if err != nil {
		return nil, err
	}

	var result baiduListAllResp
	if err = json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("unmarshal response: %w", err)
	}
	return &result, nil
}

// 确保 utils 包被引用（HashInfo）
var _ = utils.HashInfo{}
