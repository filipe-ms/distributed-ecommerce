// Package products implementa o serviço de catálogo. Cada instância
// tem seu próprio products.json; o gateway sobe duas instâncias e
// coordena a replicação por cima — o serviço em si não sabe que tem
// réplica, o que deixa ele simples.
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

// ProductRecord é a representação canônica de um produto. As tags JSON
// também são o formato em disco no products.json.
type ProductRecord struct {
	ID          int     `json:"id"`
	Name        string  `json:"name"`
	Price       float64 `json:"price"`
	Description string  `json:"description"`
	Quantity    int     `json:"quantity"`
}

// ErrProductNotFound quando o GetByID não encontra.
var ErrProductNotFound = errors.New("product not found")

// ErrInvalidProduct quando o Create recebe input claramente errado.
var ErrInvalidProduct = errors.New("invalid product")

// ErrOutOfStock quando o DecrementStock é chamado com quantidade zero.
var ErrOutOfStock = errors.New("out of stock")

// Store é uma lista de produtos em memória, thread-safe, que reescreve
// o arquivo inteiro em cada mudança. Como o volume de dados aqui é
// pequeno, regravar tudo é barato.
type Store struct {
	storageFilePath string
	mutex           sync.RWMutex
	productsByID    map[int]ProductRecord
	highestID       int
}

// OpenStore lê (ou cria) o arquivo em storageFilePath e devolve um
// Store pronto pra usar. Se o arquivo não existe, começa vazio.
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
		return store.persistLocked() // cria o arquivo vazio pra próxima
		// inicialização não precisar tratar esse caso.
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

// ListAll devolve uma cópia do catálogo, ordenado por ID pra ordem
// ser sempre a mesma entre chamadas e entre réplicas.
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

// GetByID devolve o produto pelo id, ou ErrProductNotFound.
func (store *Store) GetByID(productID int) (ProductRecord, error) {
	store.mutex.RLock()
	defer store.mutex.RUnlock()
	if found, ok := store.productsByID[productID]; ok {
		return found, nil
	}
	return ProductRecord{}, ErrProductNotFound
}

// Create insere um produto novo, atribui o próximo id e grava em disco.
// Devolve a versão que foi de fato persistida.
func (store *Store) Create(productName string, productPrice float64, productDescription string, productQuantity int) (ProductRecord, error) {
	if productName == "" {
		return ProductRecord{}, fmt.Errorf("%w: name is required", ErrInvalidProduct)
	}
	if productPrice < 0 {
		return ProductRecord{}, fmt.Errorf("%w: price must be non-negative", ErrInvalidProduct)
	}
	if productQuantity < 0 {
		return ProductRecord{}, fmt.Errorf("%w: quantity must be non-negative", ErrInvalidProduct)
	}

	store.mutex.Lock()
	defer store.mutex.Unlock()

	store.highestID++
	created := ProductRecord{
		ID:          store.highestID,
		Name:        productName,
		Price:       productPrice,
		Description: productDescription,
		Quantity:    productQuantity,
	}
	store.productsByID[created.ID] = created

	if persistError := store.persistLocked(); persistError != nil {
		// Desfaz a mudança em memória pra nunca dar sucesso enquanto o
		// disco ainda tem o estado antigo.
		delete(store.productsByID, created.ID)
		store.highestID--
		return ProductRecord{}, persistError
	}
	return created, nil
}

// DecrementStock tira uma unidade do estoque. Devolve ErrOutOfStock
// quando o produto está zerado.
func (store *Store) DecrementStock(productID int) (ProductRecord, error) {
	store.mutex.Lock()
	defer store.mutex.Unlock()

	found, ok := store.productsByID[productID]
	if !ok {
		return ProductRecord{}, ErrProductNotFound
	}
	if found.Quantity <= 0 {
		return ProductRecord{}, ErrOutOfStock
	}
	found.Quantity--
	store.productsByID[productID] = found

	if persistError := store.persistLocked(); persistError != nil {
		found.Quantity++
		store.productsByID[productID] = found
		return ProductRecord{}, persistError
	}
	return found, nil
}

// SeedDefaultsIfEmpty popula o catálogo com alguns itens só pra dar
// pra fazer a demo sem precisar cadastrar nada na mão. Se o arquivo
// já tem produtos, não faz nada.
func (store *Store) SeedDefaultsIfEmpty() error {
	store.mutex.RLock()
	alreadyHasProducts := len(store.productsByID) > 0
	store.mutex.RUnlock()
	if alreadyHasProducts {
		return nil
	}

	seeds := []struct {
		Name     string
		Price    float64
		Quantity int
	}{
		{"Barbeador", 12.50, 10},
		{"Caderno", 8.00, 10},
		{"Chave de fenda", 15.00, 10},
		{"Desodorante", 9.90, 10},
		{"Bandeirinha de São João", 4.00, 10},
	}
	for _, seed := range seeds {
		if _, createError := store.Create(seed.Name, seed.Price, "", seed.Quantity); createError != nil {
			return fmt.Errorf("seeding %q: %w", seed.Name, createError)
		}
	}
	return nil
}

// UpsertFromReplication existiria pra um sync entre réplicas no
// futuro. Não é usado nos handlers atuais, mas tá aqui pra um possível
// read-repair sem mexer no Create.
func (store *Store) UpsertFromReplication(replicated ProductRecord) error {
	store.mutex.Lock()
	defer store.mutex.Unlock()
	store.productsByID[replicated.ID] = replicated
	if replicated.ID > store.highestID {
		store.highestID = replicated.ID
	}
	return store.persistLocked()
}

// persistLocked escreve o catálogo no disco de forma atômica. Quem
// chama tem que estar segurando store.mutex.
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
