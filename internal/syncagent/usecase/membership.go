// Package usecase holds the sync-agent application workflows. It depends only
// on domain ports, never on the gossip driver directly.
package usecase

import "github.com/Vyzz1/go-velox.git/internal/syncagent/domain"

// MembershipUseCase exposes the cluster's membership view to the delivery layer.
type MembershipUseCase struct {
	cluster domain.Cluster // port
}

func NewMembership(c domain.Cluster) *MembershipUseCase {
	return &MembershipUseCase{cluster: c}
}

// List returns the current membership snapshot.
func (uc *MembershipUseCase) List() []domain.Member {
	return uc.cluster.Members()
}

// Local returns the node this agent runs on.
func (uc *MembershipUseCase) Local() domain.Member {
	return uc.cluster.Local()
}
