// Cloudflare DNS API 最小驱动 (对齐 PHP app/Utils/CloudflareDriver.php 中
// purge/注册/对账用到的能力: 删记录 / 建记录 / 查 zone / 列记录)。
//
// 语义口径:
//   - DeleteRecord: 远端 404 = 已不存在, 视为成功返回 nil (本地行可安全删除);
//     其他错误原样返回, 调用方保留本地行待重试;
//   - 所有请求 10s 超时; token 按域名解析 (DomainPool), 绝不落日志。
package service

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"
)

// CF 计划性错误
type CFError struct {
	Status  int    `json:"status"`
	Message string `json:"message"`
}

func (e *CFError) Error() string { return fmt.Sprintf("cloudflare api %d: %s", e.Status, e.Message) }

// IsCFNotFound 是否远端 404 / record not found
func IsCFNotFound(err error) bool {
	if err == nil {
		return false
	}
	if cf, ok := err.(*CFError); ok {
		return cf.Status == 404
	}
	msg := err.Error()
	return bytes.Contains([]byte(msg), []byte("404")) ||
		bytes.Contains([]byte(msg), []byte("not found")) ||
		bytes.Contains([]byte(msg), []byte("Not Found"))
}

var cfHTTP = &http.Client{Timeout: 10 * time.Second}

// CFDeleteRecord 删除 zone 下指定记录 (404 容忍)
func CFDeleteRecord(zoneID, recordID, apiToken string) error {
	return cfRequest("DELETE", "/zones/"+zoneID+"/dns_records/"+recordID, apiToken, nil, nil)
}

// CFRecord 建记录/列记录的公共结构
type CFRecord struct {
	ID      string `json:"id"`
	Type    string `json:"type"`
	Name    string `json:"name"`
	Content string `json:"content"`
	Proxied bool   `json:"proxied"`
	TTL     int    `json:"ttl"`
}

// CFCreateRecord 创建 DNS 记录, 返回新记录 ID
func CFCreateRecord(zoneID, apiToken string, rec CFRecord) (string, error) {
	body, _ := json.Marshal(map[string]any{
		"type":    rec.Type,
		"name":    rec.Name,
		"content": rec.Content,
		"proxied": rec.Proxied,
		"ttl":     rec.TTL,
	})
	var out struct {
		Result CFRecord `json:"result"`
	}
	if err := cfRequest("POST", "/zones/"+zoneID+"/dns_records", apiToken, body, &out); err != nil {
		return "", err
	}
	return out.Result.ID, nil
}

// CFListRecords 列 zone 下全部 DNS 记录 (每页 100, 翻页取全)
func CFListRecords(zoneID, apiToken string) ([]CFRecord, error) {
	var all []CFRecord
	page := 1
	for {
		var out struct {
			Result     []CFRecord `json:"result"`
			ResultInfo struct {
				Page       int `json:"page"`
				TotalPages int `json:"total_pages"`
			} `json:"result_info"`
		}
		path := fmt.Sprintf("/zones/%s/dns_records?per_page=100&page=%d", zoneID, page)
		if err := cfRequest("GET", path, apiToken, nil, &out); err != nil {
			return nil, err
		}
		all = append(all, out.Result...)
		if page >= out.ResultInfo.TotalPages || len(out.Result) == 0 {
			break
		}
		page++
	}
	return all, nil
}

// CFGetZoneIDByName 按域名查 zone id (zones?name=)
func CFGetZoneIDByName(domain, apiToken string) (string, error) {
	var out struct {
		Result []struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"result"`
	}
	path := "/zones?name=" + domain + "&status=active"
	if err := cfRequest("GET", path, apiToken, nil, &out); err != nil {
		return "", err
	}
	if len(out.Result) == 0 {
		return "", &CFError{Status: 404, Message: "zone not found for " + domain}
	}
	return out.Result[0].ID, nil
}

// zone 缓存 (域名 → zoneID; 内存缓存, 查询失败不缓存)
var (
	zoneCacheMu sync.Mutex
	zoneCache   = map[string]string{}
)

// CFZoneIDCached 带内存缓存的 zone 查询 (5 分钟 TTL 简化: 进程内缓存, 重启失效)
func CFZoneIDCached(domain, apiToken string) (string, error) {
	zoneCacheMu.Lock()
	if id, ok := zoneCache[domain]; ok {
		zoneCacheMu.Unlock()
		return id, nil
	}
	zoneCacheMu.Unlock()

	id, err := CFGetZoneIDByName(domain, apiToken)
	if err != nil {
		return "", err
	}
	zoneCacheMu.Lock()
	zoneCache[domain] = id
	zoneCacheMu.Unlock()
	return id, nil
}

// cfRequest 统一请求封装 (Bearer token, JSON 编解码)
func cfRequest(method, path, apiToken string, body []byte, out any) error {
	url := "https://api.cloudflare.com/client/v4" + path
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequest(method, url, reader)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+apiToken)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := cfHTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return err
	}

	var envelope struct {
		Success bool      `json:"success"`
		Errors  []CFError `json:"errors"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		// 非 JSON 响应 (如网关错误页): 用 HTTP 状态构造错误
		return &CFError{Status: resp.StatusCode, Message: string(raw[:min(len(raw), 200)])}
	}
	if !envelope.Success {
		msg := "cloudflare api error"
		status := resp.StatusCode
		if len(envelope.Errors) > 0 {
			msg = envelope.Errors[0].Message
			if envelope.Errors[0].Status != 0 {
				status = envelope.Errors[0].Status
			}
		}
		return &CFError{Status: status, Message: msg}
	}
	if out != nil {
		_ = json.Unmarshal(raw, out)
	}
	return nil
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
