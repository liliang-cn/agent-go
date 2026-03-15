package rag

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/liliang-cn/agent-go/pkg/domain"
	"github.com/liliang-cn/agent-go/pkg/rag/store"
	"github.com/spf13/cobra"
)

// ExportData represents the structure of exported data
type ExportData struct {
	Metadata  ExportMetadata    `json:"metadata"`
	Documents []domain.Document `json:"documents"`
	Chunks    []domain.Chunk    `json:"chunks"`
}

type ExportMetadata struct {
	ExportTime    time.Time `json:"export_time"`
	Version       string    `json:"version"`
	DocumentCount int       `json:"document_count"`
	ChunkCount    int       `json:"chunk_count"`
	VectorDim     int       `json:"vector_dim"`
}

var (
	exportFormat   string
	includeVectors bool
)

var exportCmd = &cobra.Command{
	Use:   "export [output_file]",
	Short: "Export knowledge base data",
	Long:  `Export all documents and vector data to a backup file for later import.`,
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		outputPath := args[0]

		// Ensure output directory exists
		if err := os.MkdirAll(filepath.Dir(outputPath), 0755); err != nil {
			return fmt.Errorf("failed to create output directory: %w", err)
		}

		// Initialize stores
		vectorStore, err := store.NewSQLiteStore(
			Cfg.CortexDBPath(),
			"hnsw",
		)
		if err != nil {
			return fmt.Errorf("failed to create vector store: %w", err)
		}
		defer func() {
			if closeErr := vectorStore.Close(); closeErr != nil {
				fmt.Printf("Warning: failed to close vector store: %v\n", closeErr)
			}
		}()

		docStore := store.NewDocumentStore(vectorStore.GetCortexdbStore())

		ctx := context.Background()

		// Get all documents
		documents, err := docStore.List(ctx)
		if err != nil {
			return fmt.Errorf("failed to list documents: %w", err)
		}

		// Get all chunks for each document
		var allChunks []domain.Chunk
		for _, doc := range documents {
			chunks, err := getChunksForDocument(ctx, vectorStore, doc.ID)
			if err != nil {
				fmt.Printf("Warning: failed to get chunks for document %s: %v\n", doc.ID, err)
				continue
			}

			// Optionally exclude vectors to reduce file size
			if !includeVectors {
				for i := range chunks {
					chunks[i].Vector = nil
				}
			}

			allChunks = append(allChunks, chunks...)
		}

		// Create export data
		exportData := ExportData{
			Metadata: ExportMetadata{
				ExportTime:    time.Now(),
				Version:       Version,
				DocumentCount: len(documents),
				ChunkCount:    len(allChunks),
				VectorDim:     768, // Default vector dimension
			},
			Documents: documents,
			Chunks:    allChunks,
		}

		// Write to file
		file, err := os.Create(outputPath)
		if err != nil {
			return fmt.Errorf("failed to create output file: %w", err)
		}
		defer func() {
			if err := file.Close(); err != nil {
				fmt.Fprintf(os.Stderr, "Warning: failed to close file: %v\n", err)
			}
		}()

		encoder := json.NewEncoder(file)
		encoder.SetIndent("", "  ")

		if err := encoder.Encode(exportData); err != nil {
			return fmt.Errorf("failed to encode export data: %w", err)
		}

		// Print summary
		fileInfo, _ := file.Stat()
		fmt.Printf("Successfully exported knowledge base:\n")
		fmt.Printf("  Documents: %d\n", len(documents))
		fmt.Printf("  Chunks: %d\n", len(allChunks))
		fmt.Printf("  Output file: %s\n", outputPath)
		fmt.Printf("  File size: %s\n", formatFileSize(fileInfo.Size()))

		if !includeVectors {
			fmt.Printf("  Note: Vectors excluded (use --include-vectors to include)\n")
		}

		return nil
	},
}

// getChunksForDocument retrieves all chunks for a specific document
func getChunksForDocument(ctx context.Context, store *store.SQLiteStore, docID string) ([]domain.Chunk, error) {
	// Since we don't have a direct method to get chunks by document ID,
	// we'll use a dummy search to get all chunks and filter by document ID
	// This is not optimal but works with the current API
	dummyVector := make([]float64, 768) // Use default 768 dimension
	for i := range dummyVector {
		dummyVector[i] = 0.0
	}

	allChunks, err := store.Search(ctx, dummyVector, 10000) // Get many results
	if err != nil {
		return nil, err
	}

	// Filter chunks by document ID
	var documentChunks []domain.Chunk
	for _, chunk := range allChunks {
		if chunk.DocumentID == docID {
			documentChunks = append(documentChunks, chunk)
		}
	}

	return documentChunks, nil
}

// formatFileSize formats file size in human readable format
func formatFileSize(size int64) string {
	const unit = 1024
	if size < unit {
		return fmt.Sprintf("%d B", size)
	}
	div, exp := int64(unit), 0
	for n := size / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(size)/float64(div), "KMGTPE"[exp])
}

func init() {
	exportCmd.Flags().StringVar(&exportFormat, "format", "json", "Export format (json)")
	exportCmd.Flags().BoolVar(&includeVectors, "include-vectors", false, "Include vector embeddings in export")

	// Command registration is handled by GetCommands()
}
