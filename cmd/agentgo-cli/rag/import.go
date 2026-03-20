package rag

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"github.com/liliang-cn/agent-go/v2/pkg/domain"
	"github.com/liliang-cn/agent-go/v2/pkg/rag/store"
	"github.com/liliang-cn/agent-go/v2/pkg/services"
	"github.com/spf13/cobra"
)

var (
	overwrite        bool
	skipVectors      bool
	recomputeVectors bool
)

var importCmd = &cobra.Command{
	Use:   "import [input_file]",
	Short: "Import knowledge base data",
	Long:  `Import documents and vector data from a previously exported backup file.`,
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		inputPath := args[0]

		// Check if file exists
		if _, err := os.Stat(inputPath); os.IsNotExist(err) {
			return fmt.Errorf("input file does not exist: %s", inputPath)
		}

		// Read and parse export file
		file, err := os.Open(inputPath)
		if err != nil {
			return fmt.Errorf("failed to open input file: %w", err)
		}
		defer func() {
			if err := file.Close(); err != nil {
				fmt.Fprintf(os.Stderr, "Warning: failed to close file: %v\n", err)
			}
		}()

		var exportData ExportData
		decoder := json.NewDecoder(file)
		if err := decoder.Decode(&exportData); err != nil {
			return fmt.Errorf("failed to decode export data: %w", err)
		}

		// Validate export data
		if err := validateExportData(&exportData); err != nil {
			return fmt.Errorf("invalid export data: %w", err)
		}

		fmt.Printf("Import summary:\n")
		fmt.Printf("  Export version: %s\n", exportData.Metadata.Version)
		fmt.Printf("  Export time: %s\n", exportData.Metadata.ExportTime.Format("2006-01-02 15:04:05"))
		fmt.Printf("  Documents: %d\n", exportData.Metadata.DocumentCount)
		fmt.Printf("  Chunks: %d\n", exportData.Metadata.ChunkCount)
		fmt.Printf("  Vector dimension: %d\n", exportData.Metadata.VectorDim)

		// Skip vector dimension check since v0.7.0 auto-detects dimensions
		fmt.Printf("  Note: Using auto-detect dimensions (cortexdb v2.7.0+)\n")

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

		// Check for existing data
		existingDocs, err := docStore.List(ctx)
		if err != nil {
			return fmt.Errorf("failed to check existing documents: %w", err)
		}

		if len(existingDocs) > 0 && !overwrite {
			return fmt.Errorf("database contains %d documents (use --overwrite to replace)", len(existingDocs))
		}

		// Clear existing data if overwrite is enabled
		if overwrite && len(existingDocs) > 0 {
			fmt.Println("Clearing existing data...")
			if err := vectorStore.Reset(ctx); err != nil {
				return fmt.Errorf("failed to clear existing data: %w", err)
			}
		}

		// Initialize embedder if we need to recompute vectors
		var embedService domain.Embedder
		if recomputeVectors || (len(exportData.Chunks) > 0 && len(exportData.Chunks[0].Vector) == 0) {
			// Get global embedder service
			embedService, err = services.GetGlobalEmbeddingService(ctx)
			if err != nil {
				return fmt.Errorf("failed to get global embedder service: %w", err)
			}
			fmt.Println("Embedder initialized for vector computation...")
		}

		// Import documents
		fmt.Printf("Importing %d documents...\n", len(exportData.Documents))
		for i, doc := range exportData.Documents {
			if err := docStore.Store(ctx, doc); err != nil {
				return fmt.Errorf("failed to store document %d (%s): %w", i+1, doc.ID, err)
			}
		}

		// Import chunks
		if !skipVectors && len(exportData.Chunks) > 0 {
			fmt.Printf("Importing %d chunks...\n", len(exportData.Chunks))

			// Process chunks in batches
			batchSize := Cfg.Internal.Storage.BatchSize
			for i := 0; i < len(exportData.Chunks); i += batchSize {
				end := i + batchSize
				if end > len(exportData.Chunks) {
					end = len(exportData.Chunks)
				}

				batch := exportData.Chunks[i:end]

				// Recompute vectors if needed
				if embedService != nil {
					for j := range batch {
						if len(batch[j].Vector) == 0 || recomputeVectors {
							vector, err := embedService.Embed(ctx, batch[j].Content)
							if err != nil {
								fmt.Printf("Warning: failed to embed chunk %s: %v\n", batch[j].ID, err)
								continue
							}
							batch[j].Vector = vector
						}
					}
				}

				// Store batch
				if err := vectorStore.Store(ctx, batch); err != nil {
					return fmt.Errorf("failed to store chunk batch %d-%d: %w", i+1, end, err)
				}

				fmt.Printf("  Processed %d/%d chunks\n", end, len(exportData.Chunks))
			}
		}

		fmt.Printf("\nImport completed successfully!\n")
		fmt.Printf("  Documents imported: %d\n", len(exportData.Documents))
		if !skipVectors {
			fmt.Printf("  Chunks imported: %d\n", len(exportData.Chunks))
		} else {
			fmt.Printf("  Chunks skipped (vectors disabled)\n")
		}

		return nil
	},
}

// validateExportData validates the structure and content of export data
func validateExportData(data *ExportData) error {
	if data.Metadata.DocumentCount != len(data.Documents) {
		return fmt.Errorf("document count mismatch: metadata=%d, actual=%d",
			data.Metadata.DocumentCount, len(data.Documents))
	}

	if data.Metadata.ChunkCount != len(data.Chunks) {
		return fmt.Errorf("chunk count mismatch: metadata=%d, actual=%d",
			data.Metadata.ChunkCount, len(data.Chunks))
	}

	// Validate document IDs are unique
	docIDs := make(map[string]bool)
	for _, doc := range data.Documents {
		if doc.ID == "" {
			return fmt.Errorf("document with empty ID found")
		}
		if docIDs[doc.ID] {
			return fmt.Errorf("duplicate document ID: %s", doc.ID)
		}
		docIDs[doc.ID] = true
	}

	// Validate chunk IDs are unique and document references exist
	chunkIDs := make(map[string]bool)
	for _, chunk := range data.Chunks {
		if chunk.ID == "" {
			return fmt.Errorf("chunk with empty ID found")
		}
		if chunkIDs[chunk.ID] {
			return fmt.Errorf("duplicate chunk ID: %s", chunk.ID)
		}
		if chunk.DocumentID != "" && !docIDs[chunk.DocumentID] {
			return fmt.Errorf("chunk %s references non-existent document: %s", chunk.ID, chunk.DocumentID)
		}
		chunkIDs[chunk.ID] = true
	}

	return nil
}

func init() {
	importCmd.Flags().BoolVar(&overwrite, "overwrite", false, "Overwrite existing data in database")
	importCmd.Flags().BoolVar(&skipVectors, "skip-vectors", false, "Skip importing vector data")
	importCmd.Flags().BoolVar(&recomputeVectors, "recompute-vectors", false, "Recompute all vector embeddings")

	// Command registration is handled by GetCommands()
}
