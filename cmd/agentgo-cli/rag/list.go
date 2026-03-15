package rag

import (
	"context"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/liliang-cn/agent-go/pkg/rag/store"
	"github.com/spf13/cobra"
)

var (
	showMetadata bool
	showCompact  bool
)

var listCmd = &cobra.Command{
	Use:   "list",
	Short: "List indexed documents",
	Long:  `Display all documents imported into the vector database.`,
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
		documents, err := vectorStore.List(ctx)
		if err != nil {
			return fmt.Errorf("failed to list documents: %w", err)
		}

		if len(documents) == 0 {
			fmt.Println("No documents found in the knowledge base.")
			return nil
		}

		if showMetadata {
			// Detailed view with metadata
			for i, doc := range documents {
				if i > 0 {
					fmt.Println(strings.Repeat("-", 80))
				}

				fmt.Printf("ID: %s\n", doc.ID)
				if doc.Path != "" {
					fmt.Printf("Path: %s\n", doc.Path)
				}
				if doc.URL != "" {
					fmt.Printf("URL: %s\n", doc.URL)
				}
				fmt.Printf("Created: %s\n", doc.Created.Format("2006-01-02 15:04:05"))

				if doc.Metadata != nil && len(doc.Metadata) > 0 {
					fmt.Println("Metadata:")

					// Show summary if present
					if summary, ok := doc.Metadata["summary"].(string); ok && summary != "" {
						fmt.Printf("  Summary: %s\n", summary)
					}

					// Show keywords if present
					if keywords, ok := doc.Metadata["keywords"].([]interface{}); ok && len(keywords) > 0 {
						keywordStrs := make([]string, 0, len(keywords))
						for _, k := range keywords {
							if str, ok := k.(string); ok {
								keywordStrs = append(keywordStrs, str)
							}
						}
						if len(keywordStrs) > 0 {
							fmt.Printf("  Keywords: %s\n", strings.Join(keywordStrs, ", "))
						}
					} else if keywordStr, ok := doc.Metadata["keywords"].(string); ok && keywordStr != "" {
						// Check if it's a JSON array string like "[keyword1 keyword2]"
						if strings.HasPrefix(keywordStr, "[") && strings.HasSuffix(keywordStr, "]") {
							// Remove brackets and split by spaces
							keywordStr = strings.TrimPrefix(keywordStr, "[")
							keywordStr = strings.TrimSuffix(keywordStr, "]")
							keywords := strings.Fields(keywordStr)
							if len(keywords) > 0 {
								fmt.Printf("  Keywords: %s\n", strings.Join(keywords, ", "))
							}
						}
					}

					// Show document type if present
					if docType, ok := doc.Metadata["document_type"].(string); ok && docType != "" {
						fmt.Printf("  Type: %s\n", docType)
					}

					// Show temporal references if present
					if temporalRefs, ok := doc.Metadata["temporal_refs"].(map[string]interface{}); ok && len(temporalRefs) > 0 {
						fmt.Println("  Temporal References:")
						for term, date := range temporalRefs {
							fmt.Printf("    %s: %v\n", term, date)
						}
					}

					// Show entities if present
					if entities, ok := doc.Metadata["entities"].(map[string]interface{}); ok && len(entities) > 0 {
						fmt.Println("  Entities:")
						for category, items := range entities {
							if itemList, ok := items.([]interface{}); ok && len(itemList) > 0 {
								itemStrs := make([]string, 0, len(itemList))
								for _, item := range itemList {
									if str, ok := item.(string); ok {
										itemStrs = append(itemStrs, str)
									}
								}
								if len(itemStrs) > 0 {
									fmt.Printf("    %s: %s\n", category, strings.Join(itemStrs, ", "))
								}
							}
						}
					}

					// Show events if present
					if events, ok := doc.Metadata["events"].([]interface{}); ok && len(events) > 0 {
						eventStrs := make([]string, 0, len(events))
						for _, e := range events {
							if str, ok := e.(string); ok {
								eventStrs = append(eventStrs, str)
							}
						}
						if len(eventStrs) > 0 {
							fmt.Printf("  Events: %s\n", strings.Join(eventStrs, ", "))
						}
					}

					// Show other metadata
					for k, v := range doc.Metadata {
						if k != "summary" && k != "keywords" && k != "document_type" &&
							k != "temporal_refs" && k != "entities" && k != "events" &&
							k != "chunk_index" {
							fmt.Printf("  %s: %v\n", k, v)
						}
					}

					// Debug: show all metadata keys
					if Verbose {
						fmt.Println("  [Debug] All metadata keys and values:")
						for k, v := range doc.Metadata {
							fmt.Printf("    - %s: %v (type: %T)\n", k, v, v)
						}
					}
				}
			}
		} else {
			// Compact table view
			w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)

			// Adjust headers based on whether to show compact view
			if showCompact {
				if _, err := fmt.Fprintf(w, "ID\tSOURCE\tKEYWORDS\tCREATED\n"); err != nil {
					return fmt.Errorf("failed to write header: %w", err)
				}
				if _, err := fmt.Fprintf(w, "---\t------\t--------\t-------\n"); err != nil {
					return fmt.Errorf("failed to write separator: %w", err)
				}
			} else {
				if _, err := fmt.Fprintf(w, "ID\tPATH\tURL\tCREATED\n"); err != nil {
					return fmt.Errorf("failed to write header: %w", err)
				}
				if _, err := fmt.Fprintf(w, "---\t----\t---\t-------\n"); err != nil {
					return fmt.Errorf("failed to write separator: %w", err)
				}
			}

			for _, doc := range documents {
				if showCompact {
					// Show source and keywords in compact view
					source := "-"
					keywords := "-"

					if doc.Path != "" {
						source = doc.Path
					} else if doc.URL != "" {
						source = doc.URL
					} else if sourceVal, ok := doc.Metadata["source"].(string); ok {
						source = sourceVal
					}

					// Extract keywords from metadata
					if keywordList, ok := doc.Metadata["keywords"].([]interface{}); ok && len(keywordList) > 0 {
						keywordStrs := make([]string, 0, len(keywordList))
						for _, k := range keywordList {
							if str, ok := k.(string); ok {
								keywordStrs = append(keywordStrs, str)
							}
						}
						if len(keywordStrs) > 0 {
							keywords = strings.Join(keywordStrs, ", ")
						}
					} else if keywordStr, ok := doc.Metadata["keywords"].(string); ok && keywordStr != "" {
						// Check if it's a JSON array string like "[keyword1 keyword2]"
						if strings.HasPrefix(keywordStr, "[") && strings.HasSuffix(keywordStr, "]") {
							// Remove brackets and split by spaces
							keywordStr = strings.TrimPrefix(keywordStr, "[")
							keywordStr = strings.TrimSuffix(keywordStr, "]")
							keywordList := strings.Fields(keywordStr)
							if len(keywordList) > 0 {
								keywords = strings.Join(keywordList, ", ")
							}
						}
					}

					if _, err := fmt.Fprintf(w, "%s\t%s\t%s\t%s\n",
						truncateText(doc.ID, 36),
						truncateText(source, 30),
						truncateText(keywords, 40),
						doc.Created.Format("2006-01-02 15:04:05"),
					); err != nil {
						return fmt.Errorf("failed to write document row: %w", err)
					}
				} else {
					// Original view
					path := doc.Path
					if path == "" {
						path = "-"
					}

					url := doc.URL
					if url == "" {
						url = "-"
					}

					if _, err := fmt.Fprintf(w, "%s\t%s\t%s\t%s\n",
						truncateText(doc.ID, 36),
						truncateText(path, 50),
						truncateText(url, 50),
						doc.Created.Format("2006-01-02 15:04:05"),
					); err != nil {
						return fmt.Errorf("failed to write document row: %w", err)
					}
				}
			}

			if err := w.Flush(); err != nil {
				return fmt.Errorf("failed to flush output: %w", err)
			}
		}

		fmt.Printf("\nTotal: %d documents\n", len(documents))
		return nil
	},
}

func init() {
	listCmd.Flags().BoolVarP(&showMetadata, "metadata", "m", false, "show detailed metadata for each document")
	listCmd.Flags().BoolVarP(&showCompact, "compact", "c", false, "show compact view with source and keywords")
}
