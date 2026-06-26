package usecase

import (
	"context"
	"errors"
	"testing"

	"go.uber.org/zap"

	"github.com/Vyzz1/go-velox.git/internal/configsvc/domain"
)

// fakeStore is an in-memory RuleStore double.
type fakeStore struct {
	upserted   []domain.Rule
	upsertErr  error
	deleteRet  bool
	deleteErr  error
	listAll    []domain.Rule
	listAllErr error
}

func (f *fakeStore) Upsert(_ context.Context, r domain.Rule) (domain.Rule, error) {
	if f.upsertErr != nil {
		return domain.Rule{}, f.upsertErr
	}
	f.upserted = append(f.upserted, r)
	return r, nil
}
func (f *fakeStore) Get(context.Context, string, string) (domain.Rule, error) {
	return domain.Rule{}, domain.ErrNotFound
}
func (f *fakeStore) ListByTenant(context.Context, string) ([]domain.Rule, error) {
	return nil, nil
}
func (f *fakeStore) ListAll(context.Context) ([]domain.Rule, error) {
	return f.listAll, f.listAllErr
}
func (f *fakeStore) Delete(context.Context, string, string) (bool, error) {
	return f.deleteRet, f.deleteErr
}

// fakePublisher is an in-memory RulePublisher double.
type fakePublisher struct {
	published []domain.Rule
	removed   []string
	pubErr    error
	remErr    error
}

func (f *fakePublisher) Publish(_ context.Context, r domain.Rule) error {
	if f.pubErr != nil {
		return f.pubErr
	}
	f.published = append(f.published, r)
	return nil
}
func (f *fakePublisher) Remove(_ context.Context, tenantID, ruleID string) error {
	if f.remErr != nil {
		return f.remErr
	}
	f.removed = append(f.removed, tenantID+"/"+ruleID)
	return nil
}

func validRule() domain.Rule {
	return domain.Rule{
		TenantID:   "acme",
		RuleID:     "api",
		Algorithm:  domain.AlgorithmGCRA,
		Limit:      100,
		PeriodSecs: 60,
	}
}

func newUC(s domain.RuleStore, p domain.RulePublisher) *RuleUseCase {
	return NewRule(s, p, zap.NewNop())
}

func TestUpsert_Validation(t *testing.T) {
	cases := []struct {
		name    string
		mutate  func(*domain.Rule)
		wantErr bool
	}{
		{"valid", func(*domain.Rule) {}, false},
		{"missing tenant", func(r *domain.Rule) { r.TenantID = "" }, true},
		{"missing rule", func(r *domain.Rule) { r.RuleID = "" }, true},
		{"empty algorithm", func(r *domain.Rule) { r.Algorithm = "" }, true},
		{"unknown algorithm", func(r *domain.Rule) { r.Algorithm = "bogus" }, true},
		{"zero limit", func(r *domain.Rule) { r.Limit = 0 }, true},
		{"zero period", func(r *domain.Rule) { r.PeriodSecs = 0 }, true},
		{"sliding window ok", func(r *domain.Rule) { r.Algorithm = domain.AlgorithmSlidingWindow }, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := validRule()
			tc.mutate(&r)
			uc := newUC(&fakeStore{}, &fakePublisher{})

			_, err := uc.Upsert(context.Background(), r)
			if tc.wantErr {
				var ve ValidationError
				if !errors.As(err, &ve) {
					t.Fatalf("err = %v, want ValidationError", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

func TestUpsert_PersistsThenPublishes(t *testing.T) {
	store := &fakeStore{}
	pub := &fakePublisher{}
	uc := newUC(store, pub)

	if _, err := uc.Upsert(context.Background(), validRule()); err != nil {
		t.Fatalf("Upsert error: %v", err)
	}
	if len(store.upserted) != 1 {
		t.Fatalf("store.upserted = %d, want 1", len(store.upserted))
	}
	if len(pub.published) != 1 {
		t.Fatalf("pub.published = %d, want 1", len(pub.published))
	}
}

func TestUpsert_PublishFailureStillDurable(t *testing.T) {
	store := &fakeStore{}
	pub := &fakePublisher{pubErr: errors.New("etcd down")}
	uc := newUC(store, pub)

	saved, err := uc.Upsert(context.Background(), validRule())
	if err == nil {
		t.Fatal("expected error when publish fails")
	}
	// The store write must have happened (durable) even though publish failed.
	if len(store.upserted) != 1 {
		t.Errorf("store.upserted = %d, want 1 (rule must be durable)", len(store.upserted))
	}
	if saved.RuleID != "api" {
		t.Errorf("saved rule not returned: %+v", saved)
	}
}

func TestUpsert_StoreFailureSkipsPublish(t *testing.T) {
	store := &fakeStore{upsertErr: errors.New("pg down")}
	pub := &fakePublisher{}
	uc := newUC(store, pub)

	if _, err := uc.Upsert(context.Background(), validRule()); err == nil {
		t.Fatal("expected store error")
	}
	if len(pub.published) != 0 {
		t.Errorf("publish called despite store failure: %d", len(pub.published))
	}
}

func TestDelete_Absent(t *testing.T) {
	store := &fakeStore{deleteRet: false}
	pub := &fakePublisher{}
	uc := newUC(store, pub)

	removed, err := uc.Delete(context.Background(), "acme", "api")
	if err != nil {
		t.Fatalf("Delete error: %v", err)
	}
	if removed {
		t.Error("removed = true, want false for absent rule")
	}
	if len(pub.removed) != 0 {
		t.Errorf("publisher.Remove called for absent rule: %v", pub.removed)
	}
}

func TestDelete_RemovesAndMirrors(t *testing.T) {
	store := &fakeStore{deleteRet: true}
	pub := &fakePublisher{}
	uc := newUC(store, pub)

	removed, err := uc.Delete(context.Background(), "acme", "api")
	if err != nil {
		t.Fatalf("Delete error: %v", err)
	}
	if !removed {
		t.Error("removed = false, want true")
	}
	if len(pub.removed) != 1 || pub.removed[0] != "acme/api" {
		t.Errorf("pub.removed = %v, want [acme/api]", pub.removed)
	}
}

func TestDelete_RemovePublishFailureSurfaced(t *testing.T) {
	store := &fakeStore{deleteRet: true}
	pub := &fakePublisher{remErr: errors.New("etcd down")}
	uc := newUC(store, pub)

	removed, err := uc.Delete(context.Background(), "acme", "api")
	if err == nil {
		t.Fatal("expected error when etcd remove fails")
	}
	if !removed {
		t.Error("removed = false, want true (row was deleted from store)")
	}
}

func TestReconcile_RepublishesAll(t *testing.T) {
	store := &fakeStore{listAll: []domain.Rule{validRule(), {
		TenantID:   "globex",
		RuleID:     "login",
		Algorithm:  domain.AlgorithmSlidingWindow,
		Limit:      10,
		PeriodSecs: 1,
	}}}
	pub := &fakePublisher{}
	uc := newUC(store, pub)

	if err := uc.Reconcile(context.Background()); err != nil {
		t.Fatalf("Reconcile error: %v", err)
	}
	if len(pub.published) != 2 {
		t.Errorf("pub.published = %d, want 2", len(pub.published))
	}
}
