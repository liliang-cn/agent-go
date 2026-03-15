package cache

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	cachepkg "github.com/liliang-cn/agent-go/pkg/cache"
	"github.com/liliang-cn/agent-go/pkg/config"
	"github.com/spf13/cobra"
)

var (
	sharedCfg     *config.Config
	sharedVerbose bool
)

// SetSharedVariables shares root configuration with cache subcommands.
func SetSharedVariables(cfg *config.Config, verbose bool) {
	sharedCfg = cfg
	sharedVerbose = verbose
}

// Cmd is the root cache command.
var Cmd = NewCommand()

// NewCommand creates the cache command tree.
func NewCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "cache",
		Short: "Inspect and manipulate transient caches",
		Long: `Inspect and manipulate AgentGo caches.

The cache command works with the configured cache backend and is useful for
verifying file-backed persistence across CLI invocations.`,
	}

	cmd.AddCommand(newStatusCommand())
	cmd.AddCommand(newPutCommand())
	cmd.AddCommand(newGetCommand())
	cmd.AddCommand(newDeleteCommand())
	cmd.AddCommand(newClearCommand())

	return cmd
}

func newStatusCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show cache backend configuration and namespace stats",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			manager, err := buildManager()
			if err != nil {
				return err
			}

			out := cmd.OutOrStdout()
			fmt.Fprintf(out, "Store: %s\n", sharedCfg.Cache.StoreType)
			fmt.Fprintf(out, "Path: %s\n", sharedCfg.Cache.Path)
			fmt.Fprintf(out, "Max size: %d\n", sharedCfg.Cache.MaxSize)

			stats := manager.GetStats()
			for _, namespace := range []string{"query", "vector", "llm", "chunk"} {
				namespaceStats, ok := stats[namespace]
				if !ok {
					fmt.Fprintf(out, "%s: disabled\n", namespace)
					continue
				}
				fmt.Fprintf(out, "%s: size=%d hits=%d misses=%d evictions=%d\n",
					namespace,
					namespaceStats.Size,
					namespaceStats.Hits,
					namespaceStats.Misses,
					namespaceStats.Evictions,
				)
			}

			if sharedVerbose {
				fmt.Fprintf(out, "TTLs: default=%s\n",
					time.Hour,
				)
			}
			return nil
		},
	}
}

func newPutCommand() *cobra.Command {
	var ttlRaw string

	cmd := &cobra.Command{
		Use:   "put <namespace> <key> <value>",
		Short: "Store a string value in a cache namespace",
		Args:  cobra.ExactArgs(3),
		RunE: func(cmd *cobra.Command, args []string) error {
			manager, err := buildManager()
			if err != nil {
				return err
			}

			namespace := args[0]
			backend, err := namespaceCache(manager, namespace)
			if err != nil {
				return err
			}

			ttl, err := parseTTL(ttlRaw)
			if err != nil {
				return err
			}

			if err := backend.Set(cmd.Context(), args[1], args[2], ttl); err != nil {
				return fmt.Errorf("failed to write cache entry: %w", err)
			}

			fmt.Fprintf(cmd.OutOrStdout(), "stored namespace=%s key=%s\n", namespace, args[1])
			return nil
		},
	}

	cmd.Flags().StringVar(&ttlRaw, "ttl", "", "Optional TTL duration such as 30s, 5m, or 1h")
	return cmd
}

func newGetCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "get <namespace> <key>",
		Short: "Read a cache value from a namespace",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			manager, err := buildManager()
			if err != nil {
				return err
			}

			backend, err := namespaceCache(manager, args[0])
			if err != nil {
				return err
			}

			value, ok := backend.Get(cmd.Context(), args[1])
			if !ok {
				return fmt.Errorf("cache miss for namespace=%s key=%s", args[0], args[1])
			}

			switch typed := value.(type) {
			case string:
				fmt.Fprintln(cmd.OutOrStdout(), typed)
			default:
				data, err := json.MarshalIndent(typed, "", "  ")
				if err != nil {
					return fmt.Errorf("failed to marshal cache value: %w", err)
				}
				fmt.Fprintln(cmd.OutOrStdout(), string(data))
			}
			return nil
		},
	}
}

func newDeleteCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "delete <namespace> <key>",
		Short: "Delete a cache value from a namespace",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			manager, err := buildManager()
			if err != nil {
				return err
			}

			backend, err := namespaceCache(manager, args[0])
			if err != nil {
				return err
			}

			if err := backend.Delete(cmd.Context(), args[1]); err != nil {
				return fmt.Errorf("failed to delete cache entry: %w", err)
			}

			fmt.Fprintf(cmd.OutOrStdout(), "deleted namespace=%s key=%s\n", args[0], args[1])
			return nil
		},
	}
}

func newClearCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "clear [namespace]",
		Short: "Clear one namespace or all namespaces",
		Args:  cobra.RangeArgs(0, 1),
		RunE: func(cmd *cobra.Command, args []string) error {
			manager, err := buildManager()
			if err != nil {
				return err
			}

			if len(args) == 0 {
				if err := manager.ClearAll(context.Background()); err != nil {
					return fmt.Errorf("failed to clear caches: %w", err)
				}
				fmt.Fprintln(cmd.OutOrStdout(), "cleared all namespaces")
				return nil
			}

			backend, err := namespaceCache(manager, args[0])
			if err != nil {
				return err
			}
			if err := backend.Clear(context.Background()); err != nil {
				return fmt.Errorf("failed to clear namespace %s: %w", args[0], err)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "cleared namespace=%s\n", args[0])
			return nil
		},
	}
}

func buildManager() (*cachepkg.CacheManager, error) {
	if sharedCfg == nil {
		return nil, fmt.Errorf("cache command is not configured")
	}

	cacheCfg := cachepkg.DefaultCacheConfig()
	if sharedCfg.Cache.MaxSize > 0 {
		cacheCfg.MaxSize = sharedCfg.Cache.MaxSize
	}

	return cachepkg.NewCacheManagerWithStore(
		sharedCfg.Cache.StoreType,
		sharedCfg.Cache.Path,
		cacheCfg,
	)
}

func namespaceCache(manager *cachepkg.CacheManager, namespace string) (cachepkg.Cache, error) {
	backend := manager.NamespaceCache(namespace)
	if backend == nil {
		return nil, fmt.Errorf("unknown or disabled namespace: %s", namespace)
	}
	return backend, nil
}

func parseTTL(raw string) (time.Duration, error) {
	if strings.TrimSpace(raw) == "" {
		return 0, nil
	}

	ttl, err := time.ParseDuration(raw)
	if err != nil {
		return 0, fmt.Errorf("invalid ttl %q: %w", raw, err)
	}
	if ttl <= 0 {
		return 0, fmt.Errorf("ttl must be positive")
	}
	return ttl, nil
}
