package database

import (
	"context"
	"errors"

	"gorm.io/gorm"
)

// txKey is the context key for the transactional *gorm.DB.
type txKey struct{}

// TxManager provides transaction orchestration for services.
// Services call RunInTx to wrap multiple repo operations in a single transaction.
// Repos automatically participate via DB(ctx, fallback).
type TxManager interface {
	RunInTx(ctx context.Context, fn func(ctx context.Context) error) error
}

type gormTxManager struct {
	db *gorm.DB
}

func NewTxManager(db *gorm.DB) TxManager {
	return &gormTxManager{db: db}
}

func (m *gormTxManager) RunInTx(ctx context.Context, fn func(ctx context.Context) error) error {
	return m.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		txCtx := context.WithValue(ctx, txKey{}, tx)
		return fn(txCtx)
	})
}

// IsNotFound reports whether err is gorm.ErrRecordNotFound.
// Service-layer callers use this so they don't import gorm directly.
func IsNotFound(err error) bool {
	return errors.Is(err, gorm.ErrRecordNotFound)
}

// DB returns the transactional *gorm.DB from context if present,
// otherwise returns fallback.WithContext(ctx).
// Every repo method should call database.DB(ctx, r.db) instead of r.db.WithContext(ctx).
func DB(ctx context.Context, fallback *gorm.DB) *gorm.DB {
	if tx, ok := ctx.Value(txKey{}).(*gorm.DB); ok {
		return tx.WithContext(ctx)
	}
	return fallback.WithContext(ctx)
}
