package spi_test

import (
	"context"
	"testing"

	spi "github.com/cyoda/cyoda-go-spi"
)

func TestExtendSchemaSignature(t *testing.T) {
	// Compile-time check: force the interface type against an anonymous struct
	// that implements ExtendSchema. If the interface drops or renames the method,
	// this won't compile.
	var _ spi.ModelStore = (*anonExtendSchemaImpl)(nil)
}

type anonExtendSchemaImpl struct{}

func (anonExtendSchemaImpl) Save(_ context.Context, _ *spi.ModelDescriptor) error {
	return nil
}
func (anonExtendSchemaImpl) Get(_ context.Context, _ spi.ModelRef) (*spi.ModelDescriptor, error) {
	return nil, nil
}
func (anonExtendSchemaImpl) GetAll(_ context.Context) ([]spi.ModelRef, error) { return nil, nil }
func (anonExtendSchemaImpl) Delete(_ context.Context, _ spi.ModelRef) error   { return nil }
func (anonExtendSchemaImpl) Lock(_ context.Context, _ spi.ModelRef) error     { return nil }
func (anonExtendSchemaImpl) Unlock(_ context.Context, _ spi.ModelRef) error   { return nil }
func (anonExtendSchemaImpl) IsLocked(_ context.Context, _ spi.ModelRef) (bool, error) {
	return false, nil
}
func (anonExtendSchemaImpl) SetChangeLevel(_ context.Context, _ spi.ModelRef, _ spi.ChangeLevel) error {
	return nil
}
func (anonExtendSchemaImpl) ExtendSchema(_ context.Context, _ spi.ModelRef, _ spi.SchemaDelta) error {
	return nil
}
