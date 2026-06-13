// Package products implements the catalogue service. Each instance owns its
// own products.json file; the gateway runs two instances and coordinates
// strong-consistency replication on top — the service itself is intentionally
// unaware of replication so it can stay simple and easy to reason about.
package products

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
)

// ProductRecord is the canonical product representation. The JSON tags are
// also used as the on-disk format inside products.json.
type ProductRecord struct {
	ID          int     `json:"id"`
	Name        string  `json:"name"`
	Price       float64 `json:"price"`
	Description string  `json:"description"`
}

// ErrProductNotFound is returned when GetByID cannot find a product.
var ErrProductNotFound = errors.New("product not found")

// ErrInvalidProduct is returned when Create receives obviously bad input.
var ErrInvalidProduct = errors.New("invalid product")

// Store is a thread-safe in-memory list of products that flushes the entire
// list to disk on every mutation. The data set in this assignment is tiny,
// so a full rewrite per change is cheap and removes any need for partial
// updates or compaction.
type Store struct {
	storageFilePath string
	mutex           sync.RWMutex
	productsByID    map[int]ProductRecord
	highestID       int
}

// OpenStore reads (or creates) the file at storageFilePath and returns a
// ready-to-use Store. Missing files are treated as an empty catalogue.
func OpenStore(storageFilePath string) (*Store, error) {
	if storageFilePath == "" {
		return nil, errors.New("storage file path is empty")
	}
	store := &Store{
		storageFilePath: storageFilePath,
		productsByID:    make(map[int]ProductRecord),
	}
	if loadError := store.loadFromDisk(); loadError != nil {
		return nil, loadError
	}
	return store, nil
}

func (store *Store) loadFromDisk() error {
	rawBytes, readError := os.ReadFile(store.storageFilePath)
	if errors.Is(readError, os.ErrNotExist) {
		return store.persistLocked() // create an empty file so the next
		// startup does not have to special-case "missing".
	}
	if readError != nil {
		return fmt.Errorf("reading products file: %w", readError)
	}
	if len(rawBytes) == 0 {
		return nil
	}
	var existingProducts []ProductRecord
	if decodeError := json.Unmarshal(rawBytes, &existingProducts); decodeError != nil {
		return fmt.Errorf("decoding products file: %w", decodeError)
	}
	store.mutex.Lock()
	defer store.mutex.Unlock()
	for _, current := range existingProducts {
		store.productsByID[current.ID] = current
		if current.ID > store.highestID {
			store.highestID = current.ID
		}
	}
	return nil
}

// ListAll returns a copy of the current catalogue, sorted by ID so the order
// is deterministic between calls and across replicas.
func (store *Store) ListAll() []ProductRecord {
	store.mutex.RLock()
	defer store.mutex.RUnlock()
	snapshot := make([]ProductRecord, 0, len(store.productsByID))
	for _, current := range store.productsByID {
		snapshot = append(snapshot, current)
	}
	sort.Slice(snapshot, func(leftIndex, rightIndex int) bool {
		return snapshot[leftIndex].ID < snapshot[rightIndex].ID
	})
	return snapshot
}

// GetByID returns the product with the supplied ID or ErrProductNotFound.
func (store *Store) GetByID(productID int) (ProductRecord, error) {
	store.mutex.RLock()
	defer store.mutex.RUnlock()
	if found, ok := store.productsByID[productID]; ok {
		return found, nil
	}
	return ProductRecord{}, ErrProductNotFound
}

// Create inserts a new product, assigns the next ID, and flushes to disk.
// The returned record is the version that was actually persisted.
func (store *Store) Create(productName string, productPrice float64, productDescription string) (ProductRecord, error) {
	if productName == "" {
		return ProductRecord{}, fmt.Errorf("%w: name is required", ErrInvalidProduct)
	}
	if productPrice < 0 {
		return ProductRecord{}, fmt.Errorf("%w: price must be non-negative", ErrInvalidProduct)
	}

	store.mutex.Lock()
	defer store.mutex.Unlock()

	store.highestID++
	created := ProductRecord{
		ID:          store.highestID,
		Name:        productName,
		Price:       productPrice,
		Description: productDescription,
	}
	store.productsByID[created.ID] = created

	if persistError := store.persistLocked(); persistError != nil {
		// Roll back the in-memory change so we never report success while
		// the disk has the previous state.
		delete(store.productsByID, created.ID)
		store.highestID--
		return ProductRecord{}, persistError
	}
	return created, nil
}

// UpsertFromReplication is used by future inter-replica sync paths. It is
// not currently called from a handler, but is included so a follow-up could
// implement read-repair without touching Create's invariant about ID
// assignment.
func (store *Store) UpsertFromReplication(replicated ProductRecord) error {
	store.mutex.Lock()
	defer store.mutex.Unlock()
	store.productsByID[replicated.ID] = replicated
	if replicated.ID > store.highestID {
		store.highestID = replicated.ID
	}
	return store.persistLocked()
}

// persistLocked writes the current catalogue to disk atomically. The caller
// must already hold store.mutex (in either read or write mode is fine —
// this method only reads from the map).
func (store *Store) persistLocked() error {
	if mkdirError := os.MkdirAll(filepath.Dir(store.storageFilePath), 0o755); mkdirError != nil {
		return fmt.Errorf("ensuring products directory: %w", mkdirError)
	}

	currentSnapshot := make([]ProductRecord, 0, len(store.productsByID))
	for _, current := range store.productsByID {
		currentSnapshot = append(currentSnapshot, current)
	}
	sort.Slice(currentSnapshot, func(leftIndex, rightIndex int) bool {
		return currentSnapshot[leftIndex].ID < currentSnapshot[rightIndex].ID
	})

	encodedBytes, encodeError := json.MarshalIndent(currentSnapshot, "", "  ")
	if encodeError != nil {
		return fmt.Errorf("encoding products: %w", encodeError)
	}

	temporaryFilePath := store.storageFilePath + ".tmp"
	if writeError := os.WriteFile(temporaryFilePath, encodedBytes, 0o644); writeError != nil {
		return fmt.Errorf("writing products temp file: %w", writeError)
	}
	if renameError := os.Rename(temporaryFilePath, store.storageFilePath); renameError != nil {
		return fmt.Errorf("replacing products file: %w", renameError)
	}
	return nil
}
