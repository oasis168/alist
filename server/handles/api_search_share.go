package handles

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/alist-org/alist/v3/internal/baidu"
	"github.com/alist-org/alist/v3/internal/conf"
	"github.com/alist-org/alist/v3/internal/model"
	"github.com/alist-org/alist/v3/internal/op"
	"github.com/alist-org/alist/v3/internal/search"
	"github.com/alist-org/alist/v3/server/common"
	"github.com/gin-gonic/gin"
)

// API Key 验证中间件
var (
	searchShareLimiter = newRateLimiter(20, time.Minute)
)

// 频率限制器
type rateLimiter struct {
	requests map[string][]time.Time
	mu       sync.Mutex
	limit    int
	window   time.Duration
}

func newRateLimiter(limit int, window time.Duration) *rateLimiter {
	return &rateLimiter{
		requests: make(map[string][]time.Time),
		limit:    limit,
		window:   window,
	}
}

func (rl *rateLimiter) allow(key string) bool {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	now := time.Now()
	cutoff := now.Add(-rl.window)

	// 清理过期记录
	if times, exists := rl.requests[key]; exists {
		valid := []time.Time{}
		for _, t := range times {
			if t.After(cutoff) {
				valid = append(valid, t)
			}
		}
		rl.requests[key] = valid
	}

	// 检查是否超限
	if len(rl.requests[key]) >= rl.limit {
		return false
	}

	// 记录本次请求
	rl.requests[key] = append(rl.requests[key], now)
	return true
}

// SearchAndShareReq 搜索并分享请求
type SearchAndShareReq struct {
	Keyword string `json:"keyword" binding:"required"`
	Index   int    `json:"index"`   // 返回第几个结果，默认 0（第一个）
	Period  int    `json:"period"`  // 分享有效期（天），默认 7
}

// SearchAndShareResp 搜索并分享响应
type SearchAndShareResp struct {
	Link         string `json:"link"`
	Password     string `json:"pwd"`
	FileName     string `json:"file_name"`
	FilePath     string `json:"file_path"`
	FileSize     int64  `json:"file_size"`
	TotalMatches int    `json:"total_matches"`
	UK           int64  `json:"uk"`
	FID          int64  `json:"fid"`
}

// APIKeyAuth API Key 验证中间件
func APIKeyAuth() gin.HandlerFunc {
	return func(c *gin.Context) {
		apiKey := c.GetHeader("X-API-Key")
		if apiKey == "" {
			common.ErrorStrResp(c, "Missing API Key", 401)
			c.Abort()
			return
		}

		// 从设置中读取 API Keys
		apiKeysItem, err := op.GetSettingItemByKey(conf.SearchShareAPIKeys)
		if err != nil || apiKeysItem.Value == "" {
			common.ErrorStrResp(c, "API Keys not configured", 500)
			c.Abort()
			return
		}

		// 支持多个 key，用逗号分隔
		apiKeys := strings.Split(apiKeysItem.Value, ",")
		valid := false
		for _, key := range apiKeys {
			if strings.TrimSpace(key) == apiKey {
				valid = true
				break
			}
		}

		if !valid {
			common.ErrorStrResp(c, "Invalid API Key", 401)
			c.Abort()
			return
		}

		// 频率限制
		if !searchShareLimiter.allow(apiKey) {
			common.ErrorStrResp(c, "Rate limit exceeded: 20 requests per minute", 429)
			c.Abort()
			return
		}

		c.Next()
	}
}

// SearchAndShare 搜索关键词并返回分享链接
// POST /api/public/search_and_share
func SearchAndShare(c *gin.Context) {
	var req SearchAndShareReq
	if err := c.ShouldBindJSON(&req); err != nil {
		common.ErrorResp(c, err, 400)
		return
	}

	if req.Period == 0 {
		req.Period = 7
	}

	// 搜索文件
	searchReq := model.SearchReq{
		Parent:   "/",
		Keywords: req.Keyword,
		Scope:    0,
		PageReq: model.PageReq{
			Page:    1,
			PerPage: 100,
		},
	}

	nodes, total, err := search.Search(context.Background(), searchReq)
	if err != nil {
		common.ErrorStrResp(c, fmt.Sprintf("搜索失败: %v", err), 500)
		return
	}

	if len(nodes) == 0 {
		common.ErrorStrResp(c, "未找到匹配的文件", 404)
		return
	}

	// 智能排序：完全匹配 > 文件大小 > 相关度
	sort.Slice(nodes, func(i, j int) bool {
		// 完全匹配优先
		if nodes[i].Name == req.Keyword && nodes[j].Name != req.Keyword {
			return true
		}
		if nodes[i].Name != req.Keyword && nodes[j].Name == req.Keyword {
			return false
		}
		// 文件大小大的优先
		return nodes[i].Size > nodes[j].Size
	})

	// 选择指定索引的结果
	if req.Index >= len(nodes) {
		common.ErrorStrResp(c, fmt.Sprintf("索引超出范围，共 %d 个结果", len(nodes)), 400)
		return
	}
	selectedNode := nodes[req.Index]

	// 构造完整路径并生成分享链接
	fullPath := selectedNode.Parent + "/" + selectedNode.Name
	_, mountPrefix, baiduPath, err := resolveBaiduPathFromRequest(fullPath)
	if err != nil {
		common.ErrorStrResp(c, fmt.Sprintf("解析路径失败: %v", err), 400)
		return
	}

	// 生成分享链接
	cookie, err := getBaiduStorageCookie(mountPrefix)
	if err != nil {
		common.ErrorStrResp(c, fmt.Sprintf("获取存储配置失败: %v", err), 400)
		return
	}

	client := getBaiduClient(cookie)

	// 获取 UK
	uk := client.GetUK()

	link, pwd, err := client.CreateShareByPaths([]string{baiduPath}, req.Period, "")
	if err != nil {
		common.ErrorStrResp(c, fmt.Sprintf("生成分享链接失败: %v", err), 500)
		return
	}

	common.SuccessResp(c, SearchAndShareResp{
		Link:         link,
		Password:     pwd,
		FileName:     selectedNode.Name,
		FilePath:     fullPath,
		FileSize:     selectedNode.Size,
		TotalMatches: int(total),
		UK:           uk,
		FID:          selectedNode.FsID,
	})
}

// getBaiduClient 获取百度客户端
func getBaiduClient(cookie string) *baidu.Client {
	return baidu.NewClient(cookie)
}
