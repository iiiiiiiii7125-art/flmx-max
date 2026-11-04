package repo

import (
	"sort"

	"go-backend/internal/store/model"
)

// ListExpiredPeerShareIDs 返回已到期（expiry_time 穿越）且仍有
// 未拆除存量状态的 share id 集（R1-FED-02）：到期前控制面只拒新
// 分配，存量数据面监听的拆除原本只靠节点重连对账或有未完成操
// 作的 tick 触发——节点全程在线且状态全 applied 时可无限期越
// 界存活。本普查供维护 tick 主动回收：仍计入「有活」的状态是
// status=1 且未进入释放流程的 runtime（release_pending 的由
// pending 重试面收口）、以及未完全删除落定的 resource（与
// releasePeerShareResources 的跳过口径同形：deleted＋applied
// 且无 legacy 残留才算拆完）。
func (r *Repository) ListExpiredPeerShareIDs(now int64) ([]int64, error) {
	var ids []int64
	err := r.db.Model(&model.PeerShare{}).
		Where("is_active = 1 AND expiry_time > 0 AND expiry_time <= ?", now).
		Where("(EXISTS (SELECT 1 FROM peer_share_runtime WHERE peer_share_runtime.share_id = peer_share.id AND peer_share_runtime.status = 1 AND peer_share_runtime.release_pending = 0)"+
			" OR EXISTS (SELECT 1 FROM peer_share_resource WHERE peer_share_resource.share_id = peer_share.id AND NOT (peer_share_resource.desired_state = 'deleted' AND peer_share_resource.applied = 1 AND peer_share_resource.legacy_service_base = '')))").
		Pluck("id", &ids).Error
	if err != nil {
		return nil, err
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	return ids, nil
}

// Only retry unfinished operations. Successful desired state must be replayed
// on a real reconnect, not every maintenance tick while the agent stays online.
func (r *Repository) ListPendingPeerShareNodeIDs() ([]int64, error) {
	var resourceNodes, runtimeNodes []int64
	if err := r.db.Model(&model.PeerShareResource{}).Where("applied = 0").Distinct("node_id").Pluck("node_id", &resourceNodes).Error; err != nil {
		return nil, err
	}
	if err := r.db.Model(&model.PeerShareRuntime{}).
		Where("status = 1 AND (release_pending <> 0 OR (applied = 0 AND service_name <> '' AND role IN ?))", []string{"middle", "exit"}).
		Distinct("node_id").Pluck("node_id", &runtimeNodes).Error; err != nil {
		return nil, err
	}
	seen := make(map[int64]struct{})
	for _, ids := range [][]int64{resourceNodes, runtimeNodes} {
		for _, id := range ids {
			if id > 0 {
				seen[id] = struct{}{}
			}
		}
	}
	out := make([]int64, 0, len(seen))
	for id := range seen {
		out = append(out, id)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out, nil
}
