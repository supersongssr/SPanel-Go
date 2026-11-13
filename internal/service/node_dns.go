// 节点 DNS 记录删除 (对齐 PHP NodeApiService::deleteNodeDnsRecords)。
//
// 语义: CF API DELETE 远端记录 (404 容忍 = 远端已不在, 继续删本地行);
// CF 其他失败保留本地行待重试 (自然不计入 deleted);
// 无 cf_record_id 的行直接删本地。
package service

import (
	"log"

	"spanel-go/internal/model"
	"spanel-go/internal/pkg/database"
)

// DeleteNodeDNSRecords 删除节点全部 DNS 记录, 返回实际删除的本地行数
func DeleteNodeDNSRecords(nodeID int64) int {
	if database.DB == nil {
		return 0
	}
	var records []model.DnsRecord
	if err := database.DB.Where("node_id = ?", nodeID).Find(&records).Error; err != nil {
		log.Printf("[NodeDNS] load records failed node_id=%d err=%v", nodeID, err)
		return 0
	}

	deleted := 0
	for _, rec := range records {
		if rec.CFRecordID != "" {
			zoneID, zerr := ZoneIDForDomain(rec.RootDomain)
			if zerr == nil && zoneID != "" {
				if derr := CFDeleteRecord(zoneID, rec.CFRecordID, TokenForDomain(rec.RootDomain)); derr != nil && !IsCFNotFound(derr) {
					log.Printf("[NodeDNS] CF delete failed node=%d %s.%s: %v",
						nodeID, rec.Subdomain, rec.RootDomain, derr)
					continue // 保留本地记录待重试
				}
			} else if zerr != nil {
				log.Printf("[NodeDNS] zone resolve failed node=%d domain=%s: %v", nodeID, rec.RootDomain, zerr)
				continue
			}
			// CF 成功 / 404 / 无 zone → 删本地行
		}
		if err := database.DB.Delete(&model.DnsRecord{}, rec.ID).Error; err != nil {
			log.Printf("[NodeDNS] local delete failed node=%d rec=%d err=%v", nodeID, rec.ID, err)
			continue
		}
		deleted++
	}
	return deleted
}
