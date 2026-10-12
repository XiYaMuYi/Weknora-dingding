package container

import (
	"context"
	"time"

	"github.com/Tencent/WeKnora/internal/productknowledge"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/neo4j/neo4j-go-driver/v6/neo4j"
	"gorm.io/gorm"
)

func newProductKnowledgeService(db *gorm.DB, driver neo4j.Driver) *productknowledge.Service {
	return productknowledge.NewService(productknowledge.NewSQLStore(db), productknowledge.NewSQLSourceReader(db), productknowledge.NewNeo4jGraph(driver))
}
func startProductKnowledgeWorker(service *productknowledge.Service, cleaner interfaces.ResourceCleaner) {
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); service.Run(ctx) }()
	cleaner.RegisterWithName("ProductKnowledgeProjection", func() error {
		cancel()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
		}
		return nil
	})
}

