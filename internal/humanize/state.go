package humanize

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

type MemoryFact struct {
	Category  string    `json:"category"`
	Content   string    `json:"content"`
	UpdatedAt time.Time `json:"updated_at"`
}

type SavedMemoryCandidate struct {
	Category string
	Content  string
}

func ExtractMemoryFacts(content string, now time.Time) []MemoryFact {
	sentences := splitSentences(content)
	facts := make([]MemoryFact, 0, len(sentences))

	for _, sentence := range sentences {
		lower := strings.ToLower(strings.TrimSpace(sentence))
		if lower == "" {
			continue
		}

		if value, ok := trimAfterPrefix(sentence, "my name is "); ok {
			facts = append(facts, MemoryFact{Category: "name", Content: "The user's name is " + value + ".", UpdatedAt: now})
			continue
		}
		if value, ok := trimAfterPrefix(sentence, "call me "); ok {
			facts = append(facts, MemoryFact{Category: "name", Content: "The user prefers to be called " + value + ".", UpdatedAt: now})
			continue
		}
		if value, ok := trimAfterPrefix(sentence, "i live in "); ok {
			facts = append(facts, MemoryFact{Category: "location", Content: "The user lives in " + value + ".", UpdatedAt: now})
			continue
		}
		if value, ok := trimAfterPrefix(sentence, "i work at "); ok {
			facts = append(facts, MemoryFact{Category: "workplace", Content: "The user works at " + value + ".", UpdatedAt: now})
			continue
		}
		if value, ok := trimAfterPrefix(sentence, "i work as "); ok {
			facts = append(facts, MemoryFact{Category: "role", Content: "The user works as " + value + ".", UpdatedAt: now})
			continue
		}
		if value, ok := trimAfterPrefix(sentence, "i like "); ok {
			facts = append(facts, MemoryFact{Category: "likes:" + normalizedKey(value), Content: "The user likes " + value + ".", UpdatedAt: now})
			continue
		}
		if value, ok := trimAfterPrefix(sentence, "i love "); ok {
			facts = append(facts, MemoryFact{Category: "likes:" + normalizedKey(value), Content: "The user loves " + value + ".", UpdatedAt: now})
			continue
		}
		if value, ok := trimAfterPrefix(sentence, "i prefer "); ok {
			facts = append(facts, MemoryFact{Category: "preference", Content: "The user prefers " + value + ".", UpdatedAt: now})
			continue
		}
		if strings.HasPrefix(lower, "my favorite ") && strings.Contains(lower, " is ") {
			parts := strings.SplitN(sentence, " is ", 2)
			if len(parts) != 2 {
				continue
			}
			subject := strings.TrimSpace(strings.TrimPrefix(parts[0], "My favorite "))
			value := cleanValue(parts[1])
			if subject == "" || value == "" {
				continue
			}
			facts = append(facts, MemoryFact{
				Category:  "favorite:" + normalizedKey(subject),
				Content:   fmt.Sprintf("The user's favorite %s is %s.", strings.ToLower(subject), value),
				UpdatedAt: now,
			})
		}
	}

	return facts
}

func ExtractSavedMemoryCandidates(content string) []SavedMemoryCandidate {
	sentences := splitSentences(content)
	candidates := make([]SavedMemoryCandidate, 0, len(sentences))

	for _, sentence := range sentences {
		lower := strings.ToLower(strings.TrimSpace(sentence))
		if lower == "" {
			continue
		}

		switch {
		case strings.Contains(lower, "remember that"):
			candidates = append(candidates, SavedMemoryCandidate{
				Category: inferSavedMemoryCategory(lower),
				Content:  cleanSavedMemorySentence(sentence),
			})
		case strings.Contains(lower, "please remember"):
			candidates = append(candidates, SavedMemoryCandidate{
				Category: inferSavedMemoryCategory(lower),
				Content:  cleanSavedMemorySentence(sentence),
			})
		case strings.Contains(lower, "don't forget"):
			candidates = append(candidates, SavedMemoryCandidate{
				Category: inferSavedMemoryCategory(lower),
				Content:  cleanSavedMemorySentence(sentence),
			})
		}
	}

	return compactSavedMemoryCandidates(candidates)
}

func MergeMemoryFacts(existing, incoming []MemoryFact) []MemoryFact {
	if len(incoming) == 0 {
		return trimMemoryFacts(existing, 12)
	}

	index := make(map[string]int, len(existing))
	for i, fact := range existing {
		if fact.Category != "" {
			index[fact.Category] = i
		}
	}

	for _, fact := range incoming {
		if fact.Category != "" {
			if pos, ok := index[fact.Category]; ok {
				existing[pos] = fact
				continue
			}
			index[fact.Category] = len(existing)
		}
		existing = append(existing, fact)
	}

	return trimMemoryFacts(existing, 12)
}

func DeriveMood(previousMood, userMessage, assistantMessage string) (string, string) {
	content := strings.ToLower(strings.TrimSpace(userMessage + "\n" + assistantMessage))

	switch {
	case containsAny(content, "sad", "upset", "anxious", "worried", "depressed", "lonely", "stressed", "hurt"):
		return "gentle and protective", "The recent interaction carries emotional weight, so respond more softly and reassuringly."
	case containsAny(content, "excited", "amazing", "great", "love", "awesome", "celebrate", "yay"):
		return "warmly enthusiastic", "The recent interaction feels upbeat, so lean into positive energy without becoming noisy."
	case containsAny(content, "angry", "frustrated", "annoyed", "hate", "furious"):
		return "careful and steady", "The user appears frustrated, so stay calm, precise, and de-escalating."
	case containsAny(content, "curious", "wonder", "explore", "idea", "brainstorm", "maybe"):
		return "curious and engaged", "The user is exploring ideas, so meet them with thoughtful curiosity."
	default:
		if strings.TrimSpace(previousMood) != "" {
			return previousMood, "No strong new emotional signal appeared, so keep the established tone."
		}
		return "neutral", "No strong emotional signal has appeared yet."
	}
}

func summarizeMemoryFacts(facts []MemoryFact) string {
	if len(facts) == 0 {
		return ""
	}

	lines := make([]string, 0, len(facts))
	for _, fact := range facts {
		lines = append(lines, "- "+fact.Content)
	}
	return strings.Join(lines, "\n")
}

func summarizeRelationship(interactionCount int64, facts []MemoryFact) string {
	if interactionCount <= 1 {
		return "The relationship is just beginning. Be warm, observant, and avoid pretending to know too much."
	}
	if len(facts) == 0 {
		return fmt.Sprintf("You have spoken with this user %d times. Familiarity exists, but personal details are still sparse.", interactionCount)
	}
	return fmt.Sprintf("You have spoken with this user %d times and have a growing set of remembered personal details. Sound familiar, but do not overstate intimacy.", interactionCount)
}

func splitSentences(content string) []string {
	replacer := strings.NewReplacer("!", ".", "?", ".", "\n", ".")
	parts := strings.Split(replacer.Replace(content), ".")
	result := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part != "" {
			result = append(result, part)
		}
	}
	return result
}

func trimAfterPrefix(sentence, prefix string) (string, bool) {
	lower := strings.ToLower(strings.TrimSpace(sentence))
	if !strings.HasPrefix(lower, prefix) {
		return "", false
	}
	value := cleanValue(strings.TrimSpace(sentence)[len(prefix):])
	if value == "" {
		return "", false
	}
	return value, true
}

func cleanValue(value string) string {
	value = strings.TrimSpace(value)
	return strings.Trim(value, "\"' ")
}

func normalizedKey(value string) string {
	value = strings.ToLower(cleanValue(value))
	value = strings.ReplaceAll(value, " ", "-")
	return value
}

func containsAny(content string, keywords ...string) bool {
	for _, keyword := range keywords {
		if strings.Contains(content, keyword) {
			return true
		}
	}
	return false
}

func trimMemoryFacts(facts []MemoryFact, limit int) []MemoryFact {
	if len(facts) <= limit {
		return facts
	}
	sort.SliceStable(facts, func(i, j int) bool {
		return facts[i].UpdatedAt.After(facts[j].UpdatedAt)
	})
	return append([]MemoryFact(nil), facts[:limit]...)
}

func inferSavedMemoryCategory(content string) string {
	switch {
	case containsAny(content, "birthday", "anniversary", "date"):
		return "important-date"
	case containsAny(content, "prefer", "like", "love", "hate", "favorite"):
		return "preference"
	case containsAny(content, "name", "call me"):
		return "identity"
	default:
		return "important"
	}
}

func cleanSavedMemorySentence(sentence string) string {
	sentence = strings.TrimSpace(sentence)
	sentence = strings.TrimPrefix(sentence, "Remember that ")
	sentence = strings.TrimPrefix(sentence, "remember that ")
	sentence = strings.TrimPrefix(sentence, "Please remember ")
	sentence = strings.TrimPrefix(sentence, "please remember ")
	sentence = strings.TrimPrefix(sentence, "Don't forget ")
	sentence = strings.TrimPrefix(sentence, "don't forget ")
	return strings.TrimSpace(sentence)
}

func compactSavedMemoryCandidates(candidates []SavedMemoryCandidate) []SavedMemoryCandidate {
	if len(candidates) == 0 {
		return nil
	}

	seen := make(map[string]struct{}, len(candidates))
	result := make([]SavedMemoryCandidate, 0, len(candidates))
	for _, candidate := range candidates {
		key := strings.ToLower(strings.TrimSpace(candidate.Category + "|" + candidate.Content))
		if key == "|" || candidate.Content == "" {
			continue
		}
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		result = append(result, candidate)
	}
	return result
}
