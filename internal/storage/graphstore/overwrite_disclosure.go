package graphstore

// ReplacedMemory identifies the actual checked predecessor of an unconditional
// Memory mutation. This provisional CLI result is not a public History context:
// attribution is copied as recorded, without inventing a native commit instant.
type ReplacedMemory struct {
	ID          string      `json:"id"`
	Version     string      `json:"version"`
	Attribution Attribution `json:"attribution"`
}

// replacedMemory is called only on changed paths, using the complete predecessor
// already validated inside the mutation transaction. Issue sources are excluded;
// their informational Links do not change the source Issue. Callers discard the
// entire mutation result on every transaction error, including uncertain commit.
func replacedMemory(before any, unconditional bool) *ReplacedMemory {
	memory, ok := before.(Record)
	if !unconditional || !ok {
		return nil
	}
	return &ReplacedMemory{ID: memory.ID, Version: memory.Version, Attribution: memory.Attribution}
}
