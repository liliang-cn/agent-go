package store

import (
	"fmt"

	"github.com/liliang-cn/agent-go/pkg/domain"
)

// StoreConfig holds configuration for vector stores
type StoreConfig struct {
	Type       string                 `mapstructure:"type"`
	Parameters map[string]interface{} `mapstructure:"parameters"`
}

// NewVectorStore creates a vector store based on configuration
func NewVectorStore(config StoreConfig) (domain.VectorStore, error) {
	switch config.Type {
	case "sqlite", "cortexdb":
		var dbPath, indexType string
		if config.Parameters != nil {
			dbPath, _ = config.Parameters["db_path"].(string)
			indexType, _ = config.Parameters["index_type"].(string)
		}
		if dbPath == "" {
			dbPath = "./.agentgo/data/cortex.db"
		}
		return NewSQLiteStore(dbPath, indexType)

	case "qdrant":
		var url, collection string
		if config.Parameters != nil {
			url, _ = config.Parameters["url"].(string)
			collection, _ = config.Parameters["collection"].(string)
		}
		if url == "" {
			url = "localhost:6334" // Default Qdrant gRPC port
		}
		if collection == "" {
			collection = "agentgo_documents"
		}
		return NewQdrantStore(url, collection)

	// Future implementations can be added here:
	/*
		case "pinecone":
			apiKey := config.Parameters["api_key"].(string)
			environment := config.Parameters["environment"].(string)
			indexName := config.Parameters["index_name"].(string)
			return NewPineconeStore(apiKey, environment, indexName)

		case "pgvector":
			connString := config.Parameters["connection_string"].(string)
			tableName := config.Parameters["table_name"].(string)
			return NewPgVectorStore(connString, tableName)

		case "weaviate":
			url := config.Parameters["url"].(string)
			apiKey := config.Parameters["api_key"].(string)
			className := config.Parameters["class_name"].(string)
			return NewWeaviateStore(url, apiKey, className)

		case "milvus":
			host := config.Parameters["host"].(string)
			port := config.Parameters["port"].(int)
			collection := config.Parameters["collection"].(string)
			return NewMilvusStore(host, port, collection)

		case "chromadb":
			url := config.Parameters["url"].(string)
			collection := config.Parameters["collection"].(string)
			return NewChromaStore(url, collection)
	*/

	default:
		return nil, fmt.Errorf("unsupported vector store type: %s", config.Type)
	}
}

// NewDocumentStoreFor creates a document store compatible with the given vector store
func NewDocumentStoreFor(vectorStore domain.VectorStore) domain.DocumentStore {
	// For SQLite, we can reuse the same underlying store
	if sqliteStore, ok := vectorStore.(*SQLiteStore); ok {
		return NewDocumentStore(sqliteStore.GetCortexdbStore())
	}

	// For other stores, you might need different implementations
	// For example, store documents in a separate collection/index
	// or use a different storage backend entirely

	// Default: return a generic document store that uses the vector store
	// (you'd need to implement this)
	// return NewGenericDocumentStore(vectorStore)

	return nil
}
