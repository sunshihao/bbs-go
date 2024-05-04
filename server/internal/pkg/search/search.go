package search

import (
	"bbs-go/internal/cache"
	"bbs-go/internal/models"
	html2 "bbs-go/internal/pkg/html"
	"bbs-go/internal/pkg/markdown"
	"bbs-go/internal/repositories"
	"html"
	"log"
	"log/slog"
	"strings"
	"time"

	"github.com/blevesearch/bleve/v2"
	"github.com/blevesearch/bleve/v2/index/scorch"
	"github.com/mlogclub/simple/common/jsons"
	"github.com/mlogclub/simple/sqls"
)

var index bleve.Index

type TopicDocument struct {
	Id         int64    `json:"id"`
	NodeId     int64    `json:"nodeId"`
	UserId     int64    `json:"userId"`
	Nickname   string   `json:"nickname"`
	Title      string   `json:"title"`
	Content    string   `json:"content"`
	Tags       []string `json:"tags"`
	Recommend  bool     `json:"recommend"`
	Status     int      `json:"status"`
	CreateTime int64    `json:"createTime"`
}

func (t *TopicDocument) ToStr() string {
	str, err := jsons.ToStr(t)
	if err != nil {
		slog.Error(err.Error(), slog.Any("err", err))
	}
	return str
}

func Init(indexPath string) {
	var err error
	index, err = bleve.Open(indexPath)
	if err != nil {
		textField := bleve.NewTextFieldMapping()
		textField.Store = true
		textField.Index = true
		textField.IncludeTermVectors = true
		textField.Analyzer = "en"

		numField := bleve.NewNumericFieldMapping()
		numField.DocValues = true
		numField.Store = true
		numField.Index = true

		boolField := bleve.NewBooleanFieldMapping()
		boolField.DocValues = true
		boolField.Store = true
		boolField.Index = true

		indexMapping := bleve.NewIndexMapping()
		indexMapping.DefaultMapping.AddFieldMappingsAt("id", numField)
		indexMapping.DefaultMapping.AddFieldMappingsAt("nodeId", numField)
		indexMapping.DefaultMapping.AddFieldMappingsAt("userId", numField)
		indexMapping.DefaultMapping.AddFieldMappingsAt("nickname", textField)
		indexMapping.DefaultMapping.AddFieldMappingsAt("title", textField)
		indexMapping.DefaultMapping.AddFieldMappingsAt("content", textField)
		// TODO tags
		indexMapping.DefaultMapping.AddFieldMappingsAt("recommend", boolField)
		indexMapping.DefaultMapping.AddFieldMappingsAt("status", numField)
		indexMapping.DefaultMapping.AddFieldMappingsAt("createTime", numField)

		// 使用 scorch 索引类型创建索引
		index, err = bleve.NewUsing(indexPath, indexMapping, scorch.Name, scorch.Name, nil)
		if err != nil {
			log.Fatalf("创建索引失败: %v", err)
		}
	}
}

func NewTopicDoc(topic *models.Topic) *TopicDocument {
	if topic == nil {
		return nil
	}
	doc := &TopicDocument{
		Id:         topic.Id,
		NodeId:     topic.NodeId,
		UserId:     topic.UserId,
		Title:      topic.Title,
		Status:     topic.Status,
		Recommend:  topic.Recommend,
		CreateTime: topic.CreateTime,
	}

	// 处理内容
	content := markdown.ToHTML(topic.Content)
	content = html2.GetHtmlText(content)
	content = html.EscapeString(content)

	doc.Content = content

	// 处理用户
	user := cache.UserCache.Get(topic.UserId)
	if user != nil {
		doc.Nickname = user.Nickname
	}

	// 处理标签
	tags := getTopicTags(topic.Id)
	var tagsArr []string
	for _, tag := range tags {
		tagsArr = append(tagsArr, tag.Name)
	}
	doc.Tags = tagsArr

	return doc
}

func getTopicTags(topicId int64) []models.Tag {
	topicTags := repositories.TopicTagRepository.Find(sqls.DB(), sqls.NewCnd().Where("topic_id = ?", topicId))

	var tagIds []int64
	for _, topicTag := range topicTags {
		tagIds = append(tagIds, topicTag.TagId)
	}
	return cache.TagCache.GetList(tagIds)
}

// IndexData 索引数据
func IndexData(did string, id, userId, createTime int64, context string, title string) error {
	content := markdown.ToHTML(context)
	content = html2.GetHtmlText(content)
	content = html.EscapeString(content)
	return updateData(did, map[string]interface{}{
		"id":         id,
		"userId":     userId,
		"content":    content,
		"createTime": createTime,
		"title":      title,
	})
}

// 删除索引
func DeleteData(did string) error {
	return updateData(did, nil)
}

// 分页查询
func SearchPage(queryText string, timeRange, page, limit int) (docs []TopicDocument, paging *sqls.Paging, err error) {
	paging = &sqls.Paging{Page: page, Limit: limit}
	boolQuery := bleve.NewBooleanQuery()

	// 如果queryText不为空，则添加标题匹配子查询
	if queryText != "" {
		queryMatch := bleve.NewMatchQuery(queryText)
		queryMatch.SetField("title")
		boolQuery.AddMust(queryMatch)
	}

	// 如果timeRange不为空，则根据时间范围添加时间范围查询
	if timeRange != 0 {
		var startTime int64
		currentTime := time.Now().Unix()

		switch timeRange {
		case 1: // 一天内
			startTime = currentTime - 24*3600
		case 2: // 一周内
			startTime = currentTime - 7*24*3600
		case 3: // 一月内
			startTime = currentTime - 30*24*3600
		case 4: // 一年内
			startTime = currentTime - 365*24*3600
		default:
			// 其他情况不处理
		}

		// 添加时间范围查询
		start := new(float64)
		end := new(float64)

		*start = float64(startTime * 1000)
		*end = float64(currentTime * 1000)

		queryTimeRange := bleve.NewNumericRangeQuery(start, end)
		queryTimeRange.SetField("createTime")
		boolQuery.AddMust(queryTimeRange)
	}

	searchRequest := bleve.NewSearchRequest(boolQuery)
	searchRequest.SortBy([]string{"createTime"})
	searchRequest.Fields = []string{"did", "userId", "title", "content", "createTime"}
	// 设置分页参数
	searchRequest.From = (page - 1) * limit
	searchRequest.Size = limit

	results, err := index.Search(searchRequest)
	if err != nil {
		slog.Error("搜索失败:", slog.Any("err", err))
	}

	for _, hit := range results.Hits {
		var doc TopicDocument

		doc.Type = strings.Split(hit.ID, "-")[0]

		if title, ok := hit.Fields["title"].(string); ok {
			doc.Title = title
		}
		if content, ok := hit.Fields["content"].(string); ok {
			doc.Content = content
		}

		if userId, ok := hit.Fields["userId"].(float64); ok {
			doc.UserId = userId
		}

		if did, ok := hit.Fields["did"].(float64); ok {
			doc.Id = did
		}

		if createTime, ok := hit.Fields["createTime"].(float64); ok {
			doc.CreateTime = createTime
		}

		docs = append(docs, doc)
	}

	return
}

func updateData(docID string, newData interface{}) error {
	if err := index.Delete(docID); err != nil {
		slog.Error("删除索引失败～：", slog.Any("err", err))
		return err
	}

	if newData != nil {
		if err := index.Index(docID, newData); err != nil {
			slog.Error("重建索引失败～：", slog.Any("err", err))
			return err
		}
	}
	return nil
}
