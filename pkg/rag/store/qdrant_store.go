package store

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/liliang-cn/agent-go/v2/pkg/domain"
	pb "github.com/qdrant/go-client/qdrant"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

const (
	defaultTimeout    = 30 * time.Second
	defaultVectorSize = 768 // nomic-embed-text default
	defaultDistance   = pb.Distance_Cosine
	defaultCollection = "agentgo_documents"
)

type QdrantStore struct {
	client         pb.PointsClient
	collectionName string
	conn           *grpc.ClientConn
	vectorSize     uint64
}

func NewQdrantStore(url string, collection string) (*QdrantStore, error) {
	if collection == "" {
		collection = defaultCollection
	}

	// Parse URL to extract host:port
	url = strings.TrimPrefix(url, "http://")
	url = strings.TrimPrefix(url, "https://")

	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()

	// Connect to Qdrant
	conn, err := grpc.DialContext(ctx, url, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return nil, fmt.Errorf("failed to connect to Qdrant: %w", err)
	}

	pointsClient := pb.NewPointsClient(conn)
	collectionsClient := pb.NewCollectionsClient(conn)

	store := &QdrantStore{
		client:         pointsClient,
		collectionName: collection,
		conn:           conn,
		vectorSize:     defaultVectorSize,
	}

	// Check if collection exists, create if not
	if err := store.ensureCollection(ctx, collectionsClient); err != nil {
		conn.Close()
		return nil, err
	}

	return store, nil
}

func (s *QdrantStore) ensureCollectionWithSize(ctx context.Context, client pb.CollectionsClient, vectorSize uint64) error {
	// Check if collection exists
	listResp, err := client.List(ctx, &pb.ListCollectionsRequest{})
	if err != nil {
		return fmt.Errorf("failed to list collections: %w", err)
	}

	exists := false
	needsRecreate := false
	for _, col := range listResp.Collections {
		if col.Name == s.collectionName {
			exists = true
			// Get vector size from existing collection
			info, err := client.Get(ctx, &pb.GetCollectionInfoRequest{
				CollectionName: s.collectionName,
			})
			if err == nil && info.Result != nil && info.Result.Config != nil {
				// Extract vector size from the config params
				if info.Result.Config.Params != nil {
					if vectorParams := info.Result.Config.Params.GetVectorsConfig(); vectorParams != nil {
						if params := vectorParams.GetParams(); params != nil {
							if params.Size != vectorSize {
								log.Printf("Collection exists with wrong size %d, need %d. Recreating...", params.Size, vectorSize)
								needsRecreate = true
							}
							s.vectorSize = params.Size
						}
					}
				}
			}
			break
		}
	}

	// Recreate collection if needed
	if needsRecreate {
		// Delete existing collection
		_, err := client.Delete(ctx, &pb.DeleteCollection{
			CollectionName: s.collectionName,
		})
		if err != nil {
			return fmt.Errorf("failed to delete collection for recreation: %w", err)
		}
		exists = false
		log.Printf("Deleted collection %s for recreation with new vector size", s.collectionName)
	}

	if !exists {
		// Create collection with correct size
		_, err := client.Create(ctx, &pb.CreateCollection{
			CollectionName: s.collectionName,
			VectorsConfig: &pb.VectorsConfig{
				Config: &pb.VectorsConfig_Params{
					Params: &pb.VectorParams{
						Size:     vectorSize,
						Distance: defaultDistance,
					},
				},
			},
		})
		if err != nil {
			return fmt.Errorf("failed to create collection: %w", err)
		}
		s.vectorSize = vectorSize
		log.Printf("Created Qdrant collection: %s with vector size: %d", s.collectionName, vectorSize)
	}

	return nil
}

func (s *QdrantStore) ensureCollection(ctx context.Context, client pb.CollectionsClient) error {
	// Check if collection exists
	listResp, err := client.List(ctx, &pb.ListCollectionsRequest{})
	if err != nil {
		return fmt.Errorf("failed to list collections: %w", err)
	}

	exists := false
	for _, col := range listResp.Collections {
		if col.Name == s.collectionName {
			exists = true
			// Get vector size from existing collection
			info, err := client.Get(ctx, &pb.GetCollectionInfoRequest{
				CollectionName: s.collectionName,
			})
			if err == nil && info.Result != nil && info.Result.Config != nil {
				// Extract vector size from the config params
				if info.Result.Config.Params != nil {
					if vectorParams := info.Result.Config.Params.GetVectorsConfig(); vectorParams != nil {
						if params := vectorParams.GetParams(); params != nil {
							s.vectorSize = params.Size
						}
					}
				}
			}
			break
		}
	}

	if !exists {
		// Create collection
		_, err := client.Create(ctx, &pb.CreateCollection{
			CollectionName: s.collectionName,
			VectorsConfig: &pb.VectorsConfig{
				Config: &pb.VectorsConfig_Params{
					Params: &pb.VectorParams{
						Size:     s.vectorSize,
						Distance: defaultDistance,
					},
				},
			},
		})
		if err != nil {
			return fmt.Errorf("failed to create collection: %w", err)
		}
		log.Printf("Created Qdrant collection: %s", s.collectionName)
	}

	return nil
}

func (s *QdrantStore) Store(ctx context.Context, chunks []domain.Chunk) error {
	if len(chunks) == 0 {
		return nil
	}

	// Check if we need to update vector size based on actual data
	if len(chunks) > 0 && len(chunks[0].Vector) > 0 {
		actualSize := uint64(len(chunks[0].Vector))
		if s.vectorSize != actualSize {
			log.Printf("Updating vector size from %d to %d based on actual embeddings", s.vectorSize, actualSize)
			s.vectorSize = actualSize

			// Recreate collection if size mismatch (for first time)
			collectionsClient := pb.NewCollectionsClient(s.conn)
			if err := s.ensureCollectionWithSize(ctx, collectionsClient, actualSize); err != nil {
				return fmt.Errorf("failed to ensure collection with correct size: %w", err)
			}
		}
	}

	points := make([]*pb.PointStruct, 0, len(chunks))

	for _, chunk := range chunks {
		// Generate UUID if not present or not a valid UUID
		chunkID := chunk.ID
		if chunkID == "" {
			chunkID = uuid.New().String()
		} else {
			// Check if it's a valid UUID, if not generate a new one
			if _, err := uuid.Parse(chunkID); err != nil {
				// Generate deterministic UUID from chunk ID for consistency
				chunkID = uuid.NewSHA1(uuid.NameSpaceOID, []byte(chunk.ID)).String()
			}
		}

		// Ensure embeddings are float32
		embeddings := make([]float32, len(chunk.Vector))
		for i, v := range chunk.Vector {
			embeddings[i] = float32(v)
		}

		// Create payload
		payload := map[string]*pb.Value{
			"content":  {Kind: &pb.Value_StringValue{StringValue: chunk.Content}},
			"doc_id":   {Kind: &pb.Value_StringValue{StringValue: chunk.DocumentID}},
			"chunk_id": {Kind: &pb.Value_StringValue{StringValue: chunk.ID}},
		}

		// Add metadata if present
		if chunk.Metadata != nil {
			for k, v := range chunk.Metadata {
				if strVal, ok := v.(string); ok {
					payload[k] = &pb.Value{Kind: &pb.Value_StringValue{StringValue: strVal}}
				}
			}
		}

		point := &pb.PointStruct{
			Id: &pb.PointId{
				PointIdOptions: &pb.PointId_Uuid{
					Uuid: chunkID,
				},
			},
			Vectors: &pb.Vectors{
				VectorsOptions: &pb.Vectors_Vector{
					Vector: &pb.Vector{
						Data: embeddings,
					},
				},
			},
			Payload: payload,
		}

		points = append(points, point)
	}

	// Upsert points
	_, err := s.client.Upsert(ctx, &pb.UpsertPoints{
		CollectionName: s.collectionName,
		Points:         points,
		Wait:           &waitTrue,
	})

	if err != nil {
		return fmt.Errorf("failed to upsert points: %w", err)
	}

	return nil
}

// Search performs vector similarity search
func (s *QdrantStore) Search(ctx context.Context, vector []float64, topK int) ([]domain.Chunk, error) {
	return s.SearchWithFilters(ctx, vector, topK, nil)
}

// SearchWithFilters performs vector similarity search with optional filters
func (s *QdrantStore) SearchWithFilters(ctx context.Context, vector []float64, topK int, filters map[string]interface{}) ([]domain.Chunk, error) {
	// Convert embeddings to float32
	queryVector := make([]float32, len(vector))
	for i, v := range vector {
		queryVector[i] = float32(v)
	}

	// Build filter if needed
	var filter *pb.Filter
	if filters != nil && len(filters) > 0 {
		conditions := make([]*pb.Condition, 0, len(filters))
		for k, v := range filters {
			if strVal, ok := v.(string); ok {
				conditions = append(conditions, &pb.Condition{
					ConditionOneOf: &pb.Condition_Field{
						Field: &pb.FieldCondition{
							Key: k,
							Match: &pb.Match{
								MatchValue: &pb.Match_Text{
									Text: strVal,
								},
							},
						},
					},
				})
			}
		}

		if len(conditions) > 0 {
			filter = &pb.Filter{
				Must: conditions,
			}
		}
	}

	// Perform search
	searchResp, err := s.client.Search(ctx, &pb.SearchPoints{
		CollectionName: s.collectionName,
		Vector:         queryVector,
		Filter:         filter,
		Limit:          uint64(topK),
		WithPayload: &pb.WithPayloadSelector{
			SelectorOptions: &pb.WithPayloadSelector_Enable{
				Enable: true,
			},
		},
	})

	if err != nil {
		return nil, fmt.Errorf("search failed: %w", err)
	}

	// Convert results
	results := make([]domain.Chunk, 0, len(searchResp.Result))
	for _, point := range searchResp.Result {

		chunk := domain.Chunk{
			ID:       point.Id.GetUuid(),
			Score:    float64(point.Score),
			Metadata: make(map[string]interface{}),
		}

		// Extract chunk from payload
		if payload := point.Payload; payload != nil {
			if v, ok := payload["content"]; ok {
				chunk.Content = v.GetStringValue()
			}
			if v, ok := payload["doc_id"]; ok {
				chunk.DocumentID = v.GetStringValue()
			}
			// Retrieve original chunk ID if available
			if v, ok := payload["chunk_id"]; ok {
				chunk.ID = v.GetStringValue()
			}

			// Add other metadata
			for k, v := range payload {
				if k != "content" && k != "doc_id" && k != "chunk_id" {
					chunk.Metadata[k] = v.GetStringValue()
				}
			}
		}

		results = append(results, chunk)
	}

	return results, nil
}

// Delete removes a document and all its chunks
func (s *QdrantStore) Delete(ctx context.Context, documentID string) error {
	return s.DeleteByFilter(ctx, map[string]interface{}{"doc_id": documentID})
}

// DeleteByIDs deletes specific points by their IDs
func (s *QdrantStore) DeleteByIDs(ctx context.Context, ids []string) error {
	if len(ids) == 0 {
		return nil
	}

	pointIds := make([]*pb.PointId, 0, len(ids))
	for _, id := range ids {
		pointIds = append(pointIds, &pb.PointId{
			PointIdOptions: &pb.PointId_Uuid{
				Uuid: id,
			},
		})
	}

	_, err := s.client.Delete(ctx, &pb.DeletePoints{
		CollectionName: s.collectionName,
		Points: &pb.PointsSelector{
			PointsSelectorOneOf: &pb.PointsSelector_Points{
				Points: &pb.PointsIdsList{
					Ids: pointIds,
				},
			},
		},
		Wait: &waitTrue,
	})

	if err != nil {
		return fmt.Errorf("failed to delete points: %w", err)
	}

	return nil
}

func (s *QdrantStore) DeleteByFilter(ctx context.Context, filter map[string]interface{}) error {
	if len(filter) == 0 {
		return fmt.Errorf("filter cannot be empty for safety")
	}

	// Build filter conditions
	conditions := make([]*pb.Condition, 0, len(filter))
	for k, v := range filter {
		if strVal, ok := v.(string); ok {
			conditions = append(conditions, &pb.Condition{
				ConditionOneOf: &pb.Condition_Field{
					Field: &pb.FieldCondition{
						Key: k,
						Match: &pb.Match{
							MatchValue: &pb.Match_Text{
								Text: strVal,
							},
						},
					},
				},
			})
		}
	}

	if len(conditions) == 0 {
		return fmt.Errorf("no valid filter conditions")
	}

	_, err := s.client.Delete(ctx, &pb.DeletePoints{
		CollectionName: s.collectionName,
		Points: &pb.PointsSelector{
			PointsSelectorOneOf: &pb.PointsSelector_Filter{
				Filter: &pb.Filter{
					Must: conditions,
				},
			},
		},
		Wait: &waitTrue,
	})

	if err != nil {
		return fmt.Errorf("failed to delete by filter: %w", err)
	}

	return nil
}

// List returns all documents (not directly supported by vector stores)
func (s *QdrantStore) List(ctx context.Context) ([]domain.Document, error) {
	// Vector stores typically don't store full documents
	// This would need a separate document store implementation
	return nil, fmt.Errorf("listing documents not supported by vector store")
}

// Reset clears all data from the collection
func (s *QdrantStore) Reset(ctx context.Context) error {
	// The most reliable way to clear all points in Qdrant is to recreate the collection
	collectionsClient := pb.NewCollectionsClient(s.conn)

	// Delete the collection
	_, err := collectionsClient.Delete(ctx, &pb.DeleteCollection{
		CollectionName: s.collectionName,
	})
	if err != nil {
		// If collection doesn't exist, that's fine
		log.Printf("Warning during reset (delete collection): %v", err)
	}

	// Recreate the collection with the same parameters
	_, err = collectionsClient.Create(ctx, &pb.CreateCollection{
		CollectionName: s.collectionName,
		VectorsConfig: &pb.VectorsConfig{
			Config: &pb.VectorsConfig_Params{
				Params: &pb.VectorParams{
					Size:     s.vectorSize,
					Distance: defaultDistance,
				},
			},
		},
	})
	if err != nil {
		return fmt.Errorf("failed to recreate collection during reset: %w", err)
	}

	log.Printf("Reset Qdrant collection: %s", s.collectionName)
	return nil
}

func (s *QdrantStore) Close() error {
	if s.conn != nil {
		return s.conn.Close()
	}
	return nil
}

// Helper variable for wait parameter
var waitTrue = true

func (s *QdrantStore) SearchWithReranker(ctx context.Context, vector []float64, queryText string, topK int, strategy string, boost float64) ([]domain.Chunk, error) {
	return nil, fmt.Errorf("reranking not supported in Qdrant backend yet")
}

func (s *QdrantStore) SearchWithDiversity(ctx context.Context, vector []float64, topK int, lambda float32) ([]domain.Chunk, error) {
	return nil, fmt.Errorf("diversity search not supported in Qdrant backend yet")
}

func (s *QdrantStore) GetGraphStore() domain.GraphStore {
	return nil // Graph not supported in Qdrant backend yet
}

func (s *QdrantStore) GetChatStore() domain.ChatStore {
	return nil // Chat not supported in Qdrant backend yet
}
