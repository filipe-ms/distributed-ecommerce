package gateway

import (
	"sync"
	"time"
)

// EventKind é o tipo dos eventos que o dashboard mostra. Manter como
// string deixa o JSON auto-explicativo.
type EventKind string

const (
	EventKindServiceDown      EventKind = "DOWN"
	EventKindServiceRecovered EventKind = "RECOVERED"
)

// Event guarda uma transição reportada pelo loop de heartbeat. O
// dashboard consome uma lista desses pra montar o log de atividade.
type Event struct {
	OccurredAt  time.Time `json:"at"`
	ServiceName string    `json:"service"`
	Kind        EventKind `json:"kind"`
}

// EventRing é um buffer circular de capacidade fixa. A gente só guarda
// transições (não cada tick com sucesso), então 50 entradas dão e
// sobram pro histórico que o dashboard mostra.
type EventRing struct {
	mutex          sync.Mutex
	storage        []Event
	nextWriteIndex int
	totalAdded     int
	capacity       int
}

// NewEventRing cria um ring vazio com a capacidade pedida.
func NewEventRing(capacity int) *EventRing {
	if capacity <= 0 {
		capacity = 50
	}
	return &EventRing{
		storage:  make([]Event, capacity),
		capacity: capacity,
	}
}

// Push adiciona um evento. Quando o buffer enche, sobrescreve o mais
// antigo, que é o que o enunciado pede pra "eventos recentes".
func (ring *EventRing) Push(newEvent Event) {
	ring.mutex.Lock()
	defer ring.mutex.Unlock()
	ring.storage[ring.nextWriteIndex] = newEvent
	ring.nextWriteIndex = (ring.nextWriteIndex + 1) % ring.capacity
	ring.totalAdded++
}

// Snapshot devolve uma cópia dos eventos do mais novo pro mais velho,
// que é o jeito que o dashboard quer mostrar.
func (ring *EventRing) Snapshot() []Event {
	ring.mutex.Lock()
	defer ring.mutex.Unlock()

	currentLength := ring.totalAdded
	if currentLength > ring.capacity {
		currentLength = ring.capacity
	}
	if currentLength == 0 {
		return nil
	}

	output := make([]Event, currentLength)
	for offset := 0; offset < currentLength; offset++ {
		// O slot mais recente fica uma posição atrás do nextWriteIndex.
		readIndex := (ring.nextWriteIndex - 1 - offset + ring.capacity) % ring.capacity
		output[offset] = ring.storage[readIndex]
	}
	return output
}
