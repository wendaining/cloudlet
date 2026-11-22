// Package operations stores project-scoped records for deployment and runtime actions.
package operations

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var (
	ErrIdempotencyConflict = errors.New("幂等键已用于不同请求")
	ErrInvalidTransition   = errors.New("操作状态转换无效")
	ErrStateConflict       = errors.New("操作阶段已变化或已进入终态")
)

type Registration struct {
	ProjectID          uuid.UUID
	Action             string
	IdempotencyKey     string
	RequestFingerprint string
}

type Operation struct {
	ID           uuid.UUID `gorm:"primaryKey"`
	Registration `gorm:"embedded"`
	Progress     `gorm:"embedded"`
	CreatedAt    time.Time `gorm:"autoCreateTime:false;default:(-)"`
	UpdatedAt    time.Time `gorm:"autoUpdateTime:false;default:(-)"`
}

type Store struct {
	db *gorm.DB
}

func NewStore(db *gorm.DB) (*Store, error) {
	if db == nil {
		return nil, errors.New("操作存储需要 PostgreSQL 连接")
	}
	return &Store{db: db}, nil
}

// Register returns whether this call created the operation. Callers can use
// that result to avoid launching the same action twice.
func (s *Store) Register(ctx context.Context, request Registration) (Operation, bool, error) {
	if err := request.validate(); err != nil {
		return Operation{}, false, err
	}
	op := Operation{
		ID:           uuid.New(),
		Registration: request,
		Progress:     Progress{Phase: Received, Outcome: InProgress},
	}
	result := s.db.WithContext(ctx).Clauses(
		clause.OnConflict{
			Columns:   []clause.Column{{Name: "project_id"}, {Name: "action"}, {Name: "idempotency_key"}},
			DoNothing: true,
		},
		clause.Returning{},
	).Create(&op)
	if result.Error != nil {
		return Operation{}, false, fmt.Errorf("登记项目 %s 的操作 %s: %w", request.ProjectID, op.ID, result.Error)
	}
	if result.RowsAffected == 1 {
		return op, true, nil
	}

	// A concurrent insert can win after PostgreSQL takes the INSERT snapshot.
	// A separate SELECT sees the committed winner without rewriting its row.
	var existing Operation
	err := s.db.WithContext(ctx).
		Where("project_id = ? AND action = ? AND idempotency_key = ?", request.ProjectID, request.Action, request.IdempotencyKey).
		Take(&existing).Error
	if err != nil {
		return Operation{}, false, fmt.Errorf("读取项目 %s 的幂等操作: %w", request.ProjectID, err)
	}
	if existing.RequestFingerprint != request.RequestFingerprint {
		return Operation{}, false, fmt.Errorf("登记项目 %s 的操作 %s: %w", request.ProjectID, existing.ID, ErrIdempotencyConflict)
	}
	return existing, false, nil
}

func (s *Store) Get(ctx context.Context, projectID, id uuid.UUID) (Operation, error) {
	if projectID == uuid.Nil || id == uuid.Nil {
		return Operation{}, errors.New("查询操作需要项目标识和操作标识")
	}
	var op Operation
	if err := s.db.WithContext(ctx).Where("project_id = ? AND id = ?", projectID, id).Take(&op).Error; err != nil {
		return Operation{}, fmt.Errorf("读取项目 %s 的操作 %s: %w", projectID, id, err)
	}
	return op, nil
}

// Advance updates an in-progress operation only if it is still at the expected phase.
func (s *Store) Advance(ctx context.Context, projectID, id uuid.UUID, expected Phase, next Progress) (Operation, error) {
	if projectID == uuid.Nil || id == uuid.Nil {
		return Operation{}, errors.New("推进操作需要项目标识和操作标识")
	}
	if err := next.validateFrom(expected); err != nil {
		return Operation{}, fmt.Errorf("推进项目 %s 的操作 %s: %w", projectID, id, err)
	}
	var op Operation
	result := s.db.WithContext(ctx).Model(&op).Clauses(clause.Returning{}).
		Where("project_id = ? AND id = ? AND phase = ? AND outcome = ?", projectID, id, expected, InProgress).
		Updates(map[string]any{"phase": next.Phase, "outcome": next.Outcome, "updated_at": gorm.Expr("clock_timestamp()")})
	if result.Error != nil {
		return Operation{}, fmt.Errorf("推进项目 %s 的操作 %s: %w", projectID, id, result.Error)
	}
	if result.RowsAffected == 1 {
		return op, nil
	}
	if _, err := s.Get(ctx, projectID, id); err != nil {
		return Operation{}, err
	}
	return Operation{}, fmt.Errorf("推进项目 %s 的操作 %s: %w", projectID, id, ErrStateConflict)
}

func (r Registration) validate() error {
	if r.ProjectID == uuid.Nil {
		return errors.New("登记操作需要项目标识")
	}
	if len(r.Action) > 128 || !validText(r.Action) {
		return errors.New("操作动作需要有效文本，最多 128 字节")
	}
	if len(r.IdempotencyKey) > 256 || !validText(r.IdempotencyKey) {
		return errors.New("幂等键需要有效文本，最多 256 字节")
	}
	if !validText(r.RequestFingerprint) {
		return errors.New("请求指纹需要有效文本")
	}
	return nil
}

func validText(value string) bool {
	return strings.TrimSpace(value) != "" && utf8.ValidString(value) && !strings.ContainsRune(value, 0)
}
