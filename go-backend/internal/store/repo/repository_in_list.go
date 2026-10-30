package repo

// inListChunkSize 是「ID 集合 → 单个 IN ?」类查询的统一分块大小。
//
// 背景（T2-NEW-01，真机实证）：把无界 ID 切片整体塞进一个 IN ? 会按
// 元素个数占用 SQL 变量——SQLite 上限 32766、PostgreSQL 上限 65535
// （pgx 驱动参数上限），整表规模的用户/节点/隧道/转发 ID 集合必然
// 越界、查询整体失败：备份导出在约 3.3 万用户时返回 -2
// `too many SQL variables`，导入前自动快照同路失败只能告警跳过。
// 500 与既有先例同值（forwardIngressBatchChunk /
// flowUploadForwardMetaChunkSize 均为 500），远低于两个方言的上限，
// 同时把查询次数压到条数/500。凡参数来自整表集合、备份载荷或无条数
// 上限的请求载荷的 IN 查询都必须经 chunkInt64IDs 分块；参数有明确
// 上界（代码字面量、单实体子集合、调用方已分块）的点位不在此列，
// 清单见 fix-EXP.json 的 family_sweep。
const inListChunkSize = 500

// chunkInt64IDs 把 ids 切成每块至多 inListChunkSize 个的子片（不复制
// 元素，保持原序）；空输入返回 nil。
func chunkInt64IDs(ids []int64) [][]int64 {
	if len(ids) == 0 {
		return nil
	}
	chunks := make([][]int64, 0, (len(ids)+inListChunkSize-1)/inListChunkSize)
	for start := 0; start < len(ids); start += inListChunkSize {
		end := start + inListChunkSize
		if end > len(ids) {
			end = len(ids)
		}
		chunks = append(chunks, ids[start:end])
	}
	return chunks
}
