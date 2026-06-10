package handler

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/gin-gonic/gin"
)

func writeSSE(c *gin.Context, event string, payload any) {
	bytes, _ := json.Marshal(payload)
	_, _ = fmt.Fprintf(c.Writer, "event: %s\n", event)
	_, _ = fmt.Fprintf(c.Writer, "data: %s\n\n", bytes)
	c.Writer.Flush()
}

func heartbeat(c *gin.Context, interval time.Duration, done <-chan struct{}) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-done:
			return
		case <-c.Request.Context().Done():
			return
		case <-ticker.C:
			writeSSE(c, "heartbeat", gin.H{"ok": true})
		}
	}
}
