package meilisearch

import (
	"errors"
	"fmt"
	"github.com/alist-org/alist/v3/internal/conf"
	"github.com/alist-org/alist/v3/internal/model"
	"github.com/alist-org/alist/v3/internal/op"
	"github.com/alist-org/alist/v3/internal/search/searcher"
	"github.com/alist-org/alist/v3/internal/setting"
	"github.com/alist-org/alist/v3/pkg/utils"
	"github.com/meilisearch/meilisearch-go"
)

var config = searcher.Config{
	Name:       "meilisearch",
	AutoUpdate: true,
}

func newMeilisearch() (searcher.Searcher, error) {
	host := setting.GetStr(conf.MeilisearchHost)
	if host == "" {
		host = conf.Conf.Meilisearch.Host
	}
	apiKey := setting.GetStr(conf.MeilisearchApiKey)
	if apiKey == "" {
		apiKey = conf.Conf.Meilisearch.APIKey
	}
	prefix := setting.GetStr(conf.MeilisearchPrefix)
	if prefix == "" {
		prefix = conf.Conf.Meilisearch.IndexPrefix
	}

	m := Meilisearch{
		Client: meilisearch.NewClient(meilisearch.ClientConfig{
			Host:   host,
			APIKey: apiKey,
		}),
		IndexUid:             prefix + "alist",
		FilterableAttributes: []string{"parent", "is_dir", "name"},
		SearchableAttributes: []string{"name"},
	}

	_, err := m.Client.GetIndex(m.IndexUid)
	if err != nil {
		var mErr *meilisearch.Error
		ok := errors.As(err, &mErr)
		if ok && mErr.MeilisearchApiError.Code == "index_not_found" {
			task, err := m.Client.CreateIndex(&meilisearch.IndexConfig{
				Uid:        m.IndexUid,
				PrimaryKey: "id",
			})
			if err != nil {
				return nil, err
			}
			forTask, err := m.Client.WaitForTask(task.TaskUID)
			if err != nil {
				return nil, err
			}
			if forTask.Status != meilisearch.TaskStatusSucceeded {
				return nil, fmt.Errorf("index creation failed, task status is %s", forTask.Status)
			}
		} else {
			return nil, err
		}
	}
	attributes, err := m.Client.Index(m.IndexUid).GetFilterableAttributes()
	if err != nil {
		return nil, err
	}
	if attributes == nil || !utils.SliceAllContains(*attributes, m.FilterableAttributes...) {
		_, err = m.Client.Index(m.IndexUid).UpdateFilterableAttributes(&m.FilterableAttributes)
		if err != nil {
			return nil, err
		}
	}

	attributes, err = m.Client.Index(m.IndexUid).GetSearchableAttributes()
	if err != nil {
		return nil, err
	}
	if attributes == nil || !utils.SliceAllContains(*attributes, m.SearchableAttributes...) {
		_, err = m.Client.Index(m.IndexUid).UpdateSearchableAttributes(&m.SearchableAttributes)
		if err != nil {
			return nil, err
		}
	}

	pagination, err := m.Client.Index(m.IndexUid).GetPagination()
	if err != nil {
		return nil, err
	}
	if pagination.MaxTotalHits != int64(model.MaxInt) {
		_, err := m.Client.Index(m.IndexUid).UpdatePagination(&meilisearch.Pagination{
			MaxTotalHits: int64(model.MaxInt),
		})
		if err != nil {
			return nil, err
		}
	}
	return &m, nil
}

func init() {
	searcher.RegisterSearcher(config, newMeilisearch)

	// 设置项变更时同步到 conf.Conf，下次重新初始化 meilisearch 时生效
	op.RegisterSettingItemHook(conf.MeilisearchHost, func(item *model.SettingItem) error {
		conf.Conf.Meilisearch.Host = item.Value
		return nil
	})
	op.RegisterSettingItemHook(conf.MeilisearchApiKey, func(item *model.SettingItem) error {
		conf.Conf.Meilisearch.APIKey = item.Value
		return nil
	})
	op.RegisterSettingItemHook(conf.MeilisearchPrefix, func(item *model.SettingItem) error {
		conf.Conf.Meilisearch.IndexPrefix = item.Value
		return nil
	})
}
