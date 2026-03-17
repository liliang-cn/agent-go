package config

import (
	"fmt"
	"path/filepath"
	"strings"
)

type MemoryStoreType string

const (
	MemoryStoreTypeFile   MemoryStoreType = "file"
	MemoryStoreTypeVector MemoryStoreType = "vector"
	MemoryStoreTypeHybrid MemoryStoreType = "hybrid"
)

func NormalizeMemoryStoreType(raw string) MemoryStoreType {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", "file":
		return MemoryStoreTypeFile
	case "vector", "rag":
		return MemoryStoreTypeVector
	case "hybrid":
		return MemoryStoreTypeHybrid
	default:
		return MemoryStoreType(strings.ToLower(strings.TrimSpace(raw)))
	}
}

func ParseMemoryStoreType(raw string) (MemoryStoreType, error) {
	storeType := NormalizeMemoryStoreType(raw)
	if !storeType.Valid() {
		return "", fmt.Errorf("unsupported memory store type: %s", raw)
	}
	return storeType, nil
}

func (t MemoryStoreType) String() string {
	return string(t)
}

func (t MemoryStoreType) Valid() bool {
	switch NormalizeMemoryStoreType(t.String()) {
	case MemoryStoreTypeFile, MemoryStoreTypeVector, MemoryStoreTypeHybrid:
		return true
	default:
		return false
	}
}

func (t MemoryStoreType) UsesFile() bool {
	switch NormalizeMemoryStoreType(t.String()) {
	case MemoryStoreTypeFile, MemoryStoreTypeHybrid:
		return true
	default:
		return false
	}
}

func (t MemoryStoreType) UsesVector() bool {
	switch NormalizeMemoryStoreType(t.String()) {
	case MemoryStoreTypeVector, MemoryStoreTypeHybrid:
		return true
	default:
		return false
	}
}

func (t MemoryStoreType) IsRAGBacked() bool {
	return t.UsesVector()
}

func (m *MemoryConfig) GetStoreType() MemoryStoreType {
	return NormalizeMemoryStoreType(m.StoreType.String())
}

func (m *MemoryConfig) SetStoreType(storeType MemoryStoreType) error {
	parsed, err := ParseMemoryStoreType(storeType.String())
	if err != nil {
		return err
	}
	m.StoreType = parsed
	return nil
}

func (m *MemoryConfig) SetStoreTypeString(raw string) error {
	parsed, err := ParseMemoryStoreType(raw)
	if err != nil {
		return err
	}
	m.StoreType = parsed
	return nil
}

func (c *Config) GetMemoryStoreType() MemoryStoreType {
	return c.Memory.GetStoreType()
}

func (c *Config) SetMemoryStoreType(storeType MemoryStoreType) error {
	if err := c.Memory.SetStoreType(storeType); err != nil {
		return err
	}
	c.applyMemoryLayout()
	return nil
}

func (c *Config) SetMemoryStoreTypeString(raw string) error {
	if err := c.Memory.SetStoreTypeString(raw); err != nil {
		return err
	}
	c.applyMemoryLayout()
	return nil
}

func (c *Config) MemoryVectorDBPath() string {
	return c.CortexDBPath()
}

func (c *Config) MemoryPrimaryPath() string {
	if c.GetMemoryStoreType().UsesVector() && !c.GetMemoryStoreType().UsesFile() {
		return c.MemoryVectorDBPath()
	}
	return c.Memory.MemoryPath
}

func (c *Config) applyMemoryLayout() {
	storeType := c.GetMemoryStoreType()
	if !storeType.Valid() {
		storeType = MemoryStoreTypeFile
	}
	c.Memory.StoreType = storeType

	vectorPath := c.MemoryVectorDBPath()
	filePath := filepath.Join(c.DataDir(), "memories")

	switch storeType {
	case MemoryStoreTypeVector:
		c.Memory.MemoryPath = vectorPath
	case MemoryStoreTypeHybrid:
		if c.Memory.MemoryPath == "" || !filepath.IsAbs(c.Memory.MemoryPath) || sameAbsPath(c.Memory.MemoryPath, vectorPath) {
			c.Memory.MemoryPath = filePath
		}
	case MemoryStoreTypeFile:
		if c.Memory.MemoryPath == "" || !filepath.IsAbs(c.Memory.MemoryPath) || sameAbsPath(c.Memory.MemoryPath, vectorPath) {
			c.Memory.MemoryPath = filePath
		}
	}
}

func sameAbsPath(left, right string) bool {
	return filepath.Clean(left) == filepath.Clean(right)
}
