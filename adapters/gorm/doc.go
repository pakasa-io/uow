// Package gormadapter provides a first-party uow adapter for GORM.
//
// The adapter expects a registered *gorm.DB client and returns *gorm.DB as the
// current handle in both transactional and non-transactional flows.
//
// Nested savepoint support is opt-in because dialect behavior differs across
// databases. By default the adapter advertises only root transaction support.
package gormadapter
