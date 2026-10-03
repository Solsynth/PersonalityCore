package service

import (
	"sync"
	"time"

	"github.com/cloudwego/eino/schema"
)

const (
	// defaultImageInlineMaxBytes is how large an image may be before it is sent
	// as a signed link instead of as base64 bytes in the request. The file
	// server's compressed variant of a photograph lands well under this, so the
	// common case is inlined and the request stays small.
	defaultImageInlineMaxBytes = 1 << 20

	// attachmentImageTTL is how long a resolved attachment stays current. A
	// signed link lives fifteen minutes, so this keeps one hand-over inside its
	// life while sparing a conversation that replays the same picture on every
	// turn from fetching it again each time.
	attachmentImageTTL = 10 * time.Minute

	// attachmentImageCacheLimit is the number of pictures held before expired
	// entries are swept. Each one is bounded by the inline limit, so the cache
	// stays in the low tens of megabytes at worst.
	attachmentImageCacheLimit = 64
)

// attachmentImageCache keeps resolved image parts — inlined bytes or a signed
// link — for the time they stay valid.
type attachmentImageCache struct {
	mu      sync.Mutex
	entries map[string]attachmentImageEntry
}

type attachmentImageEntry struct {
	image     *schema.MessageInputImage
	expiresAt time.Time
}

func newAttachmentImageCache() *attachmentImageCache {
	return &attachmentImageCache{entries: make(map[string]attachmentImageEntry)}
}

func (c *attachmentImageCache) get(attachmentID string) (*schema.MessageInputImage, bool) {
	if c == nil {
		return nil, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	entry, ok := c.entries[attachmentID]
	if !ok {
		return nil, false
	}
	if time.Now().After(entry.expiresAt) {
		delete(c.entries, attachmentID)
		return nil, false
	}
	return entry.image, true
}

func (c *attachmentImageCache) put(attachmentID string, image *schema.MessageInputImage) {
	if c == nil || image == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.entries) >= attachmentImageCacheLimit {
		now := time.Now()
		for id, entry := range c.entries {
			if now.After(entry.expiresAt) {
				delete(c.entries, id)
			}
		}
	}
	c.entries[attachmentID] = attachmentImageEntry{
		image:     image,
		expiresAt: time.Now().Add(attachmentImageTTL),
	}
}
