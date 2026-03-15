package rag

import (
	"context"
	"fmt"
	"os"
	"text/tabwriter"

	"github.com/liliang-cn/agent-go/pkg/rag/store"
	"github.com/spf13/cobra"
)

var collectionsCmd = &cobra.Command{
	Use:   "collections",
	Short: "List all collections in the vector store",
	Long:  `Display all collections that have been automatically created by LLM-based document classification.`,
	RunE: func(cmd *cobra.Command, args []string) error {
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

		ctx := context.Background()

		// Get all documents to extract unique collections
		documents, err := vectorStore.List(ctx)
		if err != nil {
			return fmt.Errorf("failed to list documents: %w", err)
		}

		// Extract unique collections
		collectionMap := make(map[string]int)
		for _, doc := range documents {
			if doc.Metadata != nil {
				if collection, ok := doc.Metadata["collection"].(string); ok && collection != "" {
					collectionMap[collection]++
				} else {
					collectionMap["default"]++
				}
			} else {
				collectionMap["default"]++
			}
		}

		if len(collectionMap) == 0 {
			fmt.Println("No collections found.")
			return nil
		}

		// Display collections
		w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		if _, err := fmt.Fprintf(w, "COLLECTION\tDOCUMENTS\tDESCRIPTION\n"); err != nil {
			return fmt.Errorf("failed to write header: %w", err)
		}
		if _, err := fmt.Fprintf(w, "----------\t---------\t-----------\n"); err != nil {
			return fmt.Errorf("failed to write separator: %w", err)
		}

		// Collection descriptions based on common patterns
		descriptions := map[string]string{
			"default":           "Uncategorized documents",
			"medical_records":   "Medical and healthcare documents",
			"meeting_notes":     "Meeting notes and agendas",
			"technical_docs":    "Technical documentation",
			"research_papers":   "Research papers and articles",
			"personal_notes":    "Personal notes and reminders",
			"project_docs":      "Project documentation",
			"legal_documents":   "Legal documents and contracts",
			"financial_reports": "Financial reports and invoices",
			"customer_feedback": "Customer feedback and reviews",
			"code_snippets":     "Code examples and snippets",
		}

		for collection, count := range collectionMap {
			description := descriptions[collection]
			if description == "" {
				description = "LLM-classified documents"
			}

			if _, err := fmt.Fprintf(w, "%s\t%d\t%s\n",
				collection,
				count,
				description,
			); err != nil {
				return fmt.Errorf("failed to write collection row: %w", err)
			}
		}

		if err := w.Flush(); err != nil {
			return fmt.Errorf("failed to flush output: %w", err)
		}

		fmt.Printf("\nTotal: %d collections\n", len(collectionMap))
		return nil
	},
}
