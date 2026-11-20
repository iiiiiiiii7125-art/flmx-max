package repo

import (
	"errors"

	"go-backend/internal/store/model"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// R18-F1-01 (D-3): per-node tunnel-TLS distribution state.
//
// node_tunnel_tls is derived state: the panel records which CA +
// node certificate it issued and whether the agent confirmed receipt
// (TunnelTLSIssue ack). It is the migration gate's input — the render
// only switches a tunnel segment to verified TLS when both endpoint
// nodes have Ready=1 here. Dropping the table loses only the cached
// readiness flags (nodes re-distribute on the next deploy); no user
// data lives in it.

// GetNodeTunnelTLS returns the distribution state for a node, or
// (nil, nil) when the node was never issued certificates.
func (r *Repository) GetNodeTunnelTLS(nodeID int64) (*model.NodeTunnelTLS, error) {
	if r == nil || r.db == nil {
		return nil, errors.New("repository not initialized")
	}
	if nodeID <= 0 {
		return nil, nil
	}
	var row model.NodeTunnelTLS
	if err := r.db.Where("node_id = ?", nodeID).First(&row).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, err
	}
	return &row, nil
}

// MarkNodeTunnelTLSReady upserts the distribution state after the
// agent acknowledged a TunnelTLSIssue with matching fingerprints.
// now is unix seconds.
func (r *Repository) MarkNodeTunnelTLSReady(nodeID int64, caSHA, certSHA, serverName string, now int64) error {
	if r == nil || r.db == nil {
		return errors.New("repository not initialized")
	}
	if nodeID <= 0 {
		return errors.New("invalid node id")
	}
	row := model.NodeTunnelTLS{
		NodeID:     nodeID,
		CASha256:   caSHA,
		CertSha256: certSHA,
		ServerName: serverName,
		Ready:      1,
		IssuedAt:   now,
		VerifiedAt: now,
	}
	return r.db.Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "node_id"}},
		DoUpdates: clause.Assignments(map[string]interface{}{
			"ca_sha256":   caSHA,
			"cert_sha256": certSHA,
			"server_name": serverName,
			"ready":       1,
			"issued_at":   now,
			"verified_at": now,
		}),
	}).Create(&row).Error
}

// ClearNodeTunnelTLSReady marks a node not-ready while keeping the
// last issued fingerprints for forensics (the next deploy
// re-distributes). It never deletes the row: a missing row and a
// not-ready row behave the same to the render gate, but the kept row
// still tells an operator what was issued last.
func (r *Repository) ClearNodeTunnelTLSReady(nodeID int64) error {
	if r == nil || r.db == nil {
		return errors.New("repository not initialized")
	}
	if nodeID <= 0 {
		return nil
	}
	return r.db.Model(&model.NodeTunnelTLS{}).Where("node_id = ?", nodeID).Update("ready", 0).Error
}

// ListNodeTunnelTLSReady returns the ready set for a batch of nodes
// (missing rows and Ready=0 both read as false). It is the render
// gate's single read per deploy: one indexed query per chunk, no
// per-node round trips. The id list is chunked (inListChunkSize):
// a single IN ? over an unbounded set would exceed SQLite's
// 32766-variable limit (R3 K8: a single entity's subset is not a
// bounded exemption).
func (r *Repository) ListNodeTunnelTLSReady(nodeIDs []int64) (map[int64]bool, error) {
	out := make(map[int64]bool, len(nodeIDs))
	if r == nil || r.db == nil {
		return out, errors.New("repository not initialized")
	}
	if len(nodeIDs) == 0 {
		return out, nil
	}
	var rows []model.NodeTunnelTLS
	for _, chunk := range chunkInt64IDs(nodeIDs) {
		var chunkRows []model.NodeTunnelTLS
		if err := r.db.Where("node_id IN ? AND ready = 1", chunk).Find(&chunkRows).Error; err != nil {
			return out, err
		}
		rows = append(rows, chunkRows...)
	}
	for _, row := range rows {
		out[row.NodeID] = true
	}
	return out, nil
}
