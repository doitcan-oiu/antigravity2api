package convert

import (
	"container/list"
	"strings"
	"sync"
	"time"
)

const maxCachedSignatureBytes = 256 << 10
const maxSignatureCacheBytes = 16 << 20
const maxSignatureCacheEntries = 4096

type cachedToolSignature struct {
	key, signature string
	expires        time.Time
}

var toolSignatures = struct {
	sync.Mutex
	entries map[string]*list.Element
	order   *list.List
	bytes   int
}{entries: make(map[string]*list.Element), order: list.New()}

func signatureCacheKey(model, id string) string {
	family := strings.ToLower(model)
	if strings.Contains(family, "claude") {
		family = "claude"
	} else if strings.Contains(family, "gemini") {
		family = "gemini"
	}
	return family + "\x00" + id
}
func removeCachedSignature(element *list.Element) {
	entry := element.Value.(cachedToolSignature)
	delete(toolSignatures.entries, entry.key)
	toolSignatures.bytes -= len(entry.key) + len(entry.signature)
	toolSignatures.order.Remove(element)
}

// RememberToolSignature stores signatures only for the exact tool ID returned
// to the caller. Both entry count and bytes are bounded; oversized signatures
// remain available in the response but are not retained by this optional cache.
func RememberToolSignature(model, id, signature string) {
	if id == "" || len(id) > 4096 || len(model) > 512 || signature == "" || signature == "skip_thought_signature_validator" || len(signature) > maxCachedSignatureBytes {
		return
	}
	key := signatureCacheKey(model, id)
	size := len(key) + len(signature)
	now := time.Now()
	toolSignatures.Lock()
	defer toolSignatures.Unlock()
	if existing := toolSignatures.entries[key]; existing != nil {
		removeCachedSignature(existing)
	}
	for len(toolSignatures.entries) >= maxSignatureCacheEntries || toolSignatures.bytes+size > maxSignatureCacheBytes {
		oldest := toolSignatures.order.Back()
		if oldest == nil {
			return
		}
		removeCachedSignature(oldest)
	}
	element := toolSignatures.order.PushFront(cachedToolSignature{key: key, signature: signature, expires: now.Add(time.Hour)})
	toolSignatures.entries[key] = element
	toolSignatures.bytes += size
}
func RecallToolSignature(model, id string) string {
	if id == "" || len(id) > 4096 || len(model) > 512 {
		return ""
	}
	key := signatureCacheKey(model, id)
	toolSignatures.Lock()
	defer toolSignatures.Unlock()
	element := toolSignatures.entries[key]
	if element == nil {
		return ""
	}
	entry := element.Value.(cachedToolSignature)
	if !entry.expires.After(time.Now()) {
		removeCachedSignature(element)
		return ""
	}
	toolSignatures.order.MoveToFront(element)
	return entry.signature
}
